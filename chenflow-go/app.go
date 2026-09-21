package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"chenflow-go/internal/adapter"
	"chenflow-go/internal/config"
	"chenflow-go/internal/models"
	"chenflow-go/internal/routing"
	"chenflow-go/internal/utils"

	psnet "github.com/shirou/gopsutil/v3/net"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx      context.Context
	store    *config.Store
	adapters *adapter.Manager
	engine   *routing.Engine
	mu       sync.Mutex

	customRules  []*models.SplitRule
	ruleCounter  int
	strategies   []*models.Strategy
	stratCounter int
	logBuffer    []string
	trafficData  map[string]string
	auditData    [][]string
	status       map[string]string
	trafficAdap  string
	isLogging    bool
	monitorRun   bool
	presetAddr   map[string]interface{}
	adapterMon   map[string]interface{}
	cidrCache    []string

	lastRecv      uint64
	lastSent      uint64
	startRecv     uint64
	startSent     uint64
	trafficInited bool

	adapterLastStatus map[string]bool
	hidden            bool
}

const appVersion = "3.3"
const githubRepo = "flecklesreeder-design/NetShunt"

func NewApp() *App {
	store := config.New()
	am := adapter.NewManager(store)
	eng := routing.NewEngine(am, store)

	a := &App{
		store:    store,
		adapters: am,
		engine:   eng,
		trafficData: map[string]string{
			"dl_s": "0.00 B/s", "up_s": "0.00 B/s",
			"dl_t": "0.00 MB", "up_t": "0.00 MB",
		},
		auditData:         [][]string{},
		status:            map[string]string{"text": "引擎就绪", "color": "#10b981"},
		monitorRun:        true,
		presetAddr:        defaultPresetAddresses(),
		adapterMon:        map[string]interface{}{"enabled": false, "monitored_adapters": []string{}},
		adapterLastStatus: make(map[string]bool),
	}

	a.adapters.Refresh()
	a.customRules = a.loadRulesFromStore()
	a.strategies = a.loadStrategiesFromStore()
	a.cidrCache = a.store.LoadCIDRCache()
	a.presetAddr = a.mergePresetAddresses(a.store.LoadPresetAddresses())
	a.adapterMon = a.mergeAdapterMonitor(a.store.LoadAdapterMonitor())
	a.autoBindMACs()
	a.updateRoleStatus()
	go a.applyRulesOnStartup()

	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go a.trafficMonitor()
	go a.auditMonitor()
	go a.cidrUpdateDaemon()
	go a.adapterMonitorDaemon()
	go a.autoCheckUpdate()
	go func() {
		hk := a.store.GetHotkey()
		if hk == "" {
			hk = defaultHotkey
		}
		if mods, vk, ok := parseHotkey(hk); ok {
			setHotkey(mods, vk)
		}
		hotkeyLoop(func() {
			if a.hidden {
				wailsruntime.WindowShow(a.ctx)
				a.hidden = false
			} else {
				wailsruntime.WindowHide(a.ctx)
				a.hidden = true
			}
		})
	}()
	go startTray(
		func() {
			wailsruntime.WindowShow(a.ctx)
			a.hidden = false
		},
		func() {
			stopTray()
			wailsruntime.Quit(a.ctx)
		},
	)
}

func (a *App) log(text, level string) {
	prefix := ">> "
	switch level {
	case "info":
		prefix = "[INFO] "
	case "success":
		prefix = "[OK] "
	case "error":
		prefix = "[ERROR] "
	case "warn":
		prefix = "[WARN] "
	case "guard":
		prefix = "[守护] "
	}
	stamp := time.Now().Format("15:04:05")
	a.mu.Lock()
	a.logBuffer = append(a.logBuffer, fmt.Sprintf("[%s] %s%s", stamp, prefix, text))
	if len(a.logBuffer) > 500 {
		a.logBuffer = a.logBuffer[len(a.logBuffer)-500:]
	}
	a.mu.Unlock()
}

func (a *App) logFn() routing.LogFunc {
	return func(msg, level string) { a.log(msg, level) }
}

func (a *App) updateRoleStatus() {
	profiles := a.adapters.AllProfilesSorted()
	lang := a.store.GetLanguage()
	notAssigned := "没指派"
	defaultExit := "默认出口"
	noAdapter := "未检测到网卡"
	if lang == "en" {
		notAssigned = "Not assigned"
		defaultExit = "Default Exit"
		noAdapter = "No adapter detected"
	}
	var parts []string
	anyBound := false
	for _, p := range profiles {
		stratName := notAssigned
		for _, s := range a.strategies {
			if s.IsFallback() {
				continue
			}
			if s.Adapter == p.Name {
				stratName = s.Name
				anyBound = true
				break
			}
		}
		if p.Name == a.store.GetDefaultExitAdapter() {
			if stratName == notAssigned {
				stratName = defaultExit
			} else {
				stratName += " +" + defaultExit
			}
			anyBound = true
		}
		parts = append(parts, fmt.Sprintf("%s → %s", p.Name, stratName))
	}
	text := strings.Join(parts, "  |  ")
	if text == "" {
		text = noAdapter
	}
	color := "#10b981"
	if !anyBound {
		color = "#f59e0b"
	}
	a.status = map[string]string{
		"text":  text,
		"color": color,
	}
}

func (a *App) autoBindMACs() {
	if len(a.store.GetMACs()) > 0 {
		return
	}
	ready := a.adapters.ReadyForRecommend()
	if len(ready) == 0 {
		return
	}
	a.store.SetRole(ready[0].Name, string(models.RoleDefaultExit), ready[0].MAC)
	if len(ready) >= 2 {
		a.store.SetRole(ready[1].Name, string(models.RoleCNSplit), ready[1].MAC)
	}
	a.adapters.SyncRoles()
}

func (a *App) autoCheckUpdate() {
	time.Sleep(3 * time.Second)
	resp, err := http.Get(fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var rel struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return
	}
	latestVer := strings.TrimPrefix(rel.TagName, "v")
	if compareVersion(latestVer, appVersion) <= 0 {
		return
	}
	var dlURL string
	for _, asset := range rel.Assets {
		if strings.Contains(asset.Name, "Setup") && strings.HasSuffix(asset.Name, ".exe") {
			dlURL = asset.BrowserDownloadURL
			break
		}
	}
	wailsruntime.EventsEmit(a.ctx, "update:auto_notify", map[string]interface{}{
		"current":     appVersion,
		"latest":      latestVer,
		"downloadUrl": dlURL,
		"notes":       rel.Body,
	})
}

func (a *App) applyRulesOnStartup() {
	if len(a.customRules) == 0 {
		return
	}
	a.adapters.Refresh()
	if len(a.adapters.ReadyAdapters()) == 0 {
		return
	}
	a.engine.ApplyAllRules(a.customRules, nil, nil)
}

func (a *App) loadRulesFromStore() []*models.SplitRule {
	raw := a.store.LoadRules()
	var rules []*models.SplitRule
	maxID := 0
	for _, r := range raw {
		rule := &models.SplitRule{}
		if v, ok := r["id"].(float64); ok {
			rule.ID = int(v)
		}
		if v, ok := r["type"].(string); ok {
			rule.Type = v
		}
		if v, ok := r["target"].(string); ok {
			rule.Target = v
		}
		if ips, ok := r["ips"].([]interface{}); ok {
			for _, ip := range ips {
				if s, ok := ip.(string); ok {
					rule.IPs = append(rule.IPs, s)
				}
			}
		}
		if v, ok := r["adapter"].(string); ok {
			rule.Adapter = v
		}
		if v, ok := r["status"].(string); ok {
			rule.Status = v
		}
		if rule.ID > maxID {
			maxID = rule.ID
		}
		rules = append(rules, rule)
	}
	a.ruleCounter = maxID
	return rules
}

