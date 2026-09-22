package routing

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"chenflow-go/internal/adapter"
	"chenflow-go/internal/config"
	"chenflow-go/internal/models"
	"chenflow-go/internal/utils"
)

const (
	ChnRoutesURL           = "https://ispip.clang.cn/all_cn_cidr.txt"
	CIDRBatchSize          = 120
	CIDRBatchDelaySec      = 50 * time.Millisecond
	CIDRReinjectCooldown   = 3600 * time.Second
	GuardCheckInterval     = 30 * time.Second
	GuardRepairCooldown    = 180 * time.Second
	DefaultRouteMetric     = 10
	NonExitInterfaceMetric = 50
	GuardDeadStreakLimit   = 3
)

var cnDNSServers = []string{"223.5.5.5", "119.29.29.29", "114.114.114.114", "180.76.76.76"}

type AdapterDeadError struct{ Msg string }

func (e *AdapterDeadError) Error() string { return e.Msg }

type LogFunc func(msg string, level string)

func safeLog(log LogFunc, msg, level string) {
	if log != nil {
		log(msg, level)
	}
}

type netRoute struct {
	Dest    string `json:"dest"`
	NextHop string `json:"next_hop"`
	IfIndex int    `json:"if_index"`
	Metric  int    `json:"metric"`
}

type netInterface struct {
	IfIndex int    `json:"if_index"`
	Metric  int    `json:"metric"`
	State   string `json:"state"`
}

type netState struct {
	Routes     []netRoute     `json:"routes"`
	Interfaces []netInterface `json:"interfaces"`
}

type desiredEntry struct {
	IfIndex int    `json:"if_index"`
	GUID    string `json:"guid"`
	Metric  int    `json:"metric"`
	Gateway string `json:"gateway"`
}

type desiredState struct {
	Exit             *desiredEntry `json:"exit"`
	CN               *desiredEntry `json:"cn"`
	ExitDefaultRoute *netRoute     `json:"exit_default_route"`
}

type Engine struct {
	adapters *adapter.Manager
	store    *config.Store
	mu       sync.Mutex

	guardRunning    atomic.Bool
	lastGuardRepair time.Time

	cidrInjecting  atomic.Bool
	cidrInjectKey  string
	cidrInjectTime time.Time

	deadStreak atomic.Int32
}

func NewEngine(am *adapter.Manager, store *config.Store) *Engine {
	return &Engine{
		adapters: am,
		store:    store,
	}
}

func (e *Engine) ApplyRoles(log LogFunc, injectCIDR bool) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.adapters.RefreshByMAC()
	exitProf := e.adapters.DefaultExit()
	if exitProf == nil {
		safeLog(log, "未配置「默认出口」网卡，请先在 系统设置 → 网卡管理 中指派。", "error")
		return false
	}
	if !exitProf.DefaultRouteReady() {
		hint := "离线"
		if exitProf.IsUp {
			hint = "无网关"
		}
		safeLog(log, fmt.Sprintf("网卡 [%s] 已设为默认出口，但当前不可用（%s）。请点击「刷新网卡列表」后重试。", exitProf.Name, hint), "error")
		return false
	}

	ok := e.applyDefaultRoute(exitProf, log)

	cnProf := e.adapters.CNSplit()
	if injectCIDR && cnProf != nil && cnProf.RouteReady() {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					safeLog(log, fmt.Sprintf("CIDR 注入异常: %v", r), "error")
				}
			}()
			success := e.injectChnRoutes(cnProf, log, false)
			if success {
				safeLog(log, fmt.Sprintf("国内分流已生效，路由通过 [%s] 网卡。", cnProf.Name), "success")
			} else {
				safeLog(log, fmt.Sprintf("国内分流注入失败，[%s] 网卡的路由未生效，请检查网卡状态。", cnProf.Name), "error")
			}
		}()
	} else if cnProf != nil && !cnProf.RouteReady() {
		safeLog(log, fmt.Sprintf("网卡 [%s] 已设为国内分流，但当前不可用，已跳过 CIDR 注入。", cnProf.Name), "warn")
	}
	return ok
}

