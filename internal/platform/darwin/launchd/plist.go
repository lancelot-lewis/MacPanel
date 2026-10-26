// Package launchd 生成与安装 launchd 任务（plist）。
//
// 用途分两类，共用同一套安全约束：
//   - 计划任务：把「每天 04:30 重启」交给 launchd 的 StartCalendarInterval，
//     这样即使 Agent 退出，调度依然存在（PLAN.md 5.3 的硬要求）；
//   - 受管服务：M4 的服务启停。
//
// 安全边界（三层，缺一不可）：
//  1. 本包强制 Label 前缀与 plist 目录，只碰自己的文件；
//  2. ProgramArguments 首个元素必须是本应用二进制，杜绝「任务变成任意命令执行器」；
//  3. execx 的 launchctlGuard 限制子命令与域。
package launchd

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// LabelPrefix 是本应用管理的所有 launchd 标签前缀。
//
// 强制前缀是「只操作自己的服务」的基础：没有它，一个错误的 label
// 就可能 bootout 掉系统关键服务。
const LabelPrefix = "com.homelab.manager."

// DaemonDir 是系统级 LaunchDaemon 的固定目录。
const DaemonDir = "/Library/LaunchDaemons"

// LogDir 是 launchd 任务的标准输出目录。
const LogDir = "/Library/Logs/HomeLabManager"

// CalendarInterval 对应 plist 里的 StartCalendarInterval 条目。
//
// 字段用指针表示「未指定」：launchd 的语义是「没写的字段表示每个都匹配」。
// 用零值会让「0 点」和「每小时」无法区分。
type CalendarInterval struct {
	Minute  *int
	Hour    *int
	Day     *int
	Weekday *int
	Month   *int
}

// Spec 描述一个待安装的 launchd 任务。
type Spec struct {
	// Label 必须带 LabelPrefix。
	Label string
	// ProgramArguments 首个元素必须是本应用二进制的绝对路径。
	ProgramArguments []string

	// StartCalendarInterval 与 StartInterval 二选一。
	StartCalendarInterval []CalendarInterval
	// StartIntervalSeconds 是固定间隔（秒）。
	StartIntervalSeconds int

	// RunAtLoad 必须为 false：为 true 时每次 bootstrap 都会执行一次，
	// 对重启任务意味着「装一次就重启一次」，是明确的安全隐患。
	RunAtLoad bool

	StandardOutPath   string
	StandardErrorPath string
	// WorkingDirectory 可选。
	WorkingDirectory string
}

// Validate 校验 Spec 的安全不变量。
func (s Spec) Validate() error {
	if err := ValidateLabel(s.Label); err != nil {
		return err
	}

	if len(s.ProgramArguments) == 0 {
		return fmt.Errorf("launchd: ProgramArguments 不能为空")
	}
	prog := s.ProgramArguments[0]
	if !strings.HasPrefix(prog, "/") {
		return fmt.Errorf("launchd: 可执行文件必须是绝对路径，实际 %q", prog)
	}
	// 必须是本应用二进制。这一条挡的是「改数据库就能让 root 执行任意程序」：
	// 即使有人能写入 scheduled_tasks，也无法让 launchd 执行 /bin/sh。
	if !strings.Contains(prog, "homelab-manager") {
		return fmt.Errorf("launchd: 可执行文件必须是 homelab-manager，实际 %q", prog)
	}

	if s.RunAtLoad {
		return fmt.Errorf("launchd: RunAtLoad 必须为 false（否则每次安装都会立即执行一次）")
	}

	if len(s.StartCalendarInterval) == 0 && s.StartIntervalSeconds <= 0 {
		return fmt.Errorf("launchd: 必须指定 StartCalendarInterval 或 StartInterval")
	}
	if len(s.StartCalendarInterval) > 0 && s.StartIntervalSeconds > 0 {
		return fmt.Errorf("launchd: StartCalendarInterval 与 StartInterval 不能同时指定")
	}
	if s.StartIntervalSeconds < 0 {
		return fmt.Errorf("launchd: StartInterval 不能为负")
	}
	// launchd 的最小间隔是 60 秒；更小会被系统拒绝或行为异常。
	if s.StartIntervalSeconds > 0 && s.StartIntervalSeconds < 60 {
		return fmt.Errorf("launchd: StartInterval 不能小于 60 秒，实际 %d", s.StartIntervalSeconds)
	}

	for _, ci := range s.StartCalendarInterval {
		if err := ci.validate(); err != nil {
			return err
		}
	}

	for _, p := range []struct{ name, val string }{
		{"StandardOutPath", s.StandardOutPath},
		{"StandardErrorPath", s.StandardErrorPath},
		{"WorkingDirectory", s.WorkingDirectory},
	} {
		if p.val == "" {
			continue
		}
		if !strings.HasPrefix(p.val, "/") {
			return fmt.Errorf("launchd: %s 必须是绝对路径，实际 %q", p.name, p.val)
		}
		if strings.Contains(p.val, "..") {
			return fmt.Errorf("launchd: %s 不能含 ..，实际 %q", p.name, p.val)
		}
	}
	return nil
}