func (a *App) mergePresetAddresses(loaded map[string]interface{}) map[string]interface{} {
	if len(loaded) == 0 {
		return defaultPresetAddresses()
	}
	return loaded
}

func (a *App) mergeAdapterMonitor(loaded map[string]interface{}) map[string]interface{} {
	if len(loaded) == 0 {
		return map[string]interface{}{"enabled": false, "monitored_adapters": []string{}}
	}
	return loaded
}

func defaultPresetAddresses() map[string]interface{} {
	return map[string]interface{}{
		"国内": map[string]interface{}{
			"builtin": []string{"baidu.com", "qq.com", "taobao.com", "jd.com", "163.com", "sina.com.cn",
				"zhihu.com", "bilibili.com", "douyin.com", "weibo.com",
				"114.114.114.114", "223.5.5.5", "119.29.29.29", "180.76.76.76",
				"101.226.4.6", "123.125.81.6"},
			"user": []string{},
		},
		"国外": map[string]interface{}{
			"builtin": []string{"google.com", "youtube.com", "facebook.com", "twitter.com", "instagram.com",
				"github.com", "reddit.com", "wikipedia.org", "amazon.com", "netflix.com",
				"8.8.8.8", "1.1.1.1", "8.8.4.4", "208.67.222.222"},
			"user": []string{},
		},
		"本地": map[string]interface{}{
			"builtin": []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
				"127.0.0.0/8", "169.254.0.0/16"},
			"user": []string{},
		},
	}
}

