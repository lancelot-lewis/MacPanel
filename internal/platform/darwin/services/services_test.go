package services

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// realPrintSample 是 `launchctl print system` 的真实输出片段（macOS 27）。
//
// 用真实片段而不是编造的格式：这类解析最容易出错的地方恰好是
// 「实际输出里有你没预料到的写法」（如 `(pe)` 标志、PID 为 0）。
const realPrintSample = `system = {
	type = system
	handle = 0
	active count = 1104
	service count = 438
	services = {
		       0      - 	com.apple.backgroundassets.managed.relay.service
		   58793   (pe) 	com.apple.lskdd
		       0   (pe) 	com.apple.kernelmanager_helper
		     184      - 	com.apple.runningboardd
		       0      0 	com.apple.wifiFirmwareLoader
		     331      - 	com.netease.uuremote.daemon
		     329      - 	ddns-go
		       0      - 	com.apple.ftp-proxy
	}
	disabled services = {
		"io.tailscale.ipn.macsys.login-item-helper" => enabled
	}
}`

// TestParsePrintServicesRealSample 验证真实输出能被正确解析。
func TestParsePrintServicesRealSample(t *testing.T) {
	got := ParsePrintServices(realPrintSample)

	if len(got) != 8 {
		t.Fatalf("应解析出 8 个服务，实际 %d：%+v", len(got), got)
	}

	byLabel := make(map[string]Service, len(got))
	for _, s := range got {
		byLabel[s.Label] = s
	}

	// PID 为正 → 运行中
	if s, ok := byLabel["com.apple.lskdd"]; !ok {
		t.Fatal("缺少 com.apple.lskdd")
	} else {
		if s.PID != 58793 || !s.Running {
			t.Errorf("com.apple.lskdd 应为运行中（PID 58793），实际 PID=%d running=%v", s.PID, s.Running)
		}
		if s.LastExit != "(pe)" {
			t.Errorf("应记录状态标志 (pe)，实际 %q", s.LastExit)
		}
	}

	// PID 为 0 → 未运行
	if s, ok := byLabel["com.apple.ftp-proxy"]; !ok {
		t.Fatal("缺少 com.apple.ftp-proxy")
	} else if s.Running || s.PID != 0 {
		t.Errorf("PID 0 应判定为未运行，实际 PID=%d running=%v", s.PID, s.Running)
	}

	// 第三方服务也要能识别
	if _, ok := byLabel["ddns-go"]; !ok {
		t.Error("缺少 ddns-go（第三方服务）")
	}
}

// TestParsePrintServicesIgnoresOutsideSegment 验证段外内容被忽略。
//
// disabled services 段在 services 之后，里面的 `"label" => enabled`
// 行不应被当成服务。
func TestParsePrintServicesIgnoresOutsideSegment(t *testing.T) {
	got := ParsePrintServices(realPrintSample)
	for _, s := range got {
		if strings.Contains(s.Label, "tailscale") {
			t.Errorf("disabled services 段的内容不应被解析为服务：%s", s.Label)
		}
	}
}

// TestParsePrintServicesHandlesEmpty 验证空输出不 panic。
func TestParsePrintServicesHandlesEmpty(t *testing.T) {
	for _, in := range []string{"", "system = {\n}", "no services here"} {
		if got := ParsePrintServices(in); len(got) != 0 {
			t.Errorf("输入 %q 应返回空列表，实际 %+v", in, got)
		}
	}
}

// TestParsePrintServicesSkipsMalformedLines 验证异常行被跳过而不是误判。
func TestParsePrintServicesSkipsMalformedLines(t *testing.T) {
	// 注意 no-dots-here 是**合法**标签（launchd 不要求含点，
	// 实际系统里就有 ddns-go 这样的服务）。
	in := `services = {
		not-a-number  -  com.example.foo
		1234  -  no-dots-here
		1234  -  ----
		1234  -  com.example.good
	}`
	got := ParsePrintServices(in)
	if len(got) != 2 {
		t.Fatalf("应解析出 2 个合法服务，实际 %d：%+v", len(got), got)
	}
	labels := []string{got[0].Label, got[1].Label}
	want := map[string]bool{"no-dots-here": true, "com.example.good": true}
	for _, l := range labels {
		if !want[l] {
			t.Errorf("出现不应保留的标签: %q", l)
		}
	}
}

