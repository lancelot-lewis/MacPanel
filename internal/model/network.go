package model

import "time"

// InterfaceKind 网络接口分类，用于前端分组展示。
type InterfaceKind string

const (
	KindLoopback InterfaceKind = "loopback"
	KindEthernet InterfaceKind = "ethernet"
	KindWiFi     InterfaceKind = "wifi"
	KindTunnel   InterfaceKind = "tunnel"
	KindBridge   InterfaceKind = "bridge"
	KindVirtual  InterfaceKind = "virtual"
	KindOther    InterfaceKind = "other"
)

// Address 单个接口上的一个地址。
type Address struct {
	Address     string `json:"address"`
	PrefixLen   int    `json:"prefixLen"`
	Family      string `json:"family"` // ipv4 / ipv6
	Scope       string `json:"scope"`  // global / link-local / loopback / private
	IsLoopback  bool   `json:"isLoopback"`
	IsLinkLocal bool   `json:"isLinkLocal"`
	IsPrivate   bool   `json:"isPrivate"`
}

// Interface 一个网络接口及其地址。
type Interface struct {
	Name         string        `json:"name"`
	Index        int           `json:"index"`
	Kind         InterfaceKind `json:"kind"`
	Up           bool          `json:"up"`
	Loopback     bool          `json:"loopback"`
	MTU          int           `json:"mtu"`
	HardwareAddr string        `json:"hardwareAddr,omitempty"`
	Flags        []string      `json:"flags"`
	IPv4         []Address     `json:"ipv4"`
	IPv6         []Address     `json:"ipv6"`
}

// NetworkSnapshot 网络接口快照。
type NetworkSnapshot struct {
	Interfaces  []Interface     `json:"interfaces"`
	HasIPv6     bool            `json:"hasIPv6"`
	HasGlobalV6 bool            `json:"hasGlobalIPv6"`
	CollectedAt time.Time       `json:"collectedAt"`
	Missing     []MissingMetric `json:"missing,omitempty"`
}