func (a *App) ApiCall(method string, params map[string]interface{}) map[string]interface{} {
	switch method {

	case "close_window":
		stopTray()
		wailsruntime.Quit(a.ctx)
		return map[string]interface{}{"ok": true}
	case "hide_window":
		wailsruntime.WindowHide(a.ctx)
		return map[string]interface{}{"ok": true}
	case "show_window":
		wailsruntime.WindowShow(a.ctx)
		a.hidden = false
		return map[string]interface{}{"ok": true}
	case "hide_to_tray":
		wailsruntime.WindowHide(a.ctx)
		a.hidden = true
		return map[string]interface{}{"ok": true}
	case "get_close_action":
		return map[string]interface{}{"action": a.store.GetCloseAction()}
	case "set_close_action":
		action, _ := params["action"].(string)
		a.store.SetCloseAction(action)
		return map[string]interface{}{"ok": true}
	case "minimise_window":
		wailsruntime.WindowMinimise(a.ctx)
		return map[string]interface{}{"ok": true}
	case "get_strategies":

		list := make([]interface{}, len(a.strategies))
		for i, s := range a.strategies {
			list[i] = s
		}
		return map[string]interface{}{"strategies": list}
	case "sync_strategy_url":
		return a.handleSyncStrategyURL(params)
	case "create_strategy":
		return a.handleCreateStrategy(params)
	case "update_strategy":
		return a.handleUpdateStrategy(params)
	case "delete_strategy":
		return a.handleDeleteStrategy(params)
	case "apply_strategies":
		go a.handleApplyStrategies()
		return map[string]interface{}{"ok": true, "async": true}
	case "get_theme":
		return map[string]interface{}{"theme": a.store.GetTheme()}
	case "set_theme":
		theme := "dark"
		if v, ok := params["theme"].(string); ok {
			theme = v
		}
		a.store.SetTheme(theme)
		return map[string]interface{}{"ok": true}
	case "get_language":
		return map[string]interface{}{"language": a.store.GetLanguage()}
	case "set_language":
		lang := "zh"
		if v, ok := params["language"].(string); ok {
			lang = v
		}
		a.store.SetLanguage(lang)
		a.updateRoleStatus()
		return map[string]interface{}{"ok": true}
	case "check_update":
		go func() {
			resp, err := http.Get(fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo))
			if err != nil {
				wailsruntime.EventsEmit(a.ctx, "update:result", map[string]interface{}{"available": false, "error": err.Error()})
				return
			}
			defer resp.Body.Close()
			var rel struct {
				TagName string `json:"tag_name"`
				Body    string `json:"body"`
				Assets  []struct {
					Name               string `json:"name"`
					BrowserDownloadURL string `json:"browser_download_url"`
				} `json:"assets"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
				wailsruntime.EventsEmit(a.ctx, "update:result", map[string]interface{}{"available": false, "error": err.Error()})
				return
			}
			latestVer := strings.TrimPrefix(rel.TagName, "v")
			needUpdate := compareVersion(latestVer, appVersion) > 0
			var dlURL string
			for _, asset := range rel.Assets {
				if strings.Contains(asset.Name, "Setup") && strings.HasSuffix(asset.Name, ".exe") {
					dlURL = asset.BrowserDownloadURL
					break
				}
			}
			wailsruntime.EventsEmit(a.ctx, "update:result", map[string]interface{}{
				"available":   needUpdate,
				"current":     appVersion,
				"latest":      latestVer,
				"downloadUrl": dlURL,
				"notes":       rel.Body,
			})
		}()
		return map[string]interface{}{"ok": true}
	case "perform_update":
		url, _ := params["url"].(string)
		if url == "" {
			return map[string]interface{}{"ok": false, "error": "no url"}
		}
		go func() {
			wailsruntime.EventsEmit(a.ctx, "update:progress", map[string]interface{}{"stage": "downloading", "percent": 0})
			resp, err := http.Get(url)
			if err != nil {
				wailsruntime.EventsEmit(a.ctx, "update:error", err.Error())
				return
			}
			defer resp.Body.Close()
			tmpFile := filepath.Join(os.TempDir(), "NetShunt_Update.exe")
			f, err := os.Create(tmpFile)
			if err != nil {
				wailsruntime.EventsEmit(a.ctx, "update:error", err.Error())
				return
			}
			total := resp.ContentLength
			written := int64(0)
			buf := make([]byte, 32*1024)
			for {
				n, rerr := resp.Body.Read(buf)
				if n > 0 {
					f.Write(buf[:n])
					written += int64(n)
					if total > 0 {
						wailsruntime.EventsEmit(a.ctx, "update:progress", map[string]interface{}{"stage": "downloading", "percent": int(written * 100 / total)})
					}
				}
				if rerr == io.EOF {
					break
				}
				if rerr != nil {
					f.Close()
					wailsruntime.EventsEmit(a.ctx, "update:error", rerr.Error())
					return
				}
			}
			f.Close()
			wailsruntime.EventsEmit(a.ctx, "update:progress", map[string]interface{}{"stage": "installing", "percent": 100})
			cmd := exec.Command(tmpFile, "/VERYSILENT", "/SP-", "/NORESTART", "/NOCANCEL", "/CLOSEAPPLICATIONS")
			utils.HideWindow(cmd)
			cmd.Start()
			os.Exit(0)
		}()
		return map[string]interface{}{"ok": true}
	case "get_hotkey":
		hk := a.store.GetHotkey()
		if hk == "" {
			hk = defaultHotkey
		}
		return map[string]interface{}{"hotkey": hk}
	case "set_hotkey":
		hk := getString(params, "hotkey")
		mods, vk, ok := parseHotkey(hk)
		if !ok {
			return map[string]interface{}{"ok": false, "error": "无效的快捷键格式"}
		}
		if !setHotkey(mods, vk) {
			return map[string]interface{}{"ok": false, "error": "快捷键注册失败，可能被其他程序占用"}
		}
		a.store.SetHotkey(hk)
		a.log(fmt.Sprintf("全局快捷键已设为 %s", hotkeyToString(mods, vk)), "info")
		return map[string]interface{}{"ok": true, "hotkey": hotkeyToString(mods, vk)}
	case "get_status":
		return map[string]interface{}{"text": a.status["text"], "color": a.status["color"]}
	case "get_log":
		a.mu.Lock()
		log := strings.Join(a.logBuffer, "\n")
		a.mu.Unlock()
		return map[string]interface{}{"log": log}
	case "apply_roles":
		go func() {
			a.adapters.Refresh()
			a.handleApplyStrategies()
			a.updateRoleStatus()
		}()
		return map[string]interface{}{"ok": true}
	case "toggle_guard":
		enabled, _ := params["enabled"].(bool)
		if enabled {
			a.engine.StartGuard(a.logFn())
			a.log("自愈守护已开启。", "info")
		} else {
			a.engine.StopGuard()
			a.log("自愈守护已关闭。", "info")
		}
		return map[string]interface{}{"ok": true}
	case "get_adapter_names":
		a.adapters.RefreshLight()
		var names []string
		for _, n := range a.adapters.Names() {
			if !strings.Contains(n, "Loopback") {
				names = append(names, n)
			}
		}
		return map[string]interface{}{"names": names}
	case "select_traffic_adapter":
		if v, ok := params["name"].(string); ok {
			a.trafficAdap = v
			a.trafficInited = false
			a.trafficData = map[string]string{
				"dl_s": "0.00 B/s", "up_s": "0.00 B/s",
				"dl_t": "0.00 B", "up_t": "0.00 B",
			}
		}
		return map[string]interface{}{"ok": true}
	case "get_traffic":
		return map[string]interface{}{
			"dl_s": a.trafficData["dl_s"], "up_s": a.trafficData["up_s"],
			"dl_t": a.trafficData["dl_t"], "up_t": a.trafficData["up_t"],
		}
	case "get_audit":
		return map[string]interface{}{"data": a.auditData}
	case "toggle_logging":
		a.isLogging, _ = params["enabled"].(bool)
		return map[string]interface{}{"ok": true}
	case "ping":
		target, _ := params["target"].(string)
		if !utils.SanitizeForCmd(target) {
			return map[string]interface{}{"result": "非法输入"}
		}
		go func() {
			utils.RunCmdStreaming(
				fmt.Sprintf("ping -n 4 %s", target), 30,
				func(line string) { wailsruntime.EventsEmit(a.ctx, "diag:line", line) },
				func(err error) {
					if err != nil {
						wailsruntime.EventsEmit(a.ctx, "diag:line", fmt.Sprintf("[错误] %v", err))
					}
					wailsruntime.EventsEmit(a.ctx, "diag:done", "")
				},
			)
		}()
		return map[string]interface{}{"ok": true}
	case "tracert":
		target, _ := params["target"].(string)
		if !utils.SanitizeForCmd(target) {
			return map[string]interface{}{"result": "非法输入"}
		}
		go func() {
			utils.RunCmdStreaming(
				fmt.Sprintf("tracert -d -h 15 -w 1 %s", target), 60,
				func(line string) { wailsruntime.EventsEmit(a.ctx, "diag:line", line) },
				func(err error) {
					if err != nil {
						wailsruntime.EventsEmit(a.ctx, "diag:line", fmt.Sprintf("[错误] %v", err))
					}
					wailsruntime.EventsEmit(a.ctx, "diag:done", "")
				},
			)
		}()
		return map[string]interface{}{"ok": true}
	case "flush_dns":
		return map[string]interface{}{"result": utils.RunCmd("ipconfig /flushdns", 10)}
	case "nslookup":
		target, _ := params["target"].(string)
		if !utils.SanitizeForCmd(target) {
			return map[string]interface{}{"result": "非法输入"}
		}
		go func() {
			utils.RunCmdStreaming(
				fmt.Sprintf("nslookup %s", target), 15,
				func(line string) { wailsruntime.EventsEmit(a.ctx, "diag:line", line) },
				func(err error) {
					if err != nil {
						wailsruntime.EventsEmit(a.ctx, "diag:line", fmt.Sprintf("[错误] %v", err))
					}
					wailsruntime.EventsEmit(a.ctx, "diag:done", "")
				},
			)
		}()
		return map[string]interface{}{"ok": true}
	case "netstat":
		go func() {
			utils.RunCmdStreaming(
				"netstat -ano", 15,
				func(line string) { wailsruntime.EventsEmit(a.ctx, "diag:line", line) },
				func(err error) {
					if err != nil {
						wailsruntime.EventsEmit(a.ctx, "diag:line", fmt.Sprintf("[错误] %v", err))
					}
					wailsruntime.EventsEmit(a.ctx, "diag:done", "")
				},
			)
		}()
		return map[string]interface{}{"ok": true}
	case "route_print":
		go func() {
			utils.RunCmdStreaming(
				"route print -4", 15,
				func(line string) { wailsruntime.EventsEmit(a.ctx, "diag:line", line) },
				func(err error) {
					if err != nil {
						wailsruntime.EventsEmit(a.ctx, "diag:line", fmt.Sprintf("[错误] %v", err))
					}
					wailsruntime.EventsEmit(a.ctx, "diag:done", "")
				},
			)
		}()
		return map[string]interface{}{"ok": true}
	case "arp_table":
		go func() {
			utils.RunCmdStreaming(
				"arp -a", 10,
				func(line string) { wailsruntime.EventsEmit(a.ctx, "diag:line", line) },
				func(err error) {
					if err != nil {
						wailsruntime.EventsEmit(a.ctx, "diag:line", fmt.Sprintf("[错误] %v", err))
					}
					wailsruntime.EventsEmit(a.ctx, "diag:done", "")
				},
			)
		}()
		return map[string]interface{}{"ok": true}
	case "ipconfig_all":
		go func() {
			utils.RunCmdStreaming(
				"ipconfig /all", 10,
				func(line string) { wailsruntime.EventsEmit(a.ctx, "diag:line", line) },
				func(err error) {
					if err != nil {
						wailsruntime.EventsEmit(a.ctx, "diag:line", fmt.Sprintf("[错误] %v", err))
					}
					wailsruntime.EventsEmit(a.ctx, "diag:done", "")
				},
			)
		}()
		return map[string]interface{}{"ok": true}
	case "port_test":
		target, _ := params["target"].(string)
		port, _ := params["port"].(string)
		if !utils.SanitizeForCmd(target) || !utils.SanitizeForCmd(port) {
			return map[string]interface{}{"result": "非法输入"}
		}
		go func() {
			utils.RunPSStreaming(
				fmt.Sprintf("$ProgressPreference='SilentlyContinue'; Test-NetConnection -ComputerName %s -Port %s -InformationLevel Detailed | Out-String -Stream", target, port), 30,
				func(line string) { wailsruntime.EventsEmit(a.ctx, "diag:line", line) },
				func(err error) {
					if err != nil {
						wailsruntime.EventsEmit(a.ctx, "diag:line", fmt.Sprintf("[错误] %v", err))
					}
					wailsruntime.EventsEmit(a.ctx, "diag:done", "")
				},
			)
		}()
		return map[string]interface{}{"ok": true}
	case "speed_test":
		go func() {
			wailsruntime.EventsEmit(a.ctx, "diag:line", "正在下载测速文件 (10MB)...")
			start := time.Now()
			resp, err := http.Get("https://speed.cloudflare.com/__down?bytes=10000000")
			if err != nil {
				wailsruntime.EventsEmit(a.ctx, "diag:line", fmt.Sprintf("[错误] %v", err))
				wailsruntime.EventsEmit(a.ctx, "diag:done", "")
				return
			}
			defer resp.Body.Close()
			n, _ := io.Copy(io.Discard, resp.Body)
			elapsed := time.Since(start).Seconds()
			if elapsed > 0 && n > 0 {
				speed := utils.FormatBytes(float64(n) / elapsed)
				wailsruntime.EventsEmit(a.ctx, "diag:line", fmt.Sprintf("下载完成: %s / %.1f 秒", utils.FormatBytes(float64(n)), elapsed))
				wailsruntime.EventsEmit(a.ctx, "diag:line", fmt.Sprintf("平均下载速度: %s/s", speed))
			} else {
				wailsruntime.EventsEmit(a.ctx, "diag:line", "测速失败")
			}
			wailsruntime.EventsEmit(a.ctx, "diag:done", "")
		}()
		return map[string]interface{}{"ok": true}
	case "get_adapter_profiles":
		a.adapters.RefreshLight()
		profiles := a.adapters.AllProfilesSorted()
		var result []map[string]interface{}
		for _, p := range profiles {
			strategyName := ""
			strategyID := -1
			for _, s := range a.strategies {
				if s.IsFallback() {
					continue
				}
				if s.Adapter == p.Name {
					strategyName = s.Name
					strategyID = s.ID
					break
				}
			}
			result = append(result, map[string]interface{}{
				"name":            p.Name,
				"ip":              p.IP,
				"if_index":        p.IfIndex,
				"is_up":           p.IsUp,
				"role":            string(p.Role),
				"gateway":         p.Gateway(),
				"gateway_auto":    p.GatewayAuto,
				"gateway_manual":  p.GatewayManual,
				"gateway_mode":    p.GatewayMode,
				"strategy_name":   strategyName,
				"strategy_id":     strategyID,
				"is_default_exit": p.Name == a.store.GetDefaultExitAdapter(),
			})
		}
		if len(result) == 0 {
			result = []map[string]interface{}{}
		}
		return map[string]interface{}{"profiles": result}
	case "bind_strategy_to_adapter":
		adapterName, _ := params["adapter_name"].(string)
		strategyID := getInt(params, "strategy_id")
		if adapterName == "" {
			return map[string]interface{}{"ok": false, "error": "网卡名不能为空"}
		}
		if strategyID == models.FallbackID {
			return map[string]interface{}{"ok": false, "error": "兜底策略请用默认出口勾选绑定"}
		}
		a.mu.Lock()
		for _, s := range a.strategies {
			if s.Adapter == adapterName && !s.IsFallback() {
				s.Adapter = ""
			}
		}
		if strategyID >= 0 {
			for _, s := range a.strategies {
				if s.ID == strategyID {
					s.Adapter = adapterName
					s.UpdatedAt = time.Now().Unix()
					role := models.RoleCustomOnly
					if s.Type == models.StrategyOnline {
						role = models.RoleCNSplit
					}
					profile := a.adapters.GetProfile(adapterName)
					mac := ""
					if profile != nil {
						mac = profile.MAC
						profile.Role = role
					}
					a.store.SetRole(adapterName, string(role), mac)
					break
				}
			}
		} else {
			profile := a.adapters.GetProfile(adapterName)
			mac := ""
			if profile != nil {
				mac = profile.MAC
				profile.Role = models.RoleIgnored
			}
			a.store.SetRole(adapterName, string(models.RoleIgnored), mac)
		}
		a.mu.Unlock()
		a.saveStrategies()
		a.updateRoleStatus()
		if strategyID >= 0 {
			a.log(fmt.Sprintf("网卡 [%s] 已绑定策略。", adapterName), "info")
		} else {
			a.log(fmt.Sprintf("网卡 [%s] 已解除策略绑定。", adapterName), "info")
		}
		return map[string]interface{}{"ok": true}
	case "set_default_exit":
		adapterName, _ := params["adapter_name"].(string)
		enabled, _ := params["enabled"].(bool)
		if enabled {
			a.store.SetDefaultExitAdapter(adapterName)
			for _, s := range a.strategies {
				if s.IsFallback() {
					s.Adapter = adapterName
					s.UpdatedAt = time.Now().Unix()
				}
			}
			a.log(fmt.Sprintf("网卡 [%s] 已设为默认出口。", adapterName), "info")
		} else {
			if a.store.GetDefaultExitAdapter() == adapterName {
				a.store.SetDefaultExitAdapter("")
			}
			for _, s := range a.strategies {
				if s.IsFallback() && s.Adapter == adapterName {
					s.Adapter = ""
					s.UpdatedAt = time.Now().Unix()
				}
			}
			a.log(fmt.Sprintf("网卡 [%s] 已取消默认出口。", adapterName), "info")
		}
		a.saveStrategies()
		a.updateRoleStatus()
		return map[string]interface{}{"ok": true}
	case "set_adapter_role":
		name, _ := params["name"].(string)
		label, _ := params["label"].(string)
		roleVal := string(models.RoleIgnored)
		if v, ok := models.LabelToRole[label]; ok {
			roleVal = string(v)
		}
		profile := a.adapters.GetProfile(name)
		mac := ""
		if profile != nil {
			mac = profile.MAC
		}
		a.store.SetRole(name, roleVal, mac)
		if profile != nil {
			profile.Role = models.AdapterRole(roleVal)
		}
		a.updateRoleStatus()
		a.log(fmt.Sprintf("网卡 [%s] 角色已设为「%s」。", name, label), "info")
		return map[string]interface{}{"ok": true}
	case "set_gateway_mode":
		name, _ := params["name"].(string)
		modeLabel, _ := params["mode"].(string)
		mode := "auto"
		if modeLabel == "手动" {
			mode = "manual"
		}
		if mode == "auto" {
			profile := a.adapters.GetProfile(name)
			if profile != nil {
				profile.GatewayMode = "auto"
				profile.GatewayManual = ""
			}
			a.store.SetGatewayOverride(name, "auto", "")
		}
		return map[string]interface{}{"ok": true}
	case "set_gateway_value":
		name, _ := params["name"].(string)
		value := strings.TrimSpace(getString(params, "value"))
		if value == "" {
			return map[string]interface{}{"ok": true}
		}
		profile := a.adapters.GetProfile(name)
		if profile != nil {
			profile.GatewayMode = "manual"
			profile.GatewayManual = value
		}
		a.store.SetGatewayOverride(name, "manual", value)
		a.log(fmt.Sprintf("网卡 [%s] 网关已设为手动：%s。", name, value), "success")
		return map[string]interface{}{"ok": true}
	case "recommend_roles":
		a.adapters.RefreshLight()
		ready := a.adapters.ReadyForRecommend()
		if len(ready) == 0 {
			return map[string]interface{}{"ok": false}
		}
		a.mu.Lock()
		for _, s := range a.strategies {
			s.Adapter = ""
		}
		for _, name := range a.adapters.Names() {
			a.store.SetRole(name, string(models.RoleIgnored), "")
		}
		var fallback *models.Strategy
		var online *models.Strategy
		for _, s := range a.strategies {
			if s.Type == models.StrategyFallback && fallback == nil {
				fallback = s
			}
			if s.Type == models.StrategyOnline && online == nil {
				online = s
			}
		}
		if online == nil {
			online = &models.Strategy{
				ID:        a.stratCounter,
				Name:      "国内分流（推荐）",
				Type:      models.StrategyOnline,
				Enabled:   true,
				CreatedAt: time.Now().Unix(),
				Source: &models.AddressSource{
					Mode:           "online",
					URL:            "https://raw.githubusercontent.com/misakaio/chnroutes2/master/chnroutes.txt",
					UpdateInterval: 24,
					UpdateUnit:     "hours",
					SyncStatus:     models.SyncIdle,
				},
			}
			a.stratCounter++
			a.strategies = append(a.strategies, online)
		}
		p0 := a.adapters.GetProfile(ready[0].Name)
		mac0 := ""
		if p0 != nil {
			mac0 = p0.MAC
			p0.Role = models.RoleDefaultExit
		}
		a.store.SetRole(ready[0].Name, string(models.RoleDefaultExit), mac0)
		if fallback != nil {
			fallback.Adapter = ready[0].Name
		}
		if len(ready) > 1 {
			p1 := a.adapters.GetProfile(ready[1].Name)
			mac1 := ""
			if p1 != nil {
				mac1 = p1.MAC
				p1.Role = models.RoleCNSplit
			}
			a.store.SetRole(ready[1].Name, string(models.RoleCNSplit), mac1)
			online.Adapter = ready[1].Name
		}
		a.mu.Unlock()
		a.saveStrategies()
		a.adapters.SyncRoles()
		a.updateRoleStatus()
		a.log("已应用推荐预设（策略路由模式）。", "success")
		return map[string]interface{}{"ok": true}
	case "get_preset_addresses":
		result := a.presetAddr
		result["cidr_count"] = len(a.cidrCache)
		preview := a.cidrCache
		if len(preview) > 50 {
			preview = preview[:50]
		}
		result["cidr_preview"] = preview
		return result
	case "add_preset_address":
		addr, _ := params["addr"].(string)
		cat, _ := params["cat"].(string)
		if catData, ok := a.presetAddr[cat].(map[string]interface{}); ok {
			if userList, ok := catData["user"].([]string); ok {
				catData["user"] = append(userList, addr)
			} else {
				catData["user"] = []string{addr}
			}
		} else {
			a.presetAddr[cat] = map[string]interface{}{"builtin": []string{}, "user": []string{addr}}
		}
		a.store.SavePresetAddresses(a.presetAddr)
		return map[string]interface{}{"ok": true}
	case "delete_preset_address":
		cat, _ := params["cat"].(string)
		idxF, _ := params["idx"].(float64)
		idx := int(idxF)
		if catData, ok := a.presetAddr[cat].(map[string]interface{}); ok {
			if userList, ok := catData["user"].([]string); ok {
				if idx >= 0 && idx < len(userList) {
					catData["user"] = append(userList[:idx], userList[idx+1:]...)
				}
			}
		}
		a.store.SavePresetAddresses(a.presetAddr)
		return map[string]interface{}{"ok": true}
	case "get_adapter_monitor_config":
		a.adapters.Refresh()
		enabled, _ := a.adapterMon["enabled"].(bool)
		var monitored []string
		switch v := a.adapterMon["monitored_adapters"].(type) {
		case []string:
			monitored = v
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok {
					monitored = append(monitored, s)
				}
			}
		}
		return map[string]interface{}{
			"enabled":   enabled,
			"monitored": monitored,
			"adapters":  a.adapters.Names(),
		}
	case "toggle_adapter_monitor":
		a.adapterMon["enabled"], _ = params["enabled"].(bool)
		a.store.SaveAdapterMonitor(a.adapterMon)
		return map[string]interface{}{"ok": true}
	case "save_adapter_monitor":
		a.adapterMon["enabled"], _ = params["enabled"].(bool)
		if monitored, ok := params["monitored"].([]interface{}); ok {
			var names []string
			for _, m := range monitored {
				if s, ok := m.(string); ok {
					names = append(names, s)
				}
			}
			a.adapterMon["monitored_adapters"] = names
		}
		a.store.SaveAdapterMonitor(a.adapterMon)
		return map[string]interface{}{"ok": true}
	case "emergency_reset":
		a.engine.StopGuard()
		rulesSnapshot := make([]*models.SplitRule, len(a.customRules))
		copy(rulesSnapshot, a.customRules)
		go func() {
			progressFn := func(current, total int, message string) {
				if a.ctx != nil {
					wailsruntime.EventsEmit(a.ctx, "reset:progress", map[string]interface{}{
						"current": current,
						"total":   total,
						"message": message,
					})
				}
			}
			a.engine.SoftReset(rulesSnapshot, a.logFn(), progressFn)
			if a.ctx != nil {
				wailsruntime.EventsEmit(a.ctx, "reset:done", map[string]interface{}{"ok": true})
			}
		}()
		a.customRules = []*models.SplitRule{}
		a.store.SaveRules(a.customRules)
		return map[string]interface{}{"ok": true, "async": true}
	case "get_rules":
		var result []map[string]interface{}
		for _, r := range a.customRules {
			result = append(result, map[string]interface{}{
				"id":      r.ID,
				"type":    r.Type,
				"target":  r.Target,
				"ips":     r.IPs,
				"adapter": r.Adapter,
				"status":  r.Status,
			})
		}
		if len(result) == 0 {
			result = []map[string]interface{}{}
		}
		return map[string]interface{}{"rules": result}
	case "add_rule":
		rType, _ := params["type"].(string)
		target, _ := params["target"].(string)
		adapterName, _ := params["adapter"].(string)
		var resolvedIPs []string
		if rType == "URL" {
			looked, err := net.LookupIP(target)
			if err == nil {
				seen := make(map[string]bool)
				for _, ip := range looked {
					if v4 := ip.To4(); v4 != nil {
						s := v4.String()
						if !seen[s] {
							seen[s] = true
							resolvedIPs = append(resolvedIPs, s)
						}
					}
				}
			}
		} else {
			resolvedIPs = []string{target}
		}
		a.ruleCounter++
		a.customRules = append(a.customRules, &models.SplitRule{
			ID: a.ruleCounter, Type: rType, Target: target,
			IPs: resolvedIPs, Adapter: adapterName, Status: "Pending",
		})
		a.store.SaveRules(a.customRules)
		return map[string]interface{}{"ok": true}
	case "delete_rule":
		idF, _ := params["id"].(float64)
		ruleID := int(idF)
		var toDelete *models.SplitRule
		var filtered []*models.SplitRule
		for _, r := range a.customRules {
			if r.ID == ruleID {
				toDelete = r
			} else {
				filtered = append(filtered, r)
			}
		}
		a.customRules = filtered
		a.store.SaveRules(a.customRules)
		if toDelete != nil {
			go a.engine.RemoveRuleRoutes(toDelete)
		}
		return map[string]interface{}{"ok": true}
	case "apply_all_rules":
		go func() {
			a.adapters.Refresh()
			if len(a.adapters.ReadyAdapters()) == 0 {
				a.log("无可用网卡（需在线且具备网关），无法应用规则。", "error")
				if a.ctx != nil {
					wailsruntime.EventsEmit(a.ctx, "apply_rules:done", map[string]interface{}{
						"ok": false, "error": "无可用网卡",
					})
				}
				return
			}
			progressFn := func(current, total int, message string) {
				if a.ctx != nil {
					wailsruntime.EventsEmit(a.ctx, "apply_rules:progress", map[string]interface{}{
						"current": current,
						"total":   total,
						"message": message,
					})
				}
			}
			a.engine.ApplyAllRules(a.customRules, func(applied, failed int, _ []string) {
				if applied > 0 {
					a.log(fmt.Sprintf("成功执行了 %d 条策略。", applied), "success")
				}
				if failed > 0 {
					a.log(fmt.Sprintf("%d 条规则失败。", failed), "warn")
				}
				if a.ctx != nil {
					wailsruntime.EventsEmit(a.ctx, "apply_rules:done", map[string]interface{}{
						"ok": true, "applied": applied, "failed": failed,
					})
				}
			}, progressFn)
		}()
		return map[string]interface{}{"ok": true, "async": true}
	default:
		return map[string]interface{}{"error": fmt.Sprintf("未知方法: %s", method)}
	}
}

func (a *App) trafficMonitor() {
	var lastTime time.Time
	for a.monitorRun {
		time.Sleep(time.Second)
		if a.trafficAdap == "" {
			continue
		}
		var recv, sent uint64
		stats, err := psnet.IOCounters(true)
		if err == nil {
			for _, s := range stats {
				if s.Name == a.trafficAdap {
					recv = s.BytesRecv
					sent = s.BytesSent
					break
				}
			}
		}
		if recv == 0 && sent == 0 {
			continue
		}
		now := time.Now()
		if !a.trafficInited {
			a.startRecv = recv
			a.startSent = sent
			a.lastRecv = recv
			a.lastSent = sent
			lastTime = now
			a.trafficInited = true
			continue
		}
		var dlDelta, upDelta uint64
		if recv >= a.lastRecv {
			dlDelta = recv - a.lastRecv
		} else {
			a.lastRecv = recv
			a.lastSent = sent
			lastTime = now
			continue
		}
		if sent >= a.lastSent {
			upDelta = sent - a.lastSent
		} else {
			a.lastRecv = recv
			a.lastSent = sent
			lastTime = now
			continue
		}
		elapsed := now.Sub(lastTime).Seconds()
		if elapsed <= 0 {
			elapsed = 1
		}
		dlSpeed := utils.FormatBytes(float64(dlDelta) / elapsed)
		upSpeed := utils.FormatBytes(float64(upDelta) / elapsed)
		var dlTotalVal, upTotalVal uint64
		if recv >= a.startRecv {
			dlTotalVal = recv - a.startRecv
		}
		if sent >= a.startSent {
			upTotalVal = sent - a.startSent
		}
		dlTotal := utils.FormatBytes(float64(dlTotalVal))
		upTotal := utils.FormatBytes(float64(upTotalVal))
		a.trafficData = map[string]string{
			"dl_s": dlSpeed + "/s", "up_s": upSpeed + "/s",
			"dl_t": dlTotal, "up_t": upTotal,
		}
		a.lastRecv = recv
		a.lastSent = sent
		lastTime = now
	}
}

func (a *App) auditMonitor() {
	for a.monitorRun {
		time.Sleep(5 * time.Second)
		out := utils.RunCmd("netstat -ano", 10)
		if out == "" {
			continue
		}
		ipToName := make(map[string]string)
		for _, p := range a.adapters.Profiles() {
			if p.IP != "" {
				ipToName[p.IP] = p.Name
			}
		}
		type connEntry struct {
			row []string
			pid int
		}
		var entries []connEntry
		pidSet := make(map[int]bool)
		stamp := time.Now().Format("2006-01-02 15:04:05")
		for _, line := range strings.Split(out, "\n") {
			parts := strings.Fields(line)
			if len(parts) < 5 || parts[0] != "TCP" || parts[3] != "ESTABLISHED" {
				continue
			}
			localAddr := parts[1]
			remoteAddr := parts[2]
			var pid int
			fmt.Sscanf(parts[4], "%d", &pid)
			localIP := localAddr
			if idx := strings.LastIndex(localAddr, ":"); idx > 0 {
				localIP = localAddr[:idx]
			}
			if localIP == "127.0.0.1" || localIP == "[::1]" {
				continue
			}
			remoteIP := remoteAddr
			remotePort := ""
			if idx := strings.LastIndex(remoteAddr, ":"); idx > 0 {
				remoteIP = remoteAddr[:idx]
				remotePort = remoteAddr[idx+1:]
			}
			adapter := ipToName[localIP]
			if adapter == "" {
				adapter = "未知"
			}
			entries = append(entries, connEntry{
				row: []string{stamp, adapter, "", remoteIP, remotePort, "活跃"},
				pid: pid,
			})
			if pid != 0 {
				pidSet[pid] = true
			}
		}
		pidToName := make(map[int]string)
		if len(pidSet) > 0 {
			taskOut := utils.RunCmd("tasklist /FO CSV /NH", 5)
			for _, line := range strings.Split(taskOut, "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, `"`) {
					continue
				}
				csvParts := strings.Split(line, `","`)
				if len(csvParts) >= 2 {
					name := strings.Trim(csvParts[0], `"`)
					var pid int
					fmt.Sscanf(strings.Trim(csvParts[1], `"`), "%d", &pid)
					if pidSet[pid] {
						pidToName[pid] = name
					}
				}
			}
		}
		var conns [][]string
		for _, e := range entries {
			if name, ok := pidToName[e.pid]; ok {
				e.row[2] = name
			} else {
				e.row[2] = "System"
			}
			conns = append(conns, e.row)
		}
		if len(conns) > 50 {
			conns = conns[:50]
		}
		a.auditData = conns
	}
}

