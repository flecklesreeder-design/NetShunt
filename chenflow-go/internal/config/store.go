package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const AppDirName = "NetShunt"

var (
	LegacyEthMAC  = "E0-BE-03-5F-77-B8"
	LegacyWlanMAC = "14-75-5B-BF-5B-C6"
)

type GatewayOverride struct {
	Mode    string `json:"mode"`
	Gateway string `json:"gateway"`
}

type AppConfig struct {
	Theme              string                     `json:"theme"`
	AdapterRoles       map[string]string          `json:"adapter_roles"`
	AdapterMacs        map[string]string          `json:"adapter_macs"`
	AdapterGateways    map[string]GatewayOverride `json:"adapter_gateways"`
	CloseAction        string                     `json:"close_action"`
	Hotkey             string                     `json:"hotkey"`
	DefaultExitAdapter string                     `json:"default_exit_adapter"`
	Language           string                     `json:"language"`
}

type Store struct {
	mu        sync.Mutex
	baseDir   string
	appConfig AppConfig
}

func New() *Store {
	appdata := os.Getenv("APPDATA")
	if appdata == "" {
		appdata, _ = os.Getwd()
	}
	baseDir := filepath.Join(appdata, AppDirName)
	os.MkdirAll(baseDir, 0755)
	s := &Store{
		baseDir: baseDir,
		appConfig: AppConfig{
			Theme:           "dawn",
			AdapterRoles:    make(map[string]string),
			AdapterMacs:     make(map[string]string),
			AdapterGateways: make(map[string]GatewayOverride),
		},
	}
	s.loadAppConfig()
	return s
}

func (s *Store) p(name string) string {
	return filepath.Join(s.baseDir, name)
}

func readJSON(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func writeJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) loadAppConfig() {
	s.mu.Lock()
	defer s.mu.Unlock()
	readJSON(s.p("app_config.json"), &s.appConfig)
	if s.appConfig.AdapterRoles == nil {
		s.appConfig.AdapterRoles = make(map[string]string)
	}
	if s.appConfig.AdapterMacs == nil {
		s.appConfig.AdapterMacs = make(map[string]string)
	}
	if s.appConfig.AdapterGateways == nil {
		s.appConfig.AdapterGateways = make(map[string]GatewayOverride)
	}
}

func (s *Store) saveAppConfig() {
	if err := writeJSON(s.p("app_config.json"), s.appConfig); err != nil {
		fmt.Println("[Store] saveAppConfig failed:", err)
	}
}

func (s *Store) GetRoles() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]string)
	for k, v := range s.appConfig.AdapterRoles {
		result[k] = v
	}
	return result
}

func (s *Store) GetMACs() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]string)
	for k, v := range s.appConfig.AdapterMacs {
		result[k] = v
	}
	return result
}

func (s *Store) SetRole(name, role, mac string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if role == "default_exit" {
		for k, v := range s.appConfig.AdapterRoles {
			if v == "default_exit" && k != name {
				s.appConfig.AdapterRoles[k] = "custom_only"
			}
		}
	}
	if role == "cn_split" {
		for k, v := range s.appConfig.AdapterRoles {
			if v == "cn_split" && k != name {
				s.appConfig.AdapterRoles[k] = "custom_only"
			}
		}
	}
	s.appConfig.AdapterRoles[name] = role
	if mac != "" {
		s.appConfig.AdapterMacs[name] = mac
	}
	s.saveAppConfig()
}

func (s *Store) GetDefaultExitAdapter() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appConfig.DefaultExitAdapter
}

func (s *Store) SetDefaultExitAdapter(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appConfig.DefaultExitAdapter = name
	s.saveAppConfig()
}

func (s *Store) GetLanguage() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appConfig.Language == "" {
		return "zh"
	}
	return s.appConfig.Language
}

func (s *Store) SetLanguage(lang string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lang != "zh" && lang != "en" {
		lang = "zh"
	}
	s.appConfig.Language = lang
	s.saveAppConfig()
}

func (s *Store) GetTheme() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appConfig.Theme
}

func (s *Store) SetTheme(theme string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appConfig.Theme = theme
	s.saveAppConfig()
}

func (s *Store) GetCloseAction() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appConfig.CloseAction
}

func (s *Store) SetCloseAction(action string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appConfig.CloseAction = action
	s.saveAppConfig()
}

func (s *Store) GetHotkey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appConfig.Hotkey
}

func (s *Store) SetHotkey(hotkey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appConfig.Hotkey = hotkey
	s.saveAppConfig()
}

func (s *Store) SetGatewayOverride(name, mode, gateway string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appConfig.AdapterGateways[name] = GatewayOverride{
		Mode: mode, Gateway: strings.TrimSpace(gateway),
	}
	s.saveAppConfig()
}

func (s *Store) LoadCIDRCache() []string {
	var raw []string
	readJSON(s.p("cn_cidr_cache.json"), &raw)
	return raw
}

func (s *Store) SaveCIDRCache(list []string) error {
	return writeJSON(s.p("cn_cidr_cache.json"), list)
}

func (s *Store) LoadCIDRLastUpdate() float64 {
	var raw map[string]float64
	readJSON(s.p("cidr_last_update.json"), &raw)
	return raw["last_update"]
}

func (s *Store) SaveCIDRLastUpdate(ts float64) error {
	return writeJSON(s.p("cidr_last_update.json"), map[string]float64{"last_update": ts})
}

func (s *Store) LoadRules() []map[string]interface{} {
	var raw []map[string]interface{}
	readJSON(s.p("network_rules.json"), &raw)
	return raw
}

func (s *Store) SaveRules(rules interface{}) error {
	return writeJSON(s.p("network_rules.json"), rules)
}

func (s *Store) LoadPresetAddresses() map[string]interface{} {
	var raw map[string]interface{}
	readJSON(s.p("preset_addresses.json"), &raw)
	return raw
}

func (s *Store) SavePresetAddresses(data interface{}) error {
	return writeJSON(s.p("preset_addresses.json"), data)
}

func (s *Store) LoadAdapterMonitor() map[string]interface{} {
	var raw map[string]interface{}
	readJSON(s.p("adapter_monitor.json"), &raw)
	return raw
}

func (s *Store) SaveAdapterMonitor(data interface{}) error {
	return writeJSON(s.p("adapter_monitor.json"), data)
}
func (s *Store) LoadStrategies() interface{} {
	var raw interface{}
	readJSON(s.p("strategies.json"), &raw)
	return raw
}

func (s *Store) SaveStrategies(data interface{}) error {
	return writeJSON(s.p("strategies.json"), data)
}