func (e *Engine) injectChnRoutes(profile *models.AdapterProfile, log LogFunc, force bool) bool {
	if !e.cidrInjecting.CompareAndSwap(false, true) {
		safeLog(log, "国内路由正在注入中，请稍候...", "warn")
		return false
	}
	defer e.cidrInjecting.Store(false)

	if !e.reconcile(log) {
		return false
	}

	injectKey := fmt.Sprintf("%s|%d|%s", profile.Name, profile.IfIndex, profile.Gateway())
	if !force && injectKey == e.cidrInjectKey && time.Since(e.cidrInjectTime) < CIDRReinjectCooldown {
		safeLog(log, "国内路由已在本次会话中注入，跳过重复注入（防止系统卡顿）。", "info")
		return true
	}

	cidrList := e.store.LoadCIDRCache()
	if len(cidrList) == 0 {
		cidrList = e.downloadChnRoutes(log)
	}
	if len(cidrList) == 0 {
		safeLog(log, "国内 CIDR 数据为空，跳过国内分流注入。", "warn")
		return false
	}

	var commands []string
	gw := profile.Gateway()
	idx := profile.IfIndex

	for _, dns := range cnDNSServers {
		commands = append(commands, fmt.Sprintf("route add %s mask 255.255.255.255 %s metric 1 IF %d", dns, gw, idx))
	}

	for _, line := range cidrList {
		if !strings.Contains(line, "/") {
			continue
		}
		parts := strings.SplitN(line, "/", 2)
		ip := parts[0]
		var cidrBits int
		fmt.Sscanf(parts[1], "%d", &cidrBits)
		if cidrBits < 0 || cidrBits > 32 {
			continue
		}
		mask := utils.CIDRToNetmask(cidrBits)
		commands = append(commands, fmt.Sprintf("route add %s mask %s %s metric 1 IF %d", ip, mask, gw, idx))
	}

	if len(commands) == 0 {
		return false
	}

	total := len(commands)
	safeLog(log, fmt.Sprintf("开始向 [%s] 分批注入 %d 条国内路由（约 %d 批）...", profile.Name, total, (total+CIDRBatchSize-1)/CIDRBatchSize), "info")

	injectedOK := 0
	var injectedCmds []string

	for i := 0; i < total; i += CIDRBatchSize {
		end := i + CIDRBatchSize
		if end > total {
			end = total
		}
		chunk := commands[i:end]
		okCount, errCount, dead := runRouteBatch(chunk)
		if dead {
			safeLog(log, "检测到网卡底层假死（错误 1231），正在回滚已注入路由...", "error")
			e.rollbackInjected(injectedCmds, log)
			panic(&AdapterDeadError{Msg: "检测到网卡底层假死（错误 1231），请关闭无线网卡节能模式或以管理员运行 netsh winsock reset 重置网络栈"})
		}
		injectedOK += okCount
		if errCount > 0 {
			injectedCmds = append(injectedCmds, chunk[:okCount]...)
		} else {
			injectedCmds = append(injectedCmds, chunk...)
		}
		if errCount > len(chunk)/2 {
			safeLog(log, fmt.Sprintf("国内路由注入在第 %d 条附近大面积失败（%d/%d），正在回滚...", injectedOK, errCount, len(chunk)), "error")
			e.rollbackInjected(injectedCmds, log)
			return false
		}
		time.Sleep(CIDRBatchDelaySec)
	}

	e.cidrInjectKey = injectKey
	e.cidrInjectTime = time.Now()
	e.deadStreak.Store(0)

	safeLog(log, fmt.Sprintf("国内白名单分流注入完成（共 %d/%d 条生效）。", injectedOK, total), "success")
	return true
}

func (e *Engine) rollbackInjected(injectedCmds []string, log LogFunc) {
	if len(injectedCmds) == 0 {
		return
	}
	rollbackCmds := make([]string, len(injectedCmds))
	for i, c := range injectedCmds {
		rollbackCmds[i] = strings.Replace(c, "route add", "route delete", 1)
	}
	for i := 0; i < len(rollbackCmds); i += CIDRBatchSize {
		end := i + CIDRBatchSize
		if end > len(rollbackCmds) {
			end = len(rollbackCmds)
		}
		runRouteBatch(rollbackCmds[i:end])
		time.Sleep(CIDRBatchDelaySec)
	}
	safeLog(log, fmt.Sprintf("已回滚 %d 条路由，国内分流未生效。", len(injectedCmds)), "error")
}

func runRouteBatch(commands []string) (okCount, errCount int, dead bool) {
	tmpDir := os.TempDir()
	batPath := filepath.Join(tmpDir, "_inject_chnroutes_part.bat")
	var sb strings.Builder
	sb.WriteString("@echo off\n")
	for _, c := range commands {
		sb.WriteString(c)
		sb.WriteString("\n")
	}
	if err := os.WriteFile(batPath, []byte(sb.String()), 0644); err != nil {
		return 0, len(commands), false
	}
	defer os.Remove(batPath)

	cmd := exec.Command("cmd", "/c", batPath)
	utils.HideWindow(cmd)
	out, err := cmd.CombinedOutput()
	output := utils.DecodeGBK(out)
	if err != nil && len(out) == 0 {
		return 0, len(commands), false
	}
	dead = detectDeadAdapter(output)
	errMarkers := []string{"错误", "Error", "error", "无法找到", "The route"}
	for _, line := range strings.Split(output, "\n") {
		for _, m := range errMarkers {
			if strings.Contains(line, m) {
				errCount++
				break
			}
		}
	}
	okCount = len(commands) - errCount
	if okCount < 0 {
		okCount = 0
	}
	if okCount > len(commands) {
		okCount = len(commands)
	}
	return
}

func detectDeadAdapter(output string) bool {
	return strings.Contains(output, "1231") || strings.Contains(output, "一般故障") || strings.Contains(output, "General failure")
}

func (e *Engine) getManagedInterfaceIndexes() map[int]bool {
	managed := make(map[int]bool)
	defaultExitName := e.store.GetDefaultExitAdapter()
	for _, p := range e.adapters.Profiles() {
		if p.Role == models.RoleIgnored && p.Name != defaultExitName {
			continue
		}
		if p.IfIndex != 0 {
			managed[p.IfIndex] = true
		}
	}
	return managed
}

func (e *Engine) deleteManagedDefaultRoutes(log LogFunc) int {
	managedIdxs := e.getManagedInterfaceIndexes()
	if len(managedIdxs) == 0 {
		return 0
	}
	ipToIdx := make(map[string]int)
	for _, p := range e.adapters.Profiles() {
		if p.IP != "" && p.IfIndex != 0 {
			ipToIdx[p.IP] = p.IfIndex
		}
	}
	output := utils.RunCmd("route print -4", 15)
	if output == "" {
		return 0
	}
	deleted := 0
	inActive := false
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "活动路由") || strings.Contains(line, "Active Routes") {
			inActive = true
			continue
		}
		if strings.Contains(line, "永久路由") || strings.Contains(line, "Persistent Routes") {
			break
		}
		if !inActive || strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 5 && parts[0] == "0.0.0.0" && parts[1] == "0.0.0.0" {
			gw := parts[2]
			ifaceIP := parts[3]
			ifIdx, ok := ipToIdx[ifaceIP]
			if ok && managedIdxs[ifIdx] {
				utils.RunCmd(fmt.Sprintf("route delete 0.0.0.0 mask 0.0.0.0 %s", gw), 10)
				deleted++
				safeLog(log, fmt.Sprintf("[ROUTE] 删除管理接口默认路由: gw=%s if_index=%d（第三方/VPN 路由保留）", gw, ifIdx), "info")
			}
		}
	}
	return deleted
}