func (a *App) cidrUpdateDaemon() {
	time.Sleep(10 * time.Second)
	interval := 3 * time.Hour
	for a.monitorRun {
		lastUpdate := a.store.LoadCIDRLastUpdate()
		now := float64(time.Now().Unix())
		if lastUpdate > 0 && (now-lastUpdate) < interval.Seconds() {
			time.Sleep(interval)
			continue
		}
		cidrList := a.engine.DownloadChnRoutes(nil)
		if len(cidrList) > 0 {
			a.cidrCache = cidrList
		}
		time.Sleep(interval)
	}
}

func (a *App) adapterMonitorDaemon() {
	alertTimestamps := make(map[string]time.Time)
	for a.monitorRun {
		time.Sleep(2 * time.Second)
		enabled, _ := a.adapterMon["enabled"].(bool)
		if !enabled {
			continue
		}
		var monitored []string
		switch v := a.adapterMon["monitored_adapters"].(type) {
		case []string:
			monitored = v
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok {
					monitored = append(monitored, s)
				}
			}
		}
		if len(monitored) == 0 {
			continue
		}
		ifaces, err := net.Interfaces()
		if err != nil {
			continue
		}
		statusMap := make(map[string]bool)
		for _, iface := range ifaces {
			statusMap[iface.Name] = iface.Flags&net.FlagUp != 0
		}
		now := time.Now()
		for _, adp := range monitored {
			currentUp, exists := statusMap[adp]
			if !exists {
				currentUp = false
			}
			prevUp, hadPrev := a.adapterLastStatus[adp]
			if hadPrev && prevUp && !currentUp {
				lastAlert, hasAlert := alertTimestamps[adp]
				if !hasAlert || now.Sub(lastAlert) > 10*time.Second {
					alertTimestamps[adp] = now
					a.log(fmt.Sprintf("网卡 [%s] 已断开连接！", adp), "error")
					if a.ctx != nil {
						wailsruntime.EventsEmit(a.ctx, "adapter_disconnected", adp)
					}
					showWindowsNotification("NetShunt 网卡断开", fmt.Sprintf("网卡 [%s] 已断开连接！分流策略可能受影响。", adp))
				}
			}
			if hadPrev && !prevUp && currentUp {
				a.log(fmt.Sprintf("网卡 [%s] 已恢复连接。", adp), "success")
				if a.ctx != nil {
					wailsruntime.EventsEmit(a.ctx, "adapter_connected", adp)
				}
				showWindowsNotification("NetShunt 网卡恢复", fmt.Sprintf("网卡 [%s] 已恢复连接。", adp))
			}
			a.adapterLastStatus[adp] = currentUp
		}
	}
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// ==================== 策略 CRUD ====================

