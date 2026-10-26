package model

import "time"

// AnchorState PF Anchor 的加载状态。
type AnchorState string

const (
	// AnchorUnknown 无法确认（通常是权限不足，无法读取 /dev/pf）。
	AnchorUnknown AnchorState = "unknown"
	// AnchorAbsent 规则文件不存在且未加载。
	AnchorAbsent AnchorState = "absent"
	// AnchorPresent 规则文件存在，加载状态未知。
	AnchorPresent AnchorState = "present"
	// AnchorLoaded 已确认被 PF 加载。
	AnchorLoaded AnchorState = "loaded"
)

// PFInfo PF 全局状态。字段为指针表示「无法确认」，不要用 false 冒充「已关闭」。
type PFInfo struct {
	Enabled       *bool  `json:"enabled"`
	EnforceState  *bool  `json:"enforceState,omitempty"`
	State         string `json:"state,omitempty"` // 例如 enabled / disabled / unknown
	Detail        string `json:"detail,omitempty"`
	ReadAvailable bool   `json:"readAvailable"`
	ReadError     string `json:"readError,omitempty"`
}

// AnchorInfo 本应用专用 Anchor 的信息（只读）。
type AnchorInfo struct {
	Name         string      `json:"name"`
	RulePath     string      `json:"rulePath"`
	State        AnchorState `json:"state"`
	Present      bool        `json:"present"`
	Referenced   bool        `json:"referenced"` // /etc/pf.conf 是否包含 load anchor 引用
	SizeBytes    *int64      `json:"sizeBytes,omitempty"`
	LastModified *time.Time  `json:"lastModified,omitempty"`
	Notes        []string    `json:"notes,omitempty"`
}

// ManagedState 本应用「期望状态」与实际状态的对照。
// M1 只做只读采集，期望状态固定为 not_configured。
type ManagedState string

const (
	StateNotConfigured ManagedState = "not_configured"
	StateConfigured    ManagedState = "configured"
	StateDrift         ManagedState = "drift"
)

// FirewallStatus IPv6 防火墙状态（M1 只读）。
type FirewallStatus struct {
	Managed       ManagedState `json:"managed"`
	PF            PFInfo       `json:"pf"`
	Anchor        AnchorInfo   `json:"anchor"`
	LoadedAnchors []string     `json:"loadedAnchors,omitempty"`
	Warnings      []string     `json:"warnings,omitempty"`
	CollectedAt   time.Time    `json:"collectedAt"`
	// Writable 表示当前 Agent 是否具备修改 PF 的能力（M1 恒为 false，
	// 因为写路径尚未实现，后端不会假装可以操作）。
	Writable bool `json:"writable"`
}
