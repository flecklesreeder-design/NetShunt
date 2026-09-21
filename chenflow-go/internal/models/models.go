package models

import "strings"

// AdapterRole 网卡角色枚举
type AdapterRole string

const (
	RoleIgnored     AdapterRole = "ignored"
	RoleDefaultExit AdapterRole = "default_exit"
	RoleCNSplit     AdapterRole = "cn_split"
	RoleCustomOnly  AdapterRole = "custom_only"
)

// RoleLabels 角色→中文标签
var RoleLabels = map[AdapterRole]string{
	RoleIgnored:     "不参与",
	RoleDefaultExit: "默认出口",
	RoleCNSplit:     "国内分流",
	RoleCustomOnly:  "仅自定义规则",
}

// LabelToRole 中文标签→角色
var LabelToRole = map[string]AdapterRole{
	"不参与":    RoleIgnored,
	"默认出口":   RoleDefaultExit,
	"国内分流":   RoleCNSplit,
	"仅自定义规则": RoleCustomOnly,
}

// AdapterProfile 网卡档案
type AdapterProfile struct {
	Name          string      `json:"name"`
	MAC           string      `json:"mac"`
	InterfaceGUID string      `json:"interface_guid"`
	IP            string      `json:"ip"`
	GatewayAuto   string      `json:"gateway_auto"`
	CachedGateway string      `json:"cached_gateway"`
	GatewayManual string      `json:"gateway_manual"`
	GatewayMode   string      `json:"gateway_mode"` // "auto" | "manual"
	IfIndex       int         `json:"if_index"`
	IsUp          bool        `json:"is_up"`
	Role          AdapterRole `json:"role"`
}

// Gateway 返回实际生效的网关。回退链：手动 > 自动探测 > 历史缓存
func (p *AdapterProfile) Gateway() string {
	if p.GatewayMode == "manual" && p.GatewayManual != "" {
		return p.GatewayManual
	}
	if p.GatewayAuto != "" {
		return p.GatewayAuto
	}
	return p.CachedGateway
}

// SetGateway 设置自动探测网关（非空才更新，空值保留缓存）
func (p *AdapterProfile) SetGateway(value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		p.GatewayAuto = value
		p.CachedGateway = value
	}
}

// Ready 路由下发全部条件
func (p *AdapterProfile) Ready() bool {
	return p.IsUp && p.Gateway() != "" && p.IfIndex != 0 && p.IP != ""
}

// RouteReady 路由下发最低条件
func (p *AdapterProfile) RouteReady() bool {
	return p.IsUp && p.Gateway() != "" && p.IfIndex != 0
}

// DefaultRouteReady 默认路由下发条件
func (p *AdapterProfile) DefaultRouteReady() bool {
	return p.IsUp && p.Gateway() != ""
}

// SplitRule 分流规则
type SplitRule struct {
	ID      int      `json:"id"`
	Type    string   `json:"type"` // URL / IPv4 / IPv6
	Target  string   `json:"target"`
	IPs     []string `json:"ips"`
	Adapter string   `json:"adapter"`
	Status  string   `json:"status"` // Pending / Active
}

// ==================== 流量策略（三大场景模板） ====================

// StrategyType 策略类型枚举
type StrategyType string

const (
	StrategyOnline   StrategyType = "online"   // 在线地址库（国内分流）
	StrategyLocal    StrategyType = "local"    // 本地地址库（公司内网/自定义）
	StrategyFallback StrategyType = "fallback" // 系统兜底（单例，无地址库）
)

// SyncStatus 在线库同步状态
type SyncStatus string

const (
	SyncIdle    SyncStatus = "idle"
	SyncRunning SyncStatus = "syncing"
	SyncSuccess SyncStatus = "success"
	SyncFailed  SyncStatus = "failed"
)

// AddressSource 地址源定义（与网卡绑定解耦）。
// online 模式：URL + 自动更新频率 + 同步状态；
// local  模式：静态 CIDR/IP 地址列表。
type AddressSource struct {
	Mode           string     `json:"mode"`                      // "online" | "local"
	URL            string     `json:"url,omitempty"`             // online: 地址库 URL
	UpdateInterval int        `json:"update_interval,omitempty"` // online: 更新频率数值
	UpdateUnit     string     `json:"update_unit,omitempty"`     // online: "hours" | "days"
	LastSync       int64      `json:"last_sync,omitempty"`       // online: 上次同步 Unix 时间戳
	SyncStatus     SyncStatus `json:"sync_status,omitempty"`     // online: 同步状态
	Addresses      []string   `json:"addresses,omitempty"`       // local: CIDR/IP 列表
	AddressCount   int        `json:"address_count,omitempty"`   // 地址总数（local 直接计数 / online 缓存计数）
	CacheFile      string     `json:"cache_file,omitempty"`      // online: 本地缓存文件路径
}

// Strategy 流量策略（三大场景统一承载）。
// Adapter（网卡绑定）与 Source（地址源）解耦为独立字段。
// Fallback 策略的 Source 为 nil，天然无地址库概念。
type Strategy struct {
	ID        int            `json:"id"`
	Name      string         `json:"name"`
	Type      StrategyType   `json:"type"`
	Enabled   bool           `json:"enabled"`
	Adapter   string         `json:"adapter"`          // 绑定网卡名（与 Source 解耦）
	Source    *AddressSource `json:"source,omitempty"` // 地址源（fallback 时为 nil）
	CreatedAt int64          `json:"created_at,omitempty"`
	UpdatedAt int64          `json:"updated_at,omitempty"`
}

// FallbackID 兜底策略固定 ID（单例）
const FallbackID = 0

// IsFallback 是否为系统兜底策略
func (s *Strategy) IsFallback() bool {
	return s.Type == StrategyFallback
}

// HasSource 是否拥有地址源（fallback 无）
func (s *Strategy) HasSource() bool {
	return s.Source != nil
}