func (a *App) loadStrategiesFromStore() []*models.Strategy {
	a.stratCounter = 1
	fallback := &models.Strategy{
		ID:        models.FallbackID,
		Name:      "系统兜底（国际/默认）",
		Type:      models.StrategyFallback,
		Enabled:   true,
		Adapter:   "",
		CreatedAt: time.Now().Unix(),
	}
	raw := a.store.LoadStrategies()
	if raw == nil {
		return []*models.Strategy{fallback}
	}
	list, ok := raw.([]interface{})
	if !ok {
		return []*models.Strategy{fallback}
	}
	var strategies []*models.Strategy
	strategies = append(strategies, fallback)
	for _, item := range list {
		sm, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		s := parseStrategyFromParams(map[string]interface{}{"strategy": sm})
		if s == nil {
			continue
		}
		if s.ID == models.FallbackID {
			if s.Type == models.StrategyFallback {
				fallback.Name = s.Name
				fallback.Adapter = s.Adapter
				fallback.Enabled = s.Enabled
				fallback.CreatedAt = s.CreatedAt
				fallback.UpdatedAt = s.UpdatedAt
			} else {
				s.ID = a.stratCounter
				a.stratCounter++
				strategies = append(strategies, s)
			}
			continue
		}
		if s.ID >= a.stratCounter {
			a.stratCounter = s.ID + 1
		}
		strategies = append(strategies, s)
	}
	return strategies
}