func (e *Engine) getNetStatePS() netState {
	state := netState{Routes: []netRoute{}, Interfaces: []netInterface{}}
	out1 := utils.RunPS(`Get-NetRoute -AddressFamily IPv4 | Select-Object DestinationPrefix,NextHop,InterfaceIndex,RouteMetric | ConvertTo-Json -Compress`)
	if len(out1) > 0 {
		var items []map[string]interface{}
		if json.Unmarshal([]byte(out1), &items) == nil {
			for _, item := range items {
				state.Routes = append(state.Routes, netRoute{
					Dest:    getStr(item, "DestinationPrefix"),
					NextHop: getStr(item, "NextHop"),
					IfIndex: getInt(item, "InterfaceIndex"),
					Metric:  getInt(item, "RouteMetric"),
				})
			}
		} else {
			var single map[string]interface{}
			if json.Unmarshal([]byte(out1), &single) == nil {
				state.Routes = append(state.Routes, netRoute{
					Dest:    getStr(single, "DestinationPrefix"),
					NextHop: getStr(single, "NextHop"),
					IfIndex: getInt(single, "InterfaceIndex"),
					Metric:  getInt(single, "RouteMetric"),
				})
			}
		}
	}
	out2 := utils.RunPS(`Get-NetIPInterface -AddressFamily IPv4 | Select-Object InterfaceIndex,InterfaceMetric,ConnectionState | ConvertTo-Json -Compress`)
	if len(out2) > 0 {
		var items []map[string]interface{}
		if json.Unmarshal([]byte(out2), &items) == nil {
			for _, item := range items {
				state.Interfaces = append(state.Interfaces, netInterface{
					IfIndex: getInt(item, "InterfaceIndex"),
					Metric:  getInt(item, "InterfaceMetric"),
					State:   getStr(item, "ConnectionState"),
				})
			}
		} else {
			var single map[string]interface{}
			if json.Unmarshal([]byte(out2), &single) == nil {
				state.Interfaces = append(state.Interfaces, netInterface{
					IfIndex: getInt(single, "InterfaceIndex"),
					Metric:  getInt(single, "InterfaceMetric"),
					State:   getStr(single, "ConnectionState"),
				})
			}
		}
	}
	if len(state.Routes) > 0 || len(state.Interfaces) > 0 {
		return state
	}
	return e.getNetStateFallback()
}

func (e *Engine) getNetStateFallback() netState {
	state := netState{Routes: []netRoute{}, Interfaces: []netInterface{}}
	output := utils.RunCmd("route print -4", 15)
	if output == "" {
		return state
	}
	ipToIdx := make(map[string]int)
	for _, p := range e.adapters.Profiles() {
		if p.IP != "" && p.IfIndex != 0 {
			ipToIdx[p.IP] = p.IfIndex
		}
	}
	inActive := false
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "活动路由") || strings.Contains(line, "Active Routes") {
			inActive = true
			continue
		}
		if strings.Contains(line, "永久路由") || strings.Contains(line, "Persistent Routes") {
			break
		}
		if !inActive || strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 5 {
			dest := parts[0]
			mask := parts[1]
			gw := parts[2]
			ifaceIP := parts[3]
			ifIdx := ipToIdx[ifaceIP]
			prefix := 0
			for _, o := range strings.Split(mask, ".") {
				var v int
				fmt.Sscanf(o, "%d", &v)
				for i := 0; i < 8; i++ {
					if (v>>i)&1 == 1 {
						prefix++
					}
				}
			}
			var metric int
			fmt.Sscanf(parts[4], "%d", &metric)
			state.Routes = append(state.Routes, netRoute{
				Dest:    fmt.Sprintf("%s/%d", dest, prefix),
				NextHop: gw,
				IfIndex: ifIdx,
				Metric:  metric,
			})
		}
	}
	return state
}

func (e *Engine) computeDesiredState() desiredState {
	exitProf := e.adapters.DefaultExit()
	cnProf := e.adapters.CNSplit()
	desired := desiredState{}
	if exitProf != nil && exitProf.DefaultRouteReady() {
		desired.Exit = &desiredEntry{
			IfIndex: exitProf.IfIndex,
			GUID:    exitProf.InterfaceGUID,
			Metric:  DefaultRouteMetric,
			Gateway: exitProf.Gateway(),
		}
		desired.ExitDefaultRoute = &netRoute{
			Dest:    "0.0.0.0/0",
			NextHop: exitProf.Gateway(),
			IfIndex: exitProf.IfIndex,
		}
	}
	if cnProf != nil && cnProf.RouteReady() {
		desired.CN = &desiredEntry{
			IfIndex: cnProf.IfIndex,
			GUID:    cnProf.InterfaceGUID,
			Metric:  NonExitInterfaceMetric,
			Gateway: cnProf.Gateway(),
		}
	}
	return desired
}

