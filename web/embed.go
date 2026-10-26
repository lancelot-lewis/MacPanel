// Package web 负责把构建好的前端产物内嵌进二进制。
//
// 构建顺序：npm run build（产物在 web/dist/）→ go build（embed 打包）。
// 若 dist 尚未生成，DistFS 返回错误，Server 会降级为「仅 API 可用」并输出提示，
// 而不是启动失败——这样在后端开发阶段也能直接 go run。
package web

import (
	"embed"
	"errors"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// ErrNotBuilt 表示前端产物不存在。
var ErrNotBuilt = errors.New("web: 前端未构建（缺少 web/dist）")

// DistFS 返回以 dist 为根的文件系统。
func DistFS() (fs.FS, error) {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil, ErrNotBuilt
	}
	// 确认 index.html 存在：仅有一个空目录也视为未构建。
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, ErrNotBuilt
	}
	return sub, nil
}