func (a *App) saveStrategies() {
	a.store.SaveStrategies(a.strategies)
}

func (a *App) handleCreateStrategy(params map[string]interface{}) map[string]interface{} {
	s := parseStrategyFromParams(params)
	if s == nil {
		return map[string]interface{}{"ok": false, "error": "参数无效"}
	}
	a.stratCounter++
	s.ID = a.stratCounter
	s.CreatedAt = time.Now().Unix()
	s.UpdatedAt = s.CreatedAt
	a.mu.Lock()
	a.strategies = append(a.strategies, s)
	a.mu.Unlock()
	a.saveStrategies()
	return map[string]interface{}{"ok": true, "strategy": s}
}

func (a *App) handleUpdateStrategy(params map[string]interface{}) map[string]interface{} {
	s := parseStrategyFromParams(params)
	if s == nil {
		return map[string]interface{}{"ok": false, "error": "参数无效"}
	}
	id := getInt(params, "id")
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, existing := range a.strategies {
		if existing.ID == id {
			s.ID = id
			s.CreatedAt = existing.CreatedAt
			s.UpdatedAt = time.Now().Unix()
			if existing.IsFallback() {
				s.Type = models.StrategyFallback
				s.Source = nil
				s.Adapter = existing.Adapter
			}
			a.strategies[i] = s
			a.saveStrategies()
			return map[string]interface{}{"ok": true, "strategy": s}
		}
	}
	return map[string]interface{}{"ok": false, "error": "策略不存在"}
}