func (e *Engine) reconcile(log LogFunc) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	actual := e.getNetStatePS()
	desired := e.computeDesiredState()
	if desired.Exit == nil {
		safeLog(log, "[RECONCILE] 无可用出口网卡，跳过。", "warn")
		return true
	}
	managedIdxs := e.getManagedInterfaceIndexes()
	changed := false

	type metricCheck struct {
		ifIdx  int
		metric int
	}
	checks := []metricCheck{
		{desired.Exit.IfIndex, DefaultRouteMetric},
	}
	if desired.CN != nil {
		checks = append(checks, metricCheck{desired.CN.IfIndex, NonExitInterfaceMetric})
	}
	for _, mc := range checks {
		if mc.ifIdx == 0 || !managedIdxs[mc.ifIdx] {
			continue
		}
		var actualIf *netInterface
		for i := range actual.Interfaces {
			if actual.Interfaces[i].IfIndex == mc.ifIdx {
				actualIf = &actual.Interfaces[i]
				break
			}
		}
		if actualIf != nil && actualIf.Metric != mc.metric {
			safeLog(log, fmt.Sprintf("[RECONCILE] 修正 metric: if=%d %d→%d", mc.ifIdx, actualIf.Metric, mc.metric), "info")
			e.lockInterfaceMetrics(e.adapters.DefaultExit(), log)
			changed = true
			break
		}
	}

	exitRoute := desired.ExitDefaultRoute
	exitGw := exitRoute.NextHop
	exitIdx := exitRoute.IfIndex
	hasExitRoute := false
	for _, r := range actual.Routes {
		if r.Dest == "0.0.0.0/0" && r.NextHop == exitGw && r.IfIndex == exitIdx {
			hasExitRoute = true
			break
		}
	}
	if !hasExitRoute {
		safeLog(log, fmt.Sprintf("[RECONCILE] 出口默认路由缺失，添加: %s IF %d", exitGw, exitIdx), "info")
		e.deleteManagedDefaultRoutes(log)
		ifSuffix := ""
		if exitIdx != 0 {
			ifSuffix = fmt.Sprintf(" IF %d", exitIdx)
		}
		utils.RunCmd(fmt.Sprintf("route add 0.0.0.0 mask 0.0.0.0 %s metric %d%s", exitGw, DefaultRouteMetric, ifSuffix), 15)
		changed = true
	}

	if desired.CN != nil {
		cnIdx := desired.CN.IfIndex
		if managedIdxs[cnIdx] {
			for _, r := range actual.Routes {
				if r.Dest == "0.0.0.0/0" && r.IfIndex == cnIdx {
					safeLog(log, fmt.Sprintf("[RECONCILE] 删除 CN 接口多余默认路由: gw=%s if=%d", r.NextHop, cnIdx), "info")
					utils.RunCmd(fmt.Sprintf("route delete 0.0.0.0 mask 0.0.0.0 %s", r.NextHop), 10)
					changed = true
				}
			}
		}
	}

	if !verifyDefaultRoute(exitGw) {
		safeLog(log, "[RECONCILE] 验证失败：出口默认路由未生效。", "error")
		return false
	}
	if changed {
		safeLog(log, "[RECONCILE] 完成，状态已修正。", "success")
	} else {
		safeLog(log, "[RECONCILE] 完成，无需修改。", "info")
	}
	return true
}

func (e *Engine) sanitizeDefaultRoutes(log LogFunc) bool {
	exitProf := e.adapters.DefaultExit()
	if exitProf == nil || exitProf.Gateway() == "" {
		safeLog(log, "环境净化跳过：未获取到默认出口网关，保留现有默认路由。", "warn")
		return true
	}
	gw := exitProf.Gateway()
	idx := exitProf.IfIndex
	e.lockInterfaceMetrics(exitProf, log)
	safeLog(log, "[ROUTE] 清理管理接口默认路由（保留第三方/VPN 路由）...", "info")
	e.deleteManagedDefaultRoutes(log)
	ifSuffix := ""
	if idx != 0 {
		ifSuffix = fmt.Sprintf(" IF %d", idx)
	}
	addCmd := fmt.Sprintf("route add 0.0.0.0 mask 0.0.0.0 %s metric %d%s", gw, DefaultRouteMetric, ifSuffix)
	out := utils.RunCmd(addCmd, 60)
	if detectDeadAdapter(out) {
		safeLog(log, "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!", "error")
		safeLog(log, "检测到网卡底层假死（错误 1231）！！", "error")
		safeLog(log, "处理方法：请关闭无线网卡节能模式，", "error")
		safeLog(log, "或以管理员身份运行 netsh winsock reset 重置网络栈。", "error")
		safeLog(log, "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!", "error")
		panic(&AdapterDeadError{Msg: "检测到网卡底层假死（错误 1231），请关闭无线网卡节能模式或以管理员运行 netsh winsock reset 重置网络栈"})
	}
	if !verifyDefaultRoute(gw) {
		safeLog(log, "环境净化后默认路由验证失败，中止本次注入，请检查网卡状态。", "error")
		return false
	}
	bindNote := ""
	if idx != 0 {
		bindNote = fmt.Sprintf("，IF %d", idx)
	} else {
		bindNote = "（无IF，索引未探到）"
	}
	safeLog(log, fmt.Sprintf("环境净化完成：遗留默认路由已清空，兜底出口指向 %s（metric %d%s）。", gw, DefaultRouteMetric, bindNote), "success")
	e.verifyInterfaceMetrics(exitProf, log)
	return true
}