// TestParsePrintServicesAcceptsLabelWithoutDot 验证无点标签被接受。
//
// 回归测试：早期实现要求标签必须含点，导致 `ddns-go` 这类
// 真实存在的服务在列表里凭空消失——用户看不到自己装的服务。
func TestParsePrintServicesAcceptsLabelWithoutDot(t *testing.T) {
	in := "services = {\n\t     329      - \tddns-go\n}"
	got := ParsePrintServices(in)
	if len(got) != 1 {
		t.Fatalf("应解析出 ddns-go，实际 %d 项", len(got))
	}
	if got[0].Label != "ddns-go" {
		t.Errorf("标签不符: %q", got[0].Label)
	}
}

// TestClassify 验证服务归属分类。
//
// 分类决定能否操作，所以三类边界都要测到。
func TestClassify(t *testing.T) {
	cases := map[string]Kind{
		"com.apple.Finder":            KindSystemCritical,
		"com.apple.launchd":           KindSystemCritical,
		"com.openssh.sshd":            KindSystemCritical, // Apple 提供的 sshd
		"com.homelab.manager.agent":   KindSelf,
		"com.homelab.manager.server":  KindSelf,
		"ddns-go":                     KindThirdParty,
		"com.netease.uuremote.daemon": KindThirdParty,
		"io.tailscale.ipn.macsys":     KindThirdParty,
		// 相似但不同的前缀不能被误判
		"com.applejuice.foo": KindThirdParty,
		// 空标签
		"": KindThirdParty,
	}
	for label, want := range cases {
		if got := Classify(label); got != want {
			t.Errorf("%q 应分类为 %s，实际 %s", label, want, got)
		}
	}
}

// TestValidateLabelForControlRejectsCritical 验证系统关键服务与自身被拒绝。
func TestValidateLabelForControlRejectsCritical(t *testing.T) {
	cases := []string{
		"com.apple.Finder",
		"com.openssh.sshd",
		"com.homelab.manager.agent",
		"com.homelab.manager.server",
	}
	for _, label := range cases {
		if err := ValidateLabelForControl(label); err == nil {
			t.Errorf("应拒绝操作 %q", label)
		}
	}

	// 第三方服务形态合法
	if err := ValidateLabelForControl("ddns-go"); err != nil {
		t.Errorf("第三方服务应通过形态校验，实际: %v", err)
	}

	// 非法形态
	for _, bad := range []string{"", "a/b", "a b", strings.Repeat("x", 300)} {
		if err := ValidateLabelForControl(bad); err == nil {
			t.Errorf("应拒绝非法标签 %q", bad)
		}
	}
}

// TestEnrichAssignsControllability 验证操作权限判定。
func TestEnrichAssignsControllability(t *testing.T) {
	list := []Service{
		{Label: "com.apple.Finder", Kind: KindSystemCritical},
		{Label: "com.homelab.manager.agent", Kind: KindSelf},
		{Label: "ddns-go", Kind: KindThirdParty},
		{Label: "com.example.managed", Kind: KindThirdParty},
	}

	// registered 含两个：一个已授权，一个只登记
	registered := map[string]bool{
		"com.example.managed":  true,
		"com.example.observed": true,
	}
	allowed := map[string]bool{"com.example.managed": true}
	got := Enrich(list, registered, allowed)

	if got[0].Controllable {
		t.Error("系统关键服务不应可操作")
	}
	if got[0].BlockedReason == "" {
		t.Error("应说明为何不可操作")
	}

	if got[1].Controllable {
		t.Error("本应用自身不应可操作")
	}

	if got[2].Controllable {
		t.Error("未登记的第三方服务不应可操作")
	}
	if !strings.Contains(got[2].BlockedReason, "未加入受管") {
		t.Errorf("应提示未加入受管，实际 %q", got[2].BlockedReason)
	}

	if !got[3].Controllable {
		t.Errorf("已登记的第三方服务应可操作，原因: %s", got[3].BlockedReason)
	}
	if got[3].BlockedReason != "" {
		t.Errorf("可操作时不应有拦截原因，实际 %q", got[3].BlockedReason)
	}

	// 原切片不应被修改（Enrich 返回副本）
	if list[3].Controllable {
		t.Error("不应修改入参切片")
	}
}