func (a *App) handleDeleteStrategy(params map[string]interface{}) map[string]interface{} {
	id := getInt(params, "id")
	if id == models.FallbackID {
		return map[string]interface{}{"ok": false, "error": "兜底策略不可删除"}
	}
	a.mu.Lock()
	var toDelete *models.Strategy
	var deleteIdx int = -1
	for i, s := range a.strategies {
		if s.ID == id {
			toDelete = s
			deleteIdx = i
			break
		}
	}
	if deleteIdx == -1 {
		a.mu.Unlock()
		return map[string]interface{}{"ok": false, "error": "策略不存在"}
	}
	a.strategies = append(a.strategies[:deleteIdx], a.strategies[deleteIdx+1:]...)
	a.saveStrategies()
	a.mu.Unlock()

	if toDelete != nil {
		go a.engine.RemoveStrategyRoutes(toDelete, a.logFn())
	}

	a.updateRoleStatus()
	return map[string]interface{}{"ok": true}
}

func parseStrategyFromParams(params map[string]interface{}) *models.Strategy {
	sm, ok := params["strategy"].(map[string]interface{})
	if !ok {
		return nil
	}
	s := &models.Strategy{}
	s.ID = getInt(sm, "id")
	s.Name = getString(sm, "name")
	s.Type = models.StrategyType(getString(sm, "type"))
	if v, ok := sm["enabled"].(bool); ok {
		s.Enabled = v
	}
	s.Adapter = getString(sm, "adapter")
	if v, ok := sm["created_at"].(float64); ok {
		s.CreatedAt = int64(v)
	}
	if v, ok := sm["updated_at"].(float64); ok {
		s.UpdatedAt = int64(v)
	}
	if src, ok := sm["source"].(map[string]interface{}); ok {
		source := &models.AddressSource{}
		source.Mode = getString(src, "mode")
		source.URL = getString(src, "url")
		source.UpdateUnit = getString(src, "update_unit")
		if v, ok := src["update_interval"].(float64); ok {
			source.UpdateInterval = int(v)
		}
		source.SyncStatus = models.SyncStatus(getString(src, "sync_status"))
		source.CacheFile = getString(src, "cache_file")
		if v, ok := src["last_sync"].(float64); ok {
			source.LastSync = int64(v)
		}
		if addrs, ok := src["addresses"].([]interface{}); ok {
			for _, addr := range addrs {
				if str, ok := addr.(string); ok {
					source.Addresses = append(source.Addresses, str)
				}
			}
			source.AddressCount = len(source.Addresses)
		}
		if v, ok := src["address_count"].(float64); ok && source.AddressCount == 0 {
			source.AddressCount = int(v)
		}
		s.Source = source
	}
	return s
}

func getInt(m map[string]interface{}, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}
func (a *App) handleSyncStrategyURL(params map[string]interface{}) map[string]interface{} {
	url := getString(params, "url")
	if url == "" {
		return map[string]interface{}{"ok": false, "error": "URL 不能为空"}
	}
	cidrs, cacheFile := a.downloadStrategyCIDRs(url)
	if len(cidrs) == 0 {
		return map[string]interface{}{"ok": false, "error": "下载失败或地址库为空"}
	}
	return map[string]interface{}{"ok": true, "count": len(cidrs), "cache_file": cacheFile}
}

func (a *App) downloadStrategyCIDRs(url string) ([]string, string) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, ""
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, ""
	}
	lines := strings.Split(string(body), "\n")
	var validLines []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		validLines = append(validLines, line)
	}
	if len(validLines) == 0 {
		return nil, ""
	}
	os.MkdirAll("data", 0755)
	cacheFile := fmt.Sprintf("data/strategy_cache_%d.txt", time.Now().Unix())
	os.WriteFile(cacheFile, []byte(strings.Join(validLines, "\n")), 0644)
	a.cleanupOldCacheFiles(cacheFile)
	return validLines, cacheFile
}