func (e *Engine) lockInterfaceMetrics(exitProfile *models.AdapterProfile, log LogFunc) bool {
	allOK := true
	defaultExitName := ""
	if exitProfile != nil {
		defaultExitName = exitProfile.Name
	}
	for _, profile := range e.adapters.Profiles() {
		if !profile.IsUp || (profile.Role == models.RoleIgnored && profile.Name != defaultExitName) {
			continue
		}
		isExit := exitProfile != nil && profile.Name == exitProfile.Name
		metric := NonExitInterfaceMetric
		if isExit {
			metric = DefaultRouteMetric
		}
		idx := profile.IfIndex
		var cmd string
		if idx != 0 {
			cmd = fmt.Sprintf("netsh interface ip set interface interface=%d metric=%d", idx, metric)
		} else {
			cmd = fmt.Sprintf(`netsh interface ip set interface "%s" metric=%d`, profile.Name, metric)
		}
		tag := "分流"
		if isExit {
			tag = "出口"
		}
		idxStr := "N/A"
		if idx != 0 {
			idxStr = fmt.Sprintf("%d", idx)
		}
		safeLog(log, fmt.Sprintf("[ROUTE] 设置接口跃点：%s网卡 [%s] index=%s metric=%d", tag, profile.Name, idxStr, metric), "info")
		out := utils.RunCmd(cmd, 10)
		if strings.Contains(out, "错误") || strings.Contains(out, "Error") {
			safeLog(log, fmt.Sprintf("[ROUTE] 接口跃点设置失败：[%s] output=%s", profile.Name, strings.TrimSpace(out)), "warn")
			allOK = false
		}
	}
	return allOK
}

func (e *Engine) verifyInterfaceMetrics(exitProfile *models.AdapterProfile, log LogFunc) bool {
	output := utils.RunCmd("netsh interface ip show interfaces", 15)
	if output == "" {
		return true
	}
	allOK := true
	defaultExitName := ""
	if exitProfile != nil {
		defaultExitName = exitProfile.Name
	}
	for _, profile := range e.adapters.Profiles() {
		if !profile.IsUp || (profile.Role == models.RoleIgnored && profile.Name != defaultExitName) || profile.IfIndex == 0 {
			continue
		}
		expected := NonExitInterfaceMetric
		if exitProfile != nil && profile.Name == exitProfile.Name {
			expected = DefaultRouteMetric
		}
		for _, line := range strings.Split(output, "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 && parts[0] == fmt.Sprintf("%d", profile.IfIndex) {
				var actual int
				fmt.Sscanf(parts[1], "%d", &actual)
				if actual != expected {
					safeLog(log, fmt.Sprintf("[ROUTE] 跃点验证失败：[%s] index=%d 期望=%d 实际=%d", profile.Name, profile.IfIndex, expected, actual), "warn")
					allOK = false
				}
				break
			}
		}
	}
	if allOK {
		safeLog(log, "[ROUTE] 接口跃点验证通过。", "info")
	}
	return allOK
}

func (e *Engine) DownloadChnRoutes(log LogFunc) []string {
	return e.downloadChnRoutes(log)
}

func (e *Engine) downloadChnRoutes(log LogFunc) []string {
	safeLog(log, "正在获取最新中国大陆 CIDR 数据库...", "info")
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", ChnRoutesURL, nil)
	if err != nil {
		safeLog(log, fmt.Sprintf("CIDR 数据下载失败: %v", err), "warn")
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		safeLog(log, fmt.Sprintf("CIDR 数据下载失败: %v", err), "warn")
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		safeLog(log, fmt.Sprintf("CIDR 数据读取失败: %v", err), "warn")
		return nil
	}
	var lines []string
	for _, ln := range strings.Split(string(body), "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" && !strings.HasPrefix(ln, "#") {
			lines = append(lines, ln)
		}
	}
	if len(lines) > 0 {
		e.store.SaveCIDRCache(lines)
		e.store.SaveCIDRLastUpdate(float64(time.Now().Unix()))
	}
	return lines
}

func (e *Engine) ApplyRule(rule *models.SplitRule) bool {
	profile := e.adapters.GetProfile(rule.Adapter)
	if profile == nil || !profile.Ready() {
		return false
	}
	ips := rule.IPs
	if len(ips) == 0 && rule.Type == "URL" {
		looked, err := net.LookupIP(rule.Target)
		if err != nil {
			return false
		}
		for _, ip := range looked {
			if v4 := ip.To4(); v4 != nil {
				ips = append(ips, v4.String())
			}
		}
		rule.IPs = ips
	}
	if len(ips) == 0 {
		return false
	}
	gw := profile.Gateway()
	idx := profile.IfIndex
	for _, ip := range ips {
		var cmd string
		if rule.Type == "IPv6" {
			cmd = fmt.Sprintf(`netsh interface ipv6 add route %s/128 %d %s`, ip, idx, gw)
		} else {
			cmd = fmt.Sprintf("route add %s mask 255.255.255.255 %s metric 1 IF %d", ip, gw, idx)
		}
		if cmdFailed(utils.RunCmd(cmd, 10)) {
			return false
		}
	}
	return true
}

func (e *Engine) RemoveRuleRoutes(rule *models.SplitRule) {
	if rule == nil {
		return
	}
	profile := e.adapters.GetProfile(rule.Adapter)
	if profile == nil {
		return
	}
	gw := profile.Gateway()
	if gw == "" {
		return
	}
	for _, ip := range rule.IPs {
		if rule.Type == "IPv6" {
			utils.RunCmd(fmt.Sprintf(`netsh interface ipv6 delete route %s/128 %d %s`, ip, profile.IfIndex, gw), 5)
		} else {
			utils.RunCmd(fmt.Sprintf("route delete %s mask 255.255.255.255 %s", ip, gw), 5)
		}
	}
}

func (e *Engine) ApplyAllRules(rules []*models.SplitRule, doneCB func(applied, failed int, failedTargets []string), progressCB func(current, total int, message string)) {
	go func() {
		applied, failed := 0, 0
		var failedTargets []string
		total := len(rules)
		for i, rule := range rules {
			if progressCB != nil {
				progressCB(i, total, fmt.Sprintf("正在应用规则 %d/%d: %s", i+1, total, rule.Target))
			}
			if e.ApplyRule(rule) {
				rule.Status = "Active"
				applied++
			} else {
				rule.Status = "Pending"
				failed++
				failedTargets = append(failedTargets, rule.Target)
			}
		}
		e.store.SaveRules(rules)
		if progressCB != nil {
			progressCB(total, total, "规则应用完成")
		}
		if doneCB != nil {
			doneCB(applied, failed, failedTargets)
		}
	}()
}