// TestEnrichDistinguishesRegisteredFromAuthorized 验证两种受管状态的区分。
//
// 回归测试：早期实现只用一个集合，导致「已登记但未授权」在界面上
// 显示成「未登记」，用户看不到「授权启停」入口。
func TestEnrichDistinguishesRegisteredFromAuthorized(t *testing.T) {
	list := []Service{
		{Label: "com.example.observed", Kind: KindThirdParty},
		{Label: "com.example.allowed", Kind: KindThirdParty},
	}
	registered := map[string]bool{
		"com.example.observed": true,
		"com.example.allowed":  true,
	}
	allowed := map[string]bool{"com.example.allowed": true}

	got := Enrich(list, registered, allowed)

	// 已登记未授权
	if !got[0].Managed {
		t.Error("已登记的服务应标记 managed")
	}
	if got[0].Controllable {
		t.Error("未授权的服务不应可操作")
	}
	if !strings.Contains(got[0].BlockedReason, "未授权") {
		t.Errorf("应提示未授权而不是未登记，实际 %q", got[0].BlockedReason)
	}

	// 已登记且已授权
	if !got[1].Managed || !got[1].Controllable {
		t.Errorf("已授权的服务应同时满足 managed 与 controllable: %+v", got[1])
	}
}

// TestSummarize 验证分类统计。
func TestSummarize(t *testing.T) {
	list := []Service{
		{Kind: KindSystemCritical, Running: true},
		{Kind: KindSystemCritical, Running: false},
		{Kind: KindSelf, Running: true},
		{Kind: KindThirdParty, Running: true, Managed: true},
		{Kind: KindThirdParty, Running: false},
	}
	got := Summarize(list)

	if got.Total != 5 || got.SystemCritical != 2 || got.Self != 1 || got.ThirdParty != 2 {
		t.Errorf("分类统计不符: %+v", got)
	}
	if got.Managed != 1 {
		t.Errorf("受管数应为 1，实际 %d", got.Managed)
	}
	if got.Running != 3 {
		t.Errorf("运行中应为 3，实际 %d", got.Running)
	}
}

// ---- plist 解析 ----

const samplePlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.example.demo</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/demo</string>
		<string>--serve</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>StandardOutPath</key>
	<string>/var/log/demo.log</string>
	<key>StandardErrorPath</key>
	<string>/var/log/demo.err.log</string>
	<key>StartCalendarInterval</key>
	<dict>
		<key>Hour</key>
		<integer>4</integer>
		<key>Minute</key>
		<integer>30</integer>
	</dict>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/usr/bin:/bin</string>
	</dict>
