package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// FileValues 是 server.conf 支持的配置项，零值表示「未配置」。
//
// 为什么需要配置文件，而不是只用环境变量：
// 监听地址属于「用户在面板之外设定、且必须在 pkg 升级后保留」的配置。
// launchd plist 由 postinstall 每次按模板重写，手改必丢；而 launchd
// 启动的进程没有交互式 shell，环境变量也无处可改。
//
// 优先级：环境变量 > 配置文件 > 内置默认值。
type FileValues struct {
	Listen string
	// AllowRemote 用指针区分「未配置」与「显式 false」。
	AllowRemote *bool
	// PublicHosts 为 nil 表示未配置；空切片表示显式清空。
	PublicHosts []string
}

// ParseConfigFile 读取 server.conf。
//
// 文件不存在返回零值且不报错：「只有默认配置」是完全合法的状态。
// 但语法错误与未知配置项必须报错——静默忽略拼错的键，会让用户以为
// 配置生效了，实际还在跑默认值。
func ParseConfigFile(path string) (FileValues, error) {
	var v FileValues
	if path == "" {
		return v, nil
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return v, nil
		}
		return v, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		key, val, ok := strings.Cut(raw, "=")
		if !ok {
			return v, fmt.Errorf("%s 第 %d 行缺少 '=': %q", path, line, raw)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)

		switch key {
		case "listen":
			if val == "" {
				return v, fmt.Errorf("%s 第 %d 行 listen 为空", path, line)
			}
			v.Listen = val
		case "allow_remote":
			b, err := strconv.ParseBool(val)
			if err != nil {
				return v, fmt.Errorf("%s 第 %d 行 allow_remote 不是合法布尔值: %q", path, line, val)
			}
			v.AllowRemote = &b
		case "public_hosts":
			hosts := splitAndTrim(val)
			if hosts == nil {
				hosts = []string{}
			}
			v.PublicHosts = hosts
		default:
			return v, fmt.Errorf("%s 第 %d 行未知配置项 %q（支持 listen / allow_remote / public_hosts）",
				path, line, key)
		}
	}
	if err := sc.Err(); err != nil {
		return v, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}
	return v, nil
}

// ServerConfHeader 是写入配置文件时的说明头。
const ServerConfHeader = `# MacPanel server 配置
#
# 优先级：环境变量 > 本文件 > 内置默认值。
# 本文件由 macpanel config set 维护；手工修改同样有效，但会被 config set 重写。
#
# listen         监听地址，默认 127.0.0.1:7788。要让局域网访问改成 0.0.0.0:7788。
# allow_remote   是否允许非回环访问。绑非回环地址时必须为 true。
#                开启后所有访问（含本机）都要求登录，因此必须先设置管理密码。
# public_hosts   反向代理场景额外允许的 Host（含端口，逗号分隔）。
#
# 修改后需重启服务生效：
#   sudo launchctl kickstart -k system/com.macpanel.server
`

// WriteConfigFile 以 0640 原子写入配置文件。
//
// 权限 0640 root:_macpanel：Server 以 _macpanel 运行，只读即可；
// 属组的修正在调用方完成（需要 root，且账户是安装时创建的）。
func WriteConfigFile(path string, v FileValues) error {
	if path == "" {
		return fmt.Errorf("未指定配置文件路径")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("创建配置目录 %s 失败: %w", dir, err)
	}

	var b strings.Builder
	b.WriteString(ServerConfHeader)
	b.WriteString("\n")
	if v.Listen != "" {
		fmt.Fprintf(&b, "listen = %s\n", v.Listen)
	}
	if v.AllowRemote != nil {
		fmt.Fprintf(&b, "allow_remote = %t\n", *v.AllowRemote)
	}
	if v.PublicHosts != nil {
		fmt.Fprintf(&b, "public_hosts = %s\n", strings.Join(v.PublicHosts, ","))
	}

	tmp, err := os.CreateTemp(dir, ".server.conf-*")
	if err != nil {
		return fmt.Errorf("写入配置文件失败（目录 %s 不可写，通常需要 sudo）: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后是空操作

	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return fmt.Errorf("写入配置文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("写入配置文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		return fmt.Errorf("设置配置文件权限失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换配置文件 %s 失败: %w", path, err)
	}
	return nil
}