func (e *Engine) Verify(routes string) bool {
	exitProf := e.adapters.DefaultExit()
	if exitProf == nil || !exitProf.DefaultRouteReady() {
		return true
	}
	if routes == "" {
		routes = utils.RunCmd("route print -4", 15)
	}
	if routes == "" {
		return true
	}
	pattern := fmt.Sprintf(`0\.0\.0\.0\s+0\.0\.0\.0\s+%s(?:\s|$)`, regexp.QuoteMeta(exitProf.Gateway()))
	matched, err := regexp.MatchString(pattern, routes)
	if err != nil {
		return true
	}
	return matched
}

func (e *Engine) VerifyCIDR(routes string) bool {
	cnProf := e.adapters.CNSplit()
	if cnProf == nil || !cnProf.RouteReady() {
		return true
	}
	if e.cidrInjectKey == "" {
		return true
	}
	cidrList := e.store.LoadCIDRCache()
	if len(cidrList) == 0 {
		return true
	}
	sampleCount := 5
	if len(cidrList) < sampleCount {
		sampleCount = len(cidrList)
	}
	rand.Shuffle(len(cidrList), func(i, j int) { cidrList[i], cidrList[j] = cidrList[j], cidrList[i] })
	samples := cidrList[:sampleCount]
	if routes == "" {
		routes = utils.RunCmd("route print -4", 15)
	}
	if routes == "" {
		return true
	}
	for _, line := range samples {
		if !strings.Contains(line, "/") {
			continue
		}
		parts := strings.SplitN(line, "/", 2)
		ip := parts[0]
		var cidrBits int
		fmt.Sscanf(parts[1], "%d", &cidrBits)
		if cidrBits < 0 || cidrBits > 32 {
			continue
		}
		mask := utils.CIDRToNetmask(cidrBits)
		pattern := fmt.Sprintf(`%s\s+%s(?:\s|$)`, regexp.QuoteMeta(ip), regexp.QuoteMeta(mask))
		matched, err := regexp.MatchString(pattern, routes)
		if err != nil {
			return true
		}
		if !matched {
			return false
		}
	}
	return true
}

func (e *Engine) Repair(log LogFunc) {
	exitProf := e.adapters.DefaultExit()
	if exitProf != nil && exitProf.DefaultRouteReady() {
		e.applyDefaultRoute(exitProf, log)
	}
	if !e.VerifyCIDR("") {
		cnProf := e.adapters.CNSplit()
		if cnProf != nil && cnProf.RouteReady() {
			safeLog(log, "检测到国内分流路由缺失，正在重新注入...", "warn")
			func() {
				defer func() {
					if r := recover(); r != nil {
						if ade, ok := r.(*AdapterDeadError); ok {
							e.deadStreak.Add(1)
							safeLog(log, ade.Error(), "error")
							if e.deadStreak.Load() >= int32(GuardDeadStreakLimit) {
								safeLog(log, "网卡连续假死，自动修复已暂停，请先解决假死问题。", "warn")
							}
						} else {
							safeLog(log, fmt.Sprintf("修复异常: %v", r), "error")
						}
					}
				}()
				e.injectChnRoutes(cnProf, log, true)
			}()
		}
	}
}

func (e *Engine) GuardLoop(log LogFunc, interval time.Duration) {
	if interval == 0 {
		interval = GuardCheckInterval
	}
	for e.guardRunning.Load() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					safeLog(log, fmt.Sprintf("守护检查单轮异常（已跳过，下轮继续）: %v", r), "warn")
				}
			}()
			time.Sleep(interval)
			if !e.guardRunning.Load() {
				return
			}
			if e.cidrInjecting.Load() {
				return
			}
			if e.deadStreak.Load() >= int32(GuardDeadStreakLimit) {
				return
			}
			routes := utils.RunCmd("route print -4", 15)
			needRepair := false
			if !e.Verify(routes) {
				needRepair = true
			} else if !e.VerifyCIDR(routes) {
				needRepair = true
			}
			if !needRepair {
				return
			}
			now := time.Now()
			if now.Sub(e.lastGuardRepair) < GuardRepairCooldown {
				return
			}
			e.mu.Lock()
			if !e.guardRunning.Load() || e.cidrInjecting.Load() {
				e.mu.Unlock()
				return
			}
			fresh := utils.RunCmd("route print -4", 15)
			if e.Verify(fresh) && e.VerifyCIDR(fresh) {
				e.mu.Unlock()
				return
			}
			e.mu.Unlock()
			safeLog(log, "检测到路由漂移，正在修复...", "warn")
			e.adapters.RefreshByMAC()
			e.Repair(log)
			e.lastGuardRepair = now
		}()
	}
}

func (e *Engine) StartGuard(log LogFunc) {
	if !e.guardRunning.CompareAndSwap(false, true) {
		return
	}
	go e.GuardLoop(log, 0)
}

func (e *Engine) StopGuard() {
	e.guardRunning.Store(false)
}