// validate 校验日历字段的取值范围。
func (ci CalendarInterval) validate() error {
	type field struct {
		name string
		val  *int
		min  int
		max  int
	}
	for _, f := range []field{
		{"Minute", ci.Minute, 0, 59},
		{"Hour", ci.Hour, 0, 23},
		{"Day", ci.Day, 1, 31},
		{"Weekday", ci.Weekday, 0, 7},
		{"Month", ci.Month, 1, 12},
	} {
		if f.val == nil {
			continue
		}
		if *f.val < f.min || *f.val > f.max {
			return fmt.Errorf("launchd: %s 必须在 %d-%d 之间，实际 %d", f.name, f.min, f.max, *f.val)
		}
	}
	if ci.Minute == nil && ci.Hour == nil && ci.Day == nil && ci.Weekday == nil && ci.Month == nil {
		return fmt.Errorf("launchd: 日历条目不能全为空（会导致每分钟都执行）")
	}
	return nil
}

// PlistPath 返回该 Label 对应的 plist 路径。
func (s Spec) PlistPath() string {
	return DaemonDir + "/" + s.Label + ".plist"
}

// plistTemplate 是 plist 的模板。
//
// 刻意手写模板而不是用 encoding/xml 构造：plist 的 DTD 声明与
// 键值配对结构用 xml 库生成反而更易出错（xml 会自作主张添加命名空间）。
// 所有插入值都经过 xmlEscape。
var plistTemplate = template.Must(template.New("plist").Funcs(template.FuncMap{
	"xmlEscape": xmlEscape,
}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{xmlEscape .Label}}</string>
	<key>ProgramArguments</key>
	<array>
{{- range .ProgramArguments}}
		<string>{{xmlEscape .}}</string>
{{- end}}
	</array>
{{- if .StartIntervalSeconds}}
	<key>StartInterval</key>
	<integer>{{.StartIntervalSeconds}}</integer>
{{- end}}
{{- if .StartCalendarInterval}}
	<key>StartCalendarInterval</key>
	<array>
{{- range .StartCalendarInterval}}
		<dict>
{{- if .HasMonth}}
			<key>Month</key>
			<integer>{{.Month}}</integer>
{{- end}}
{{- if .HasDay}}
			<key>Day</key>
			<integer>{{.Day}}</integer>
{{- end}}
{{- if .HasWeekday}}
			<key>Weekday</key>
			<integer>{{.Weekday}}</integer>
{{- end}}
{{- if .HasHour}}
			<key>Hour</key>
			<integer>{{.Hour}}</integer>
{{- end}}
{{- if .HasMinute}}
			<key>Minute</key>
			<integer>{{.Minute}}</integer>
{{- end}}
		</dict>
{{- end}}
	</array>
{{- end}}
	<key>RunAtLoad</key>
	<false/>
{{- if .StandardOutPath}}
	<key>StandardOutPath</key>
	<string>{{xmlEscape .StandardOutPath}}</string>
{{- end}}
{{- if .StandardErrorPath}}
	<key>StandardErrorPath</key>
	<string>{{xmlEscape .StandardErrorPath}}</string>
{{- end}}
{{- if .WorkingDirectory}}
	<key>WorkingDirectory</key>
	<string>{{xmlEscape .WorkingDirectory}}</string>
{{- end}}
	<key>ProcessType</key>
	<string>Background</string>
</dict>
</plist>
`))

// renderData 是模板的输入。指针字段先解引用成值，
// 让模板的 {{if}} 能按「是否设置」判断（0 与未设置必须区分）。
type renderData struct {
	Label                 string
	ProgramArguments      []string
	StartIntervalSeconds  int
	StartCalendarInterval []calendarEntry
	StandardOutPath       string
	StandardErrorPath     string
	WorkingDirectory      string
}

// calendarEntry 把指针字段解引用成「值 + 是否设置」。
//
// 不能直接把 *int 塞进模板：text/template 打印指针会输出地址
// （0xc000...），生成的 plist 里就变成非法整数。
type calendarEntry struct {
	Month, Day, Weekday, Hour, Minute                int
	HasMonth, HasDay, HasWeekday, HasHour, HasMinute bool
}

// RenderPlist 生成 plist 内容。
//
// 纯函数：不碰文件系统，因此可以对生成结果做逐字断言。
func RenderPlist(s Spec) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}

	data := renderData{
		Label:                s.Label,
		ProgramArguments:     s.ProgramArguments,
		StartIntervalSeconds: s.StartIntervalSeconds,
		StandardOutPath:      s.StandardOutPath,
		StandardErrorPath:    s.StandardErrorPath,
		WorkingDirectory:     s.WorkingDirectory,
	}
	for _, ci := range s.StartCalendarInterval {
		e := calendarEntry{}
		if ci.Month != nil {
			e.Month, e.HasMonth = *ci.Month, true
		}
		if ci.Day != nil {
			e.Day, e.HasDay = *ci.Day, true
		}
		if ci.Weekday != nil {
			e.Weekday, e.HasWeekday = *ci.Weekday, true
		}
		if ci.Hour != nil {
			e.Hour, e.HasHour = *ci.Hour, true
		}
		if ci.Minute != nil {
			e.Minute, e.HasMinute = *ci.Minute, true
		}
		data.StartCalendarInterval = append(data.StartCalendarInterval, e)
	}

	var buf bytes.Buffer
	if err := plistTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("launchd: 渲染 plist 失败: %w", err)
	}
	return buf.Bytes(), nil
}

// xmlEscape 转义 XML 特殊字符。
//
// 为什么必须做：ProgramArguments 里会出现路径，而日志路径、
// 任务名都可能带 & 或 <。不转义会生成非法 XML，plutil 校验失败
// 还算是好的结果——更糟的是被截断解析，装上一个行为不对的任务。
func xmlEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// IntPtr 是构造字段指针的辅助函数。
func IntPtr(v int) *int { return &v }

// ValidateLabel 校验 launchd 标签的合法性。
//
// 抽成独立函数是因为 Installer 与 Spec.Validate 都要用它。
// 两处各写一份必然漂移——一处改了另一处忘改，就会留下
// 「Spec 拒绝但 Installer 放行」的绕过路径。
//
// 规则：
//   - 必须带本应用前缀（只操作自己的服务）；
//   - 只允许小写字母、数字、点、连字符（label 会被拼进路径与命令行参数，
//     限制字符集从根上排除路径穿越与参数注入）；
//   - 不以点或连字符开头/结尾，不含连续点（避免生成 "xxx..plist"
//     这类容易混淆的路径）。
func ValidateLabel(label string) error {
	if label == "" {
		return fmt.Errorf("launchd: Label 不能为空")
	}
	if !strings.HasPrefix(label, LabelPrefix) {
		return fmt.Errorf("launchd: Label 必须以 %q 开头，实际 %q", LabelPrefix, label)
	}
	for _, r := range label {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-'
		if !ok {
			return fmt.Errorf("launchd: Label 含非法字符 %q（只允许小写字母、数字、点和连字符）", r)
		}
	}
	if strings.HasSuffix(label, ".") || strings.HasSuffix(label, "-") {
		return fmt.Errorf("launchd: Label 不能以点或连字符结尾，实际 %q", label)
	}
	if strings.Contains(label, "..") {
		return fmt.Errorf("launchd: Label 不能含连续的点，实际 %q", label)
	}
	return nil
}