</dict>
</plist>`

// TestParsePlist 验证常用字段提取。
func TestParsePlist(t *testing.T) {
	info, err := ParsePlist([]byte(samplePlist))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if info.Label != "com.example.demo" {
		t.Errorf("Label 不符: %q", info.Label)
	}
	if len(info.ProgramArguments) != 2 || info.ProgramArguments[0] != "/usr/local/bin/demo" {
		t.Errorf("ProgramArguments 不符: %v", info.ProgramArguments)
	}
	if !info.RunAtLoad {
		t.Error("RunAtLoad 应为 true")
	}
	// KeepAlive 为 dict 时表示「按条件保活」，等价于开启
	if !info.KeepAlive {
		t.Error("KeepAlive 为 dict 时应判定为开启")
	}
	if info.StandardOutPath != "/var/log/demo.log" {
		t.Errorf("StandardOutPath 不符: %q", info.StandardOutPath)
	}
	if info.StandardErrorPath != "/var/log/demo.err.log" {
		t.Errorf("StandardErrorPath 不符: %q", info.StandardErrorPath)
	}
}

// TestParsePlistSkipsNestedStructures 验证嵌套结构不会导致解析失败。
//
// StartCalendarInterval 与 EnvironmentVariables 都是嵌套 dict，
// 解析器必须能跳过而不是误把它们当作顶层键。
func TestParsePlistSkipsNestedStructures(t *testing.T) {
	info, err := ParsePlist([]byte(samplePlist))
	if err != nil {
		t.Fatalf("嵌套结构不应导致失败: %v", err)
	}
	// 跳过后仍应正确读到后续字段（StandardOutPath 在嵌套之后）
	if info.StandardOutPath == "" {
		t.Error("跳过嵌套结构后应能继续解析后续字段")
	}
}

// TestParsePlistProgramFallback 验证只有 Program 时也能取到可执行文件。
func TestParsePlistProgramFallback(t *testing.T) {
	plist := `<plist version="1.0"><dict>
		<key>Label</key><string>x</string>
		<key>Program</key><string>/usr/bin/x</string>
	</dict></plist>`

	info, err := ParsePlist([]byte(plist))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got := info.Executable(); got != "/usr/bin/x" {
		t.Errorf("应回落到 Program，实际 %q", got)
	}
}

// TestParsePlistProgramArgumentsPriority 验证两者都有时 ProgramArguments 优先。
//
// launchd 的语义是「有 ProgramArguments 就用它」，展示时也要给出真正会跑的那个。
func TestParsePlistProgramArgumentsPriority(t *testing.T) {
	plist := `<plist version="1.0"><dict>
		<key>Label</key><string>x</string>
		<key>Program</key><string>/usr/bin/ignored</string>
		<key>ProgramArguments</key><array><string>/usr/bin/actual</string></array>
	</dict></plist>`

	info, err := ParsePlist([]byte(plist))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got := info.Executable(); got != "/usr/bin/actual" {
		t.Errorf("ProgramArguments 应优先，实际 %q", got)
	}
}

// TestParsePlistRejectsMalformed 验证非法输入被拒绝而不是返回半截数据。
func TestParsePlistRejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"not xml at all",
		`<plist version="1.0"><dict><key>Label</key>`, // 未闭合
		`<plist version="1.0"></plist>`,               // 没有 dict
	}
	for _, in := range cases {
		if _, err := ParsePlist([]byte(in)); err == nil {
			t.Errorf("应拒绝非法输入: %q", in)
		}
	}
}

// TestLogPaths 验证日志路径提取与去重。
func TestLogPaths(t *testing.T) {
	info := PlistInfo{
		StandardOutPath:   "/var/log/a.log",
		StandardErrorPath: "/var/log/a.log",
	}
	got := LogPaths(info)
	if len(got) != 1 {
		t.Errorf("相同路径应去重，实际 %v", got)
	}

	// 都没声明时返回空——不猜路径，否则用户会去找不存在的文件
	if got := LogPaths(PlistInfo{}); len(got) != 0 {
		t.Errorf("未声明时应返回空，实际 %v", got)
	}
}

// ---- 控制器 ----

// fakeRunner 记录 launchctl 调用并可注入失败。
type fakeRunner struct {
	calls  [][]string
	failOn string // 该子命令返回错误
	loaded map[string]bool
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{loaded: map[string]bool{}}
}

func (f *fakeRunner) run(ctx context.Context, args ...string) error {
	f.calls = append(f.calls, args)
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "print":
		if len(args) >= 2 && f.loaded[strings.TrimPrefix(args[1], "system/")] {
			return nil
		}
		return errors.New("service not found")
	case "kickstart", "bootout":
		if f.failOn == args[0] {
			return errors.New("模拟失败")
		}
		if len(args) >= 3 {
			f.loaded[strings.TrimPrefix(args[2], "system/")] = args[0] == "kickstart"
		}
		return nil
	case "bootstrap":
		if f.failOn == "bootstrap" {
			return errors.New("模拟失败")
		}
		if len(args) >= 3 {
			label := strings.TrimSuffix(args[3-1+1-1], ".plist") // 见下方说明
			_ = label
		}
		return nil
	}
	return nil
}

func (f *fakeRunner) hasCall(sub string) bool {
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == sub {
			return true
		}
	}
	return false
}

func (f *fakeRunner) countCalls(sub string) int {
	n := 0
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == sub {
			n++
		}
	}
	return n
}

// TestControllerRejectsCriticalServices 验证控制器自身也会拦截关键服务。
//
// 这是第二道门槛：即使白名单里误加了 com.apple.finder，
// 这里也必须拒绝。两层独立校验的价值就在于任一处被绕过仍有兜底。
func TestControllerRejectsCriticalServices(t *testing.T) {
	runner := newFakeRunner()
	c := NewControllerForTest(runner.run, func(string) error { return nil })

	cases := []struct {
		label  string
		action Action
	}{
		{"com.apple.Finder", ActionStop},
		{"com.openssh.sshd", ActionStop},
		{"com.homelab.manager.agent", ActionStop},
		{"com.homelab.manager.server", ActionRestart},
	}
	for _, tc := range cases {
		err := c.Execute(context.Background(), tc.label, tc.action, "")
		if err == nil {
			t.Errorf("应拒绝 %s %s", tc.action, tc.label)
		}
	}
	if len(runner.calls) != 0 {
		t.Errorf("被拒绝的请求不应触达 launchctl，实际调用: %v", runner.calls)
	}
}

// TestControllerRestartUsesKickstart 验证重启用 kickstart -k。
func TestControllerRestartUsesKickstart(t *testing.T) {
	runner := newFakeRunner()
	c := NewControllerForTest(runner.run, func(string) error { return nil })

	if err := c.Execute(context.Background(), "ddns-go", ActionRestart, ""); err != nil {
		t.Fatalf("重启失败: %v", err)
	}

	var found []string
	for _, call := range runner.calls {
		if call[0] == "kickstart" {
			found = call
		}
	}
	if len(found) == 0 {
		t.Fatal("应调用 kickstart")
	}
	// 必须带 -k（先终止再启动），否则对已运行的服务只是「确保运行」
	joined := strings.Join(found, " ")
	if !strings.Contains(joined, "-k") {
		t.Errorf("kickstart 必须带 -k，实际: %s", joined)
	}
	if !strings.Contains(joined, "system/ddns-go") {
		t.Errorf("目标应为 system/ddns-go，实际: %s", joined)
	}
}

// TestControllerStartIsIdempotent 验证已加载时启动是空操作。
func TestControllerStartIsIdempotent(t *testing.T) {
	runner := newFakeRunner()
	runner.loaded["ddns-go"] = true // 已加载
	c := NewControllerForTest(runner.run, func(string) error { return nil })

	if err := c.Execute(context.Background(), "ddns-go", ActionStart, "/Library/LaunchDaemons/ddns-go.plist"); err != nil {
		t.Fatalf("启动应成功: %v", err)
	}
	if runner.hasCall("bootstrap") {
		t.Error("已加载时不应重复 bootstrap")
	}
}

// TestControllerStartRequiresPlist 验证启动需要有效的 plist 路径。
func TestControllerStartRequiresPlist(t *testing.T) {
	runner := newFakeRunner()
	c := NewControllerForTest(runner.run, func(string) error { return nil })

	// 没有 plist 路径
	if err := c.Execute(context.Background(), "ddns-go", ActionStart, ""); err == nil {
		t.Error("缺少 plist 路径时应拒绝启动")
	}

	// plist 不存在
	c2 := NewControllerForTest(runner.run, func(string) error { return errors.New("not found") })
	err := c2.Execute(context.Background(), "ddns-go", ActionStart, "/Library/LaunchDaemons/ddns-go.plist")
	if err == nil {
		t.Error("plist 不存在时应拒绝启动")
	}
	if !strings.Contains(err.Error(), "plist 不存在") {
		t.Errorf("错误信息应说明原因，实际: %v", err)
	}
}

// TestControllerStopIsIdempotent 验证未加载时停止是空操作。
func TestControllerStopIsIdempotent(t *testing.T) {
	runner := newFakeRunner() // 未加载
	c := NewControllerForTest(runner.run, func(string) error { return nil })

	if err := c.Execute(context.Background(), "ddns-go", ActionStop, ""); err != nil {
		t.Fatalf("停止应成功（幂等）: %v", err)
	}
	if runner.hasCall("bootout") {
		t.Error("未加载时不应调用 bootout")
	}
}

// TestControllerRejectsUnknownAction 验证未知操作被拒绝。
func TestControllerRejectsUnknownAction(t *testing.T) {
	runner := newFakeRunner()
	c := NewControllerForTest(runner.run, func(string) error { return nil })

	if err := c.Execute(context.Background(), "ddns-go", Action("delete"), ""); err == nil {
		t.Error("应拒绝未知操作")
	}
	if len(runner.calls) != 0 {
		t.Error("未知操作不应触达 launchctl")
	}
}

// TestScannerListReportsWarnings 验证单个域失败不导致整体失败。
func TestScannerListReportsWarnings(t *testing.T) {
	scanner := NewScannerForTest(
		func(ctx context.Context, domain string) (string, error) {
			if domain == "system" {
				return "", errors.New("permission denied")
			}
			return "services = {\n\t 100 - \tcom.example.gui\n}", nil
		},
		func(string) ([]byte, error) { return nil, errors.New("not found") },
		func(string) error { return errors.New("not found") },
		func() int { return 501 },
	)

	list, warnings, err := scanner.List(context.Background(), ListOptions{IncludeGUI: true, GUIUID: 501})
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if len(warnings) != 1 {
		t.Errorf("应报告 1 条警告，实际 %d: %v", len(warnings), warnings)
	}
	if len(list) != 1 || list[0].Label != "com.example.gui" {
		t.Errorf("gui 域应仍被读取，实际: %+v", list)
	}
}

// TestScannerListSortsStable 验证排序稳定（system 在前，按标签升序）。
func TestScannerListSortsStable(t *testing.T) {
	scanner := NewScannerForTest(
		func(ctx context.Context, domain string) (string, error) {
			if domain == "system" {
				return "services = {\n\t 1 - \tzzz.service\n\t 2 - \taaa.service\n}", nil
			}
			return "services = {\n\t 3 - \tcom.example.gui\n}", nil
		},
		func(string) ([]byte, error) { return nil, errors.New("not found") },
		func(string) error { return errors.New("not found") },
		func() int { return 501 },
	)

	list, _, err := scanner.List(context.Background(), ListOptions{IncludeGUI: true, GUIUID: 501})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("应有 3 项，实际 %d", len(list))
	}
	if list[0].Label != "aaa.service" || list[1].Label != "zzz.service" {
		t.Errorf("system 域应排在前且按标签升序，实际: %s, %s", list[0].Label, list[1].Label)
	}
	if list[2].Domain != DomainGUI {
		t.Errorf("gui 域应排在后面，实际: %s", list[2].Domain)
	}
}

// TestScannerDetailMissingPlist 验证找不到 plist 时返回空而不是报错。
//
// 服务可能来自内嵌 plist 或文件已被删除但仍加载——
// 这种情况本身是有价值的诊断信息。
func TestScannerDetailMissingPlist(t *testing.T) {
	scanner := NewScannerForTest(
		func(context.Context, string) (string, error) { return "", nil },
		func(string) ([]byte, error) { return nil, errors.New("not found") },
		func(string) error { return errors.New("not found") },
		func() int { return 501 },
	)

	info, path, err := scanner.Detail(context.Background(), "ddns-go", DomainSystem)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if path != "" {
		t.Errorf("应返回空路径，实际 %q", path)
	}
	if info.Label != "" {
		t.Errorf("应返回空信息，实际 %+v", info)
	}
}