func (e *Engine) purgeRoutesOnManagedInterfaces(log LogFunc) int {
	managedIdxs := e.getManagedInterfaceIndexes()
	if len(managedIdxs) == 0 {
		return 0
	}
	ipToIdx := make(map[string]int)
	for _, p := range e.adapters.Profiles() {
		if p.IP != "" && p.IfIndex != 0 {
			ipToIdx[p.IP] = p.IfIndex
		}
	}
	output := utils.RunCmd("route print -4", 15)
	if output == "" {
		return 0
	}
	var deleteCmds []string
	inActive := false
	systemDests := map[string]bool{
		"0.0.0.0": true, "127.0.0.0": true, "224.0.0.0": true, "255.255.255.255": true,
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "活动路由") || strings.Contains(line, "Active Routes") {
			inActive = true
			continue
		}
		if strings.Contains(line, "永久路由") || strings.Contains(line, "Persistent Routes") {
			break
		}
		if !inActive || strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 5 {
			continue
		}
		dest := parts[0]
		gw := parts[2]
		ifaceIP := parts[3]
		if systemDests[dest] {
			continue
		}
		if gw == "0.0.0.0" {
			continue
		}
		ifIdx, ok := ipToIdx[ifaceIP]
		if ok && managedIdxs[ifIdx] {
			deleteCmds = append(deleteCmds, fmt.Sprintf("route delete %s mask %s %s", dest, parts[1], gw))
		}
	}
	deleted := 0
	for i := 0; i < len(deleteCmds); i += CIDRBatchSize {
		end := i + CIDRBatchSize
		if end > len(deleteCmds) {
			end = len(deleteCmds)
		}
		okCount, _, _ := runRouteBatch(deleteCmds[i:end])
		deleted += okCount
		time.Sleep(CIDRBatchDelaySec)
	}
	if deleted > 0 {
		safeLog(log, fmt.Sprintf("[ROUTE] 扫描路由表额外清理了 %d 条残留路由", deleted), "info")
	}
	return deleted
}

func (e *Engine) RemoveStrategyRoutes(s *models.Strategy, log LogFunc) {
	if s == nil || s.IsFallback() {
		return
	}
	var profile *models.AdapterProfile
	for _, p := range e.adapters.Profiles() {
		if p.Name == s.Adapter {
			profile = p
			break
		}
	}
	if profile == nil {
		safeLog(log, fmt.Sprintf("策略「%s」绑定的网卡「%s」不存在，跳过路由清理", s.Name, s.Adapter), "warn")
		return
	}
	gw := profile.Gateway()
	if gw == "" {
		safeLog(log, fmt.Sprintf("策略「%s」绑定的网卡「%s」无网关，跳过路由清理", s.Name, s.Adapter), "warn")
		return
	}

	var deleteCmds []string

	if s.Type == models.StrategyOnline {
		for _, dns := range cnDNSServers {
			deleteCmds = append(deleteCmds, fmt.Sprintf("route delete %s mask 255.255.255.255 %s", dns, gw))
		}
		for _, cidr := range e.store.LoadCIDRCache() {
			if !strings.Contains(cidr, "/") {
				continue
			}
			parts := strings.SplitN(cidr, "/", 2)
			ip := parts[0]
			var bits int
			fmt.Sscanf(parts[1], "%d", &bits)
			if bits < 0 || bits > 32 {
				continue
			}
			mask := utils.CIDRToNetmask(bits)
			deleteCmds = append(deleteCmds, fmt.Sprintf("route delete %s mask %s %s", ip, mask, gw))
		}
	} else if s.Type == models.StrategyLocal && s.Source != nil {
		for _, addr := range s.Source.Addresses {
			if strings.Contains(addr, "/") {
				parts := strings.SplitN(addr, "/", 2)
				ip := parts[0]
				var bits int
				fmt.Sscanf(parts[1], "%d", &bits)
				if bits < 0 || bits > 32 {
					continue
				}
				mask := utils.CIDRToNetmask(bits)
				deleteCmds = append(deleteCmds, fmt.Sprintf("route delete %s mask %s %s", ip, mask, gw))
			} else {
				deleteCmds = append(deleteCmds, fmt.Sprintf("route delete %s mask 255.255.255.255 %s", addr, gw))
			}
		}
	}

	if len(deleteCmds) == 0 {
		return
	}

	safeLog(log, fmt.Sprintf("正在清理策略「%s」的路由（%d 条）...", s.Name, len(deleteCmds)), "info")
	deleted := 0
	for i := 0; i < len(deleteCmds); i += CIDRBatchSize {
		end := i + CIDRBatchSize
		if end > len(deleteCmds) {
			end = len(deleteCmds)
		}
		okCount, _, _ := runRouteBatch(deleteCmds[i:end])
		deleted += okCount
		time.Sleep(CIDRBatchDelaySec)
	}
	safeLog(log, fmt.Sprintf("策略「%s」路由清理完成，删除 %d 条", s.Name, deleted), "success")
}

