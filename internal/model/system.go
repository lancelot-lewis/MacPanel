package model

import "time"

// MetricState 表示单个指标的数据质量。
// 采集失败时必须是 unavailable，不能被填充为 0 或 false 冒充正常值。
type MetricState string

const (
	MetricOK          MetricState = "ok"
	MetricUnavailable MetricState = "unavailable"
)

// Unavailable 描述一项未能采集到的数据。
type MissingMetric struct {
	Metric string `json:"metric"`
	Reason string `json:"reason"`
}

// OSInfo 系统版本信息。
type OSInfo struct {
	ProductVersion string `json:"productVersion"` // 例如 15.0.1
	Build          string `json:"build"`          // 例如 24A348
	Kernel         string `json:"kernel"`         // 例如 Darwin 24.0.0
	Architecture   string `json:"architecture"`
}

// CPUInfo CPU 概要。
type CPUInfo struct {
	Model         string   `json:"model"`
	PhysicalCores int      `json:"physicalCores"`
	LogicalCores  int      `json:"logicalCores"`
	LoadAverage   *LoadAvg `json:"loadAverage,omitempty"`
	UsagePercent  *float64 `json:"usagePercent,omitempty"` // 需要两次采样，M1 暂不提供
}

// LoadAvg 系统 1/5/15 分钟负载。
type LoadAvg struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

// MemoryInfo 内存概要，单位为字节。
type MemoryInfo struct {
	TotalBytes     int64       `json:"totalBytes"`
	UsedBytes      *int64      `json:"usedBytes,omitempty"`
	FreeBytes      *int64      `json:"freeBytes,omitempty"`
	AvailableBytes *int64      `json:"availableBytes,omitempty"`
	State          MetricState `json:"state"`
}

// DiskUsage 单个挂载点的容量。
type DiskUsage struct {
	MountPoint string  `json:"mountPoint"`
	FileSystem string  `json:"fileSystem"`
	TotalBytes int64   `json:"totalBytes"`
	FreeBytes  int64   `json:"freeBytes"`
	UsedBytes  int64   `json:"usedBytes"`
	UsedRatio  float64 `json:"usedRatio"`
}

// SystemOverview 系统概览。
type SystemOverview struct {
	Hostname      string          `json:"hostname"`
	OS            OSInfo          `json:"os"`
	CPU           CPUInfo         `json:"cpu"`
	Memory        MemoryInfo      `json:"memory"`
	Disks         []DiskUsage     `json:"disks"`
	BootTime      *time.Time      `json:"bootTime,omitempty"`
	UptimeSeconds *float64        `json:"uptimeSeconds,omitempty"`
	CollectedAt   time.Time       `json:"collectedAt"`
	Missing       []MissingMetric `json:"missing,omitempty"`
}

// AgentHealth Agent 自身健康状况。
type AgentHealth struct {
	Version     string    `json:"version"`
	StartedAt   time.Time `json:"startedAt"`
	PID         int       `json:"pid"`
	Platform    string    `json:"platform"`
	IsRoot      bool      `json:"isRoot"`
	MethodCount int       `json:"methodCount"`
	ServerClock time.Time `json:"serverClock"`
}

// AgentHealthResponse 是 agent.health 方法的完整返回结构。
// 单独定义为具名类型（而非匿名结构体），以便调用方与测试共用同一类型，
// 避免出现「字段相同但类型不同」导致断言失败的问题。
type AgentHealthResponse struct {
	AgentHealth
	Methods []string `json:"methods"`
}