func (a *App) cleanupOldCacheFiles(keepFile string) {
	entries, err := os.ReadDir("data")
	if err != nil {
		return
	}
	keepSet := map[string]bool{keepFile: true}
	for _, s := range a.strategies {
		if s.Source != nil && s.Source.CacheFile != "" {
			keepSet[s.Source.CacheFile] = true
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "strategy_cache_") || !strings.HasSuffix(name, ".txt") {
			continue
		}
		fullPath := "data/" + name
		if !keepSet[fullPath] {
			os.Remove(fullPath)
		}
	}
}

func (a *App) loadStrategyCacheFile(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var result []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}
func (a *App) handleApplyStrategies() map[string]interface{} {
	a.adapters.Refresh()
	profiles := a.adapters.AllProfilesSorted()
	profileMap := make(map[string]*models.AdapterProfile)
	for _, p := range profiles {
		profileMap[p.Name] = p
	}

	totalRoutes := 0
	for _, s := range a.strategies {
		if !s.Enabled || s.Adapter == "" {
			continue
		}
		if s.IsFallback() {
			totalRoutes++
			continue
		}
		if s.Source == nil {
			continue
		}
		if s.Source.Mode == "online" {
			var cidrs []string
			if s.Source.CacheFile != "" {
				cidrs = a.loadStrategyCacheFile(s.Source.CacheFile)
			}
			totalRoutes += len(cidrs)
		} else {
			totalRoutes += len(s.Source.Addresses)
		}
	}

	emitProgress := func(current, total int, message string) {
		if a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "apply:progress", map[string]interface{}{
				"current": current,
				"total":   total,
				"message": message,
			})
		}
	}

	emitProgress(0, totalRoutes, "正在准备策略...")
	a.log("正在应用分流策略...", "info")

	var applied, failed int
	var messages []string
	currentRoute := 0

	for _, s := range a.strategies {
		if !s.Enabled || s.Adapter == "" {
			continue
		}
		p, ok := profileMap[s.Adapter]
		if !ok {
			failed++
			messages = append(messages, fmt.Sprintf("[%s] 网卡 %s 未找到", s.Name, s.Adapter))
			continue
		}
		if !p.RouteReady() {
			failed++
			messages = append(messages, fmt.Sprintf("[%s] 网卡 %s 未就绪", s.Name, s.Adapter))
			continue
		}

		gw := p.Gateway()
		idx := p.IfIndex
		a.log(fmt.Sprintf("[%s] 开始处理 → 网卡 %s (网关 %s IF %d)", s.Name, s.Adapter, gw, idx), "info")

		if s.IsFallback() {
			currentRoute++
			emitProgress(currentRoute, totalRoutes, fmt.Sprintf("[%s] 正在注入默认路由...", s.Name))
			cmd := fmt.Sprintf("route add 0.0.0.0 mask 0.0.0.0 %s metric 10 IF %d", gw, idx)
			out := utils.RunCmd(cmd, 15)
			if !utils.CmdFailed(out) {
				applied++
				messages = append(messages, fmt.Sprintf("[%s] 默认路由 → %s IF %d ✓", s.Name, gw, idx))
				a.log(fmt.Sprintf("[%s] 默认路由 → %s IF %d ✓", s.Name, gw, idx), "success")
			} else {
				failed++
				messages = append(messages, fmt.Sprintf("[%s] 默认路由失败: %s", s.Name, out))
				a.log(fmt.Sprintf("[%s] 默认路由失败: %s", s.Name, out), "error")
			}
			continue
		}

		if s.Source == nil {
			continue
		}

		var cidrList []string
		if s.Source.Mode == "online" {
			if s.Source.CacheFile != "" {
				cidrList = a.loadStrategyCacheFile(s.Source.CacheFile)
			}
			if len(cidrList) == 0 && s.Source.URL != "" {
				emitProgress(currentRoute, totalRoutes, fmt.Sprintf("[%s] 正在下载地址库...", s.Name))
				a.log(fmt.Sprintf("[%s] 正在下载地址库 %s ...", s.Name, s.Source.URL), "info")
				messages = append(messages, fmt.Sprintf("[%s] 正在自动下载地址库 %s ...", s.Name, s.Source.URL))
				dlCIDRs, dlPath := a.downloadStrategyCIDRs(s.Source.URL)
				if len(dlCIDRs) > 0 {
					s.Source.CacheFile = dlPath
					s.Source.AddressCount = len(dlCIDRs)
					s.Source.SyncStatus = models.SyncSuccess
					s.Source.LastSync = time.Now().Unix()
					cidrList = dlCIDRs
					a.saveStrategies()
					totalRoutes += len(dlCIDRs)
					messages = append(messages, fmt.Sprintf("[%s] 下载完成 · %d 条地址", s.Name, len(dlCIDRs)))
					a.log(fmt.Sprintf("[%s] 下载完成 · %d 条地址，开始注入路由...", s.Name, len(dlCIDRs)), "success")
					emitProgress(currentRoute, totalRoutes, fmt.Sprintf("[%s] 下载完成，开始注入 %d 条路由...", s.Name, len(dlCIDRs)))
				}
			}
			if len(cidrList) == 0 {
				failed++
				messages = append(messages, fmt.Sprintf("[%s] 地址库下载失败或为空，请检查 URL", s.Name))
				continue
			}
		} else {
			cidrList = s.Source.Addresses
		}

		stratApplied := 0
		for i, line := range cidrList {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if (i+1)%100 == 0 || i == len(cidrList)-1 {
				emitProgress(currentRoute, totalRoutes, fmt.Sprintf("[%s] 正在注入路由 %d/%d...", s.Name, i+1, len(cidrList)))
			}
			if (i+1)%500 == 0 {
				a.log(fmt.Sprintf("[%s] 已注入 %d/%d 条路由...", s.Name, i+1, len(cidrList)), "info")
			}
			var cmd string
			if strings.Contains(line, "/") {
				parts := strings.SplitN(line, "/", 2)
				ip := parts[0]
				var bits int
				fmt.Sscanf(parts[1], "%d", &bits)
				if bits < 0 || bits > 32 {
					continue
				}
				mask := utils.CIDRToNetmask(bits)
				cmd = fmt.Sprintf("route add %s mask %s %s metric 1 IF %d", ip, mask, gw, idx)
			} else {
				cmd = fmt.Sprintf("route add %s mask 255.255.255.255 %s metric 1 IF %d", line, gw, idx)
			}
			out := utils.RunCmd(cmd, 10)
			if !utils.CmdFailed(out) {
				stratApplied++
			}
			currentRoute++
		}
		applied++
		messages = append(messages, fmt.Sprintf("[%s] 注入 %d 条路由 → %s IF %d ✓", s.Name, stratApplied, gw, idx))
		a.log(fmt.Sprintf("[%s] 完成 · 注入 %d 条路由 → %s IF %d ✓", s.Name, stratApplied, gw, idx), "success")
	}

	result := map[string]interface{}{
		"ok":       true,
		"applied":  applied,
		"failed":   failed,
		"messages": messages,
	}

	a.log(fmt.Sprintf("策略应用完成: 成功 %d, 失败 %d", applied, failed), "info")

	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "apply:done", result)
	}

	return result
}
func compareVersion(v1, v2 string) int {
	p1 := strings.Split(v1, ".")
	p2 := strings.Split(v2, ".")
	for i := 0; i < len(p1) || i < len(p2); i++ {
		var n1, n2 int
		if i < len(p1) {
			n1, _ = strconv.Atoi(p1[i])
		}
		if i < len(p2) {
			n2, _ = strconv.Atoi(p2[i])
		}
		if n1 > n2 {
			return 1
		}
		if n1 < n2 {
			return -1
		}
	}
	return 0
}
