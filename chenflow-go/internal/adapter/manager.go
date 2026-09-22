package adapter

import (
	"net"
	"sort"
	"strings"
	"sync"

	"chenflow-go/internal/config"
	"chenflow-go/internal/models"
	"chenflow-go/internal/utils"
)

type Manager struct {
	mu       sync.Mutex
	store    *config.Store
	profiles map[string]*models.AdapterProfile
}

func NewManager(store *config.Store) *Manager {
	return &Manager{
		store:    store,
		profiles: make(map[string]*models.AdapterProfile),
	}
}

func (m *Manager) Profiles() map[string]*models.AdapterProfile {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]*models.AdapterProfile)
	for k, v := range m.profiles {
		result[k] = v
	}
	return result
}

func (m *Manager) Names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.profiles))
	for n := range m.profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (m *Manager) GetProfile(name string) *models.AdapterProfile {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.profiles[name]
}

func (m *Manager) DefaultExit() *models.AdapterProfile {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := m.store.GetDefaultExitAdapter()
	if name == "" {
		return nil
	}
	return m.profiles[name]
}

func (m *Manager) CNSplit() *models.AdapterProfile {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.profiles {
		if p.Role == models.RoleCNSplit {
			return p
		}
	}
	return nil
}

func (m *Manager) ReadyAdapters() []*models.AdapterProfile {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*models.AdapterProfile
	for _, p := range m.profiles {
		if p.Ready() {
			result = append(result, p)
		}
	}
	return result
}

func (m *Manager) Refresh() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.detect()
	m.syncRoles()
}

func (m *Manager) RefreshLight() { m.Refresh() }
func (m *Manager) RefreshByMAC() { m.Refresh() }

var hiddenKeywords = []string{
	"loopback", "本地连接*", "wintun", "tap-windows", "tap-win",
	"teredo", "isatap", "6to4", "vmnet", "virtualbox",
	"hyper-v", "bluetooth", "蓝牙", "pseudo",
}

func isHiddenAdapter(name string) bool {
	lower := strings.ToLower(name)
	for _, kw := range hiddenKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func (m *Manager) detect() {
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}
	gwMap := parseIpConfigGateways(utils.RunCmd("ipconfig", 5))
	roles := m.store.GetRoles()

	newProfiles := make(map[string]*models.AdapterProfile)
	for _, iface := range ifaces {
		if iface.Name == "" {
			continue
		}
		ip := ""
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok {
				if v4 := ipNet.IP.To4(); v4 != nil && !v4.IsLoopback() {
					ip = v4.String()
					break
				}
			}
		}
		mac := ""
		if len(iface.HardwareAddr) > 0 {
			mac = strings.ToUpper(iface.HardwareAddr.String())
		}
		p := &models.AdapterProfile{
			Name:        iface.Name,
			MAC:         mac,
			IfIndex:     iface.Index,
			IsUp:        iface.Flags&net.FlagUp != 0,
			IP:          ip,
			Role:        models.RoleIgnored,
			GatewayMode: "auto",
		}
		if gw, ok := gwMap[iface.Name]; ok {
			p.SetGateway(gw)
		}
		if role, ok := roles[iface.Name]; ok {
			p.Role = models.AdapterRole(role)
		}
		newProfiles[iface.Name] = p
	}
	m.profiles = newProfiles
}

func parseIpConfigGateways(out string) map[string]string {
	result := make(map[string]string)
	var currentAdapter string
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "Windows IP 配置" || trimmed == "Windows IP Configuration" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && strings.HasSuffix(trimmed, ":") {
			name := strings.TrimSuffix(trimmed, ":")
			if idx := strings.LastIndex(name, "适配器 "); idx >= 0 {
				name = strings.TrimSpace(name[idx+len("适配器 "):])
			} else if idx := strings.LastIndex(name, "adapter "); idx >= 0 {
				name = strings.TrimSpace(name[idx+len("adapter "):])
			}
			currentAdapter = name
			continue
		}
		if currentAdapter != "" && (strings.Contains(trimmed, "默认网关") || strings.Contains(trimmed, "Default Gateway")) {
			parts := strings.Split(trimmed, ":")
			if len(parts) >= 2 {
				gw := strings.TrimSpace(parts[len(parts)-1])
				if gw != "" && gw != "0.0.0.0" {
					result[currentAdapter] = gw
				}
			}
		}
	}
	return result
}

func (m *Manager) syncRoles() {
	roles := m.store.GetRoles()
	for name, p := range m.profiles {
		if role, ok := roles[name]; ok {
			p.Role = models.AdapterRole(role)
		}
	}
}

func (m *Manager) SyncRoles() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncRoles()
}

func RankForRecommend(p *models.AdapterProfile) int {
	score := 0
	if p.IsUp {
		score += 100
	}
	if p.Gateway() != "" {
		score += 50
	}
	if p.IfIndex != 0 {
		score += 10
	}
	name := strings.ToLower(p.Name)
	if strings.Contains(name, "ethernet") || strings.Contains(p.Name, "以太网") {
		score += 5
	}
	if strings.Contains(name, "wlan") || strings.Contains(p.Name, "WLAN") {
		score += 3
	}
	return score
}

func (m *Manager) ReadyForRecommend() []*models.AdapterProfile {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*models.AdapterProfile
	for _, p := range m.profiles {
		if p.IsUp && p.Gateway() != "" && p.IfIndex != 0 {
			result = append(result, p)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return RankForRecommend(result[i]) > RankForRecommend(result[j])
	})
	return result
}
func (m *Manager) AllProfilesSorted() []*models.AdapterProfile {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*models.AdapterProfile
	for _, p := range m.profiles {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		return RankForRecommend(result[i]) > RankForRecommend(result[j])
	})
	return result
}