func (e *Engine) SoftReset(rules []*models.SplitRule, log LogFunc, progress func(current, total int, message string)) bool {
	safeLog(log, "正在安全重置网络（仅清理本软件写入的路由）...", "info")
	e.cidrInjectKey = ""
	e.cidrInjectTime = time.Time{}

	emitProgress := func(current, total int, message string) {
		if progress != nil {
			progress(current, total, message)
		}
	}

	emitProgress(0, 1, "正在扫描路由表...")

	e.deleteManagedDefaultRoutes(log)
	e.purgeRoutesOnManagedInterfaces(log)

	var lines []string
	for _, dns := range cnDNSServers {
		lines = append(lines, fmt.Sprintf("route delete %s mask 255.255.255.255", dns))
	}
	for _, cidr := range e.store.LoadCIDRCache() {
		if strings.Contains(cidr, "/") {
			parts := strings.SplitN(cidr, "/", 2)
			ip := parts[0]
			mask := "255.255.255.255"
			if len(parts) > 1 {
				if bits, err := strconv.Atoi(parts[1]); err == nil {
					mask = utils.CIDRToNetmask(bits)
				}
			}
			lines = append(lines, fmt.Sprintf("route delete %s mask %s", ip, mask))
		}
	}
	for _, rule := range rules {
		for _, ip := range rule.IPs {
			if rule.Type == "IPv6" {
				lines = append(lines, fmt.Sprintf("netsh interface ipv6 delete route %s/128", ip))
			} else {
				lines = append(lines, fmt.Sprintf("route delete %s mask 255.255.255.255", ip))
			}
		}
	}

	profiles := e.adapters.Profiles()
	var restoreProfiles []*models.AdapterProfile
	for _, profile := range profiles {
		if profile.Role != models.RoleIgnored {
			restoreProfiles = append(restoreProfiles, profile)
		}
	}

	totalWork := len(lines) + len(restoreProfiles) + 1
	currentWork := 0

	emitProgress(0, totalWork, "正在清理路由表...")

	for i := 0; i < len(lines); i += CIDRBatchSize {
		end := i + CIDRBatchSize
		if end > len(lines) {
			end = len(lines)
		}
		_, _, dead := runRouteBatch(lines[i:end])
		if dead {
			safeLog(log, "清理过程中检测到网卡底层假死（错误 1231），重置可能不完整，建议关闭无线网卡节能模式或运行 netsh winsock reset。", "error")
		}
		currentWork += end - i
		emitProgress(currentWork, totalWork, fmt.Sprintf("正在清理路由 %d/%d...", currentWork, len(lines)))
		time.Sleep(CIDRBatchDelaySec)
	}

	for _, profile := range restoreProfiles {
		idx := profile.IfIndex
		var cmd string
		if idx != 0 {
			cmd = fmt.Sprintf("netsh interface ip set interface interface=%d metric=automatic", idx)
		} else {
			cmd = fmt.Sprintf(`netsh interface ip set interface "%s" metric=automatic`, profile.Name)
		}
		idxStr := "N/A"
		if idx != 0 {
			idxStr = fmt.Sprintf("%d", idx)
		}
		safeLog(log, fmt.Sprintf("[ROUTE] 恢复接口跃点：[%s] index=%s metric=automatic", profile.Name, idxStr), "info")
		out := utils.RunCmd(cmd, 10)
		if strings.Contains(out, "错误") || strings.Contains(out, "Error") {
			safeLog(log, fmt.Sprintf("[ROUTE] 接口跃点恢复失败：[%s] output=%s", profile.Name, strings.TrimSpace(out)), "warn")
		}
		currentWork++
		emitProgress(currentWork, totalWork, fmt.Sprintf("正在恢复接口跃点 [%s]...", profile.Name))
	}

	emitProgress(currentWork, totalWork, "正在刷新网络配置 (ipconfig /renew)...")
	utils.RunCmd("ipconfig /renew", 90)
	currentWork++
	emitProgress(currentWork, totalWork, "重置完成")

	safeLog(log, "系统网络已恢复原生状态，本软件的所有路由已清除。", "success")
	return true
}

func (e *Engine) applyDefaultRoute(profile *models.AdapterProfile, log LogFunc) bool {
	gw := profile.Gateway()
	name := profile.Name
	idx := profile.IfIndex
	ifSuffix := ""
	if idx != 0 {
		ifSuffix = fmt.Sprintf(" IF %d", idx)
	}
	bindNote := ifSuffix
	if bindNote == "" {
		bindNote = "（无IF，索引未探到）"
	}
	utils.RunCmd(fmt.Sprintf("route delete 0.0.0.0 mask 0.0.0.0 %s", gw), 10)
	addCmd := fmt.Sprintf("route add 0.0.0.0 mask 0.0.0.0 %s metric %d%s", gw, DefaultRouteMetric, ifSuffix)
	out := utils.RunCmd(addCmd, 15)
	if !cmdFailed(out) && verifyDefaultRoute(gw) {
		safeLog(log, fmt.Sprintf("默认出口已指向 [%s]（网关 %s，metric %d%s）", name, gw, DefaultRouteMetric, bindNote), "success")
		return true
	}
	changeCmd := fmt.Sprintf("route change 0.0.0.0 mask 0.0.0.0 %s metric %d%s", gw, DefaultRouteMetric, ifSuffix)
	out = utils.RunCmd(changeCmd, 15)
	if !cmdFailed(out) && verifyDefaultRoute(gw) {
		safeLog(log, fmt.Sprintf("默认出口已指向 [%s]（网关 %s，metric %d%s）", name, gw, DefaultRouteMetric, bindNote), "success")
		return true
	}
	if verifyDefaultRoute(gw) {
		safeLog(log, fmt.Sprintf("默认出口路由已在 [%s] 上生效", name), "info")
		return true
	}
	safeLog(log, fmt.Sprintf("默认路由下发失败：%s", strings.TrimSpace(out)), "error")
	return false
}

func verifyDefaultRoute(gateway string) bool {
	routes := utils.RunCmd("route print -4", 15)
	if routes == "" {
		return false
	}
	pattern := fmt.Sprintf(`0\.0\.0\.0\s+0\.0\.0\.0\s+%s`, regexp.QuoteMeta(gateway))
	matched, _ := regexp.MatchString(pattern, routes)
	return matched
}

func routeAlreadyExists(output string) bool {
	text := strings.ToLower(output)
	markers := []string{"对象已存在", "already exists", "already exist"}
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

func cmdFailed(output string) bool {
	if routeAlreadyExists(output) {
		return false
	}
	keywords := []string{"错误", "Error", "无法", "失败", "操作需要提升权限"}
	for _, k := range keywords {
		if strings.Contains(output, k) {
			return true
		}
	}
	return false
}

func getStr(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getInt(m map[string]interface{}, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}
