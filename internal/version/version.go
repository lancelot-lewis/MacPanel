// Package version 暴露构建期注入的版本信息。
package version

import (
	"fmt"
	"runtime"
)

// 这些变量通过 -ldflags "-X ..." 在构建时注入，默认值用于本地 go run。
var (
	// Version 语义化版本号。
	Version = "0.1.0-dev"
	// Commit 构建对应的 git 提交短哈希。
	Commit = "unknown"
	// BuildTime 构建时间（RFC3339）。
	BuildTime = "unknown"
)

// Info 汇总二进制与运行时环境信息。
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"buildTime"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

// Current 返回当前二进制的版本信息。
func Current() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
		GoVersion: runtime.Version(),
		Platform:  fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}
}

// String 便于 CLI 输出。
func (i Info) String() string {
	return fmt.Sprintf("homelab-manager %s (commit %s, built %s, %s)", i.Version, i.Commit, i.BuildTime, i.Platform)
}
