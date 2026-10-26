package launchd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner 记录 launchctl 调用并可注入失败。
type fakeRunner struct {
	calls         [][]string
	loaded        map[string]bool
	failBootstrap bool
	failBootout   bool
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{loaded: map[string]bool{}}
}

func (f *fakeRunner) Run(ctx context.Context, args ...string) error {
	f.calls = append(f.calls, args)
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "bootstrap":
		if f.failBootstrap {
			return errors.New("模拟 bootstrap 失败")
		}
		if len(args) >= 2 {
			// bootstrap system <plist> → 标记为已加载
			label := strings.TrimSuffix(filepath.Base(args[len(args)-1]), ".plist")
			f.loaded[label] = true
		}
	case "bootout":
		if f.failBootout {
			return errors.New("模拟 bootout 失败")
		}
		if len(args) >= 2 {
			f.loaded[strings.TrimPrefix(args[1], "system/")] = false
		}
	}
	return nil
}

func (f *fakeRunner) Output(ctx context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if len(args) >= 2 && args[0] == "print" {
		label := strings.TrimPrefix(args[1], "system/")
		if f.loaded[label] {
			return "loaded", nil
		}
		return "", errors.New("service not found")
	}
	return "", nil
}

func (f *fakeRunner) hasCall(sub string) bool {
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == sub {
			return true
		}
	}
	return false
}

// sampleSpec 构造每日 04:30 的任务。
func sampleSpec() Spec {
	return Spec{
		Label:            LabelPrefix + "task.7",
		ProgramArguments: []string{"/usr/local/bin/homelab-manager", "task-run", "--task", "7"},
		StartCalendarInterval: []CalendarInterval{
			{Hour: IntPtr(4), Minute: IntPtr(30)},
		},
		StandardOutPath:   LogDir + "/task-7.log",
		StandardErrorPath: LogDir + "/task-7.err.log",
	}
}

// TestRenderPlistDaily 验证每日任务的 plist 结构。
func TestRenderPlistDaily(t *testing.T) {
	data, err := RenderPlist(sampleSpec())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	s := string(data)

	checks := []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		`<key>Label</key>`,
		`<string>com.homelab.manager.task.7</string>`,
		`<key>ProgramArguments</key>`,
		`<string>task-run</string>`,
		`<string>--task</string>`,
		`<key>StartCalendarInterval</key>`,
		`<key>Hour</key>`,
		`<integer>4</integer>`,
		`<key>Minute</key>`,
		`<integer>30</integer>`,
		// RunAtLoad 必须显式为 false
		`<key>RunAtLoad</key>`,
		`<false/>`,
		`<key>StandardOutPath</key>`,
		`<key>ProcessType</key>`,
		`<string>Background</string>`,
	}
	for _, want := range checks {
		if !strings.Contains(s, want) {
			t.Errorf("plist 缺少 %q", want)
		}
	}

	// 不应出现 StartInterval（与日历互斥）
	if strings.Contains(s, "<key>StartInterval</key>") {
		t.Error("日历模式下不应生成 StartInterval")
	}
}

// TestRenderPlistWeeklyMultipleEntries 验证每周多天生成多个日历条目。
func TestRenderPlistWeeklyMultipleEntries(t *testing.T) {
	spec := sampleSpec()
	spec.StartCalendarInterval = []CalendarInterval{
		{Weekday: IntPtr(1), Hour: IntPtr(3), Minute: IntPtr(0)},
		{Weekday: IntPtr(4), Hour: IntPtr(3), Minute: IntPtr(0)},
	}
	data, err := RenderPlist(spec)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	s := string(data)

	if n := strings.Count(s, "<key>Weekday</key>"); n != 2 {
		t.Errorf("应生成 2 个 Weekday 条目，实际 %d", n)
	}
	// 0 点必须写成 0 而不是被省略
	if !strings.Contains(s, "<key>Hour</key>\n\t\t\t<integer>3</integer>") {
		t.Errorf("Hour 应为 3，实际内容:\n%s", s)
	}
	if !strings.Contains(s, "<integer>0</integer>") {
		t.Error("Minute=0 必须显式写出，不能被省略")
	}
}

// TestRenderPlistInterval 验证固定间隔模式。
func TestRenderPlistInterval(t *testing.T) {
	spec := sampleSpec()
	spec.StartCalendarInterval = nil
	spec.StartIntervalSeconds = 3600

	data, err := RenderPlist(spec)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	s := string(data)

	if !strings.Contains(s, "<key>StartInterval</key>") {
		t.Error("应生成 StartInterval")
	}
	if !strings.Contains(s, "<integer>3600</integer>") {
		t.Error("间隔值应为 3600")
	}
	if strings.Contains(s, "StartCalendarInterval") {
		t.Error("间隔模式下不应生成日历条目")
	}
}

// TestRenderPlistEscapesXMLEntities 验证 XML 特殊字符被转义。
//
// 不转义会生成非法 XML：plutil 校验失败还算好的结果，
// 更糟的是被截断解析后装上一个行为不对的任务。
func TestRenderPlistEscapesXMLEntities(t *testing.T) {
	spec := sampleSpec()
	spec.ProgramArguments = []string{
		"/usr/local/bin/homelab-manager",
		"task-run",
		"--note", "a<b&c>d\"e'f",
	}

	data, err := RenderPlist(spec)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	s := string(data)

	if !strings.Contains(s, "a&lt;b&amp;c&gt;d&quot;e&apos;f") {
		t.Errorf("特殊字符未正确转义，内容:\n%s", s)
	}
	// 原始未转义形式不应出现（否则 XML 结构会被破坏）
	if strings.Contains(s, "a<b&c>d") {
		t.Error("存在未转义的原始字符")
	}
}

// TestValidateRejectsForeignLabel 验证只接受本应用的 label。
//
// 这是「只操作自己的服务」的基础：没有它，一个错误 label
// 就可能 bootout 掉系统关键服务。
func TestValidateRejectsForeignLabel(t *testing.T) {
	cases := []string{
		"",
		"com.apple.something",
		"com.example.mine",
		"homelab.manager.task.1",
		"com.homelab.manager.Task.1", // 大写
		"com.homelab.manager...",     // 非法字符
		"com.homelab.manager.a/b",    // 路径分隔符
	}

	for _, label := range cases {
		t.Run(label, func(t *testing.T) {
			spec := sampleSpec()
			spec.Label = label
			if err := spec.Validate(); err == nil {
				t.Errorf("应拒绝 label %q", label)
			}
		})
	}
}

// TestValidateRejectsRunAtLoad 验证 RunAtLoad 必须为 false。
//
// RunAtLoad=true 时每次 bootstrap 都会立即执行一次。对重启任务
// 意味着「装一次就重启一次」，是明确的安全隐患。
func TestValidateRejectsRunAtLoad(t *testing.T) {
	spec := sampleSpec()
	spec.RunAtLoad = true
	if err := spec.Validate(); err == nil {
		t.Error("应拒绝 RunAtLoad=true")
	}
}

// TestValidateRejectsForeignProgram 验证可执行文件必须是本应用。
//
// 这一条挡的是「改数据库就能让 root 执行任意程序」：
// 即使有人能写入 scheduled_tasks，也无法让 launchd 执行 /bin/sh。
func TestValidateRejectsForeignProgram(t *testing.T) {
	for _, prog := range []string{
		"/bin/sh",
		"/usr/bin/curl",
		"sh", // 非绝对路径
		"",   // 空
	} {
		spec := sampleSpec()
		spec.ProgramArguments = []string{prog}
		if err := spec.Validate(); err == nil {
			t.Errorf("应拒绝程序 %q", prog)
		}
	}
}

// TestValidateRejectsBadSchedule 验证调度字段的互斥与边界。
func TestValidateRejectsBadSchedule(t *testing.T) {
	spec := sampleSpec()
	spec.StartIntervalSeconds = 60 // 同时指定两种
	if err := spec.Validate(); err == nil {
		t.Error("不能同时指定日历与间隔")
	}

	noSchedule := sampleSpec()
	noSchedule.StartCalendarInterval = nil
	if err := noSchedule.Validate(); err == nil {
		t.Error("必须指定一种调度方式")
	}

	tooShort := sampleSpec()
	tooShort.StartCalendarInterval = nil
	tooShort.StartIntervalSeconds = 30
	if err := tooShort.Validate(); err == nil {
		t.Error("间隔小于 60 秒应被拒绝")
	}

	emptyEntry := sampleSpec()
	emptyEntry.StartCalendarInterval = []CalendarInterval{{}}
	if err := emptyEntry.Validate(); err == nil {
		t.Error("空日历条目应被拒绝（会导致每分钟执行）")
	}

	badHour := sampleSpec()
	badHour.StartCalendarInterval = []CalendarInterval{{Hour: IntPtr(24)}}
	if err := badHour.Validate(); err == nil {
		t.Error("小时越界应被拒绝")
	}
}

// TestPlistPathGuard 验证路径计算不会越界。
func TestPlistPathGuard(t *testing.T) {
	inst := NewInstallerWithoutValidation(t.TempDir(), newFakeRunner())

	if _, err := inst.plistPath("com.apple.finder"); err == nil {
		t.Error("应拒绝非本应用 label")
	}
	if _, err := inst.plistPath(LabelPrefix + "a/../../etc/passwd"); err == nil {
		t.Error("应拒绝路径穿越")
	}

	// 正常 label 的路径应在目录内
	p, err := inst.plistPath(LabelPrefix + "task.1")
	if err != nil {
		t.Fatalf("正常 label 应通过: %v", err)
	}
	if !strings.HasPrefix(p, inst.Dir()) {
		t.Errorf("路径应在安装目录内: %s", p)
	}
}

// TestInstallWritesAndBootstraps 验证安装写入文件并加载。
func TestInstallWritesAndBootstraps(t *testing.T) {
	dir := t.TempDir()
	runner := newFakeRunner()
	inst := NewInstallerWithoutValidation(dir, runner)

	if err := inst.Install(context.Background(), sampleSpec()); err != nil {
		t.Fatalf("安装失败: %v", err)
	}

	// 文件已写入
	path := filepath.Join(dir, sampleSpec().Label+".plist")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 plist 失败: %v", err)
	}
	if !strings.Contains(string(data), "com.homelab.manager.task.7") {
		t.Error("文件内容不对")
	}

	// 权限应为 0644（launchd 要求 plist 可被 root 读取）
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o644 {
		t.Errorf("权限应为 0644，实际 %o", mode)
	}

	// 调用了 bootstrap
	if !runner.hasCall("bootstrap") {
		t.Error("应调用 launchctl bootstrap")
	}
	if !runner.loaded[sampleSpec().Label] {
		t.Error("任务应标记为已加载")
	}
}

// TestInstallRollsBackOnBootstrapFailure 验证加载失败时删除 plist。
//
// 这是必要的回滚：若留下孤儿 plist，下次开机会被 launchd 自动加载，
// 变成「安装报错但实际生效」——这种状态最难排查。
func TestInstallRollsBackOnBootstrapFailure(t *testing.T) {
	dir := t.TempDir()
	runner := newFakeRunner()
	runner.failBootstrap = true
	inst := NewInstallerWithoutValidation(dir, runner)

	err := inst.Install(context.Background(), sampleSpec())
	if err == nil {
		t.Fatal("bootstrap 失败时安装应报错")
	}

	path := filepath.Join(dir, sampleSpec().Label+".plist")
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("加载失败后应删除 plist，避免开机被自动加载")
	}
}

// TestInstallReplacesLoadedTask 验证重复安装会先卸载。
//
// 不先 bootout 的话，bootstrap 会因 "service already loaded" 失败。
func TestInstallReplacesLoadedTask(t *testing.T) {
	dir := t.TempDir()
	runner := newFakeRunner()
	inst := NewInstallerWithoutValidation(dir, runner)

	if err := inst.Install(context.Background(), sampleSpec()); err != nil {
		t.Fatalf("首次安装失败: %v", err)
	}
	runner.calls = nil

	if err := inst.Install(context.Background(), sampleSpec()); err != nil {
		t.Fatalf("重复安装失败: %v", err)
	}
	if !runner.hasCall("bootout") {
		t.Error("重复安装应先 bootout")
	}
}

// TestRemoveDeletesFileAndUnloads 验证卸载。
func TestRemoveDeletesFileAndUnloads(t *testing.T) {
	dir := t.TempDir()
	runner := newFakeRunner()
	inst := NewInstallerWithoutValidation(dir, runner)

	if err := inst.Install(context.Background(), sampleSpec()); err != nil {
		t.Fatalf("安装失败: %v", err)
	}
	if err := inst.Remove(context.Background(), sampleSpec().Label); err != nil {
		t.Fatalf("卸载失败: %v", err)
	}

	path := filepath.Join(dir, sampleSpec().Label+".plist")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("plist 应已删除")
	}
	if runner.loaded[sampleSpec().Label] {
		t.Error("任务应已卸载")
	}
}

// TestRemoveNonexistentIsIdempotent 验证卸载不存在的任务不报错。
func TestRemoveNonexistentIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	runner := newFakeRunner()
	runner.failBootout = true // 未安装时 bootout 会失败
	inst := NewInstallerWithoutValidation(dir, runner)

	if err := inst.Remove(context.Background(), LabelPrefix+"task.99"); err != nil {
		t.Errorf("卸载不存在的任务不应报错，实际: %v", err)
	}
}

// TestRemoveWarnsWhenBootoutFails 验证文件删了但卸载失败时明确报错。
//
// 这种情况服务可能仍在运行，假装成功会让用户以为已经停掉了。
func TestRemoveWarnsWhenBootoutFails(t *testing.T) {
	dir := t.TempDir()
	runner := newFakeRunner()
	inst := NewInstallerWithoutValidation(dir, runner)

	if err := inst.Install(context.Background(), sampleSpec()); err != nil {
		t.Fatalf("安装失败: %v", err)
	}
	runner.failBootout = true

	err := inst.Remove(context.Background(), sampleSpec().Label)
	if err == nil {
		t.Error("卸载失败时应报错，而不是假装成功")
	}
	if !strings.Contains(err.Error(), "plist 已删除") {
		t.Errorf("错误信息应说明文件已删除，实际: %v", err)
	}
}

// TestStateReportsFileAndLoaded 验证状态分别报告文件与加载状态。
func TestStateReportsFileAndLoaded(t *testing.T) {
	dir := t.TempDir()
	runner := newFakeRunner()
	inst := NewInstallerWithoutValidation(dir, runner)
	ctx := context.Background()

	// 初始：都没有
	st, err := inst.State(ctx, LabelPrefix+"task.1")
	if err != nil {
		t.Fatalf("查询状态失败: %v", err)
	}
	if st.PlistExists || st.Loaded {
		t.Errorf("初始状态应都为空: %+v", st)
	}
	if !st.Healthy() {
		t.Error("文件与加载状态一致时应为 healthy")
	}

	// 只写文件（模拟 bootstrap 失败后的残留）→ 不一致
	path := filepath.Join(dir, LabelPrefix+"task.1.plist")
	if err := os.WriteFile(path, []byte("<plist/>"), 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	st, _ = inst.State(ctx, LabelPrefix+"task.1")
	if !st.PlistExists {
		t.Error("应检测到文件存在")
	}
	if st.Loaded {
		t.Error("不应标记为已加载")
	}
	if st.Healthy() {
		t.Error("文件存在但未加载时应为不健康")
	}
}

// TestListManagedOnlyReturnsOwnLabels 验证只列出本应用的 plist。
//
// 目录里可能有其它程序或系统的任务，全都读出来既没必要也可能误导。
func TestListManagedOnlyReturnsOwnLabels(t *testing.T) {
	dir := t.TempDir()
	inst := NewInstallerWithoutValidation(dir, newFakeRunner())

	files := []string{
		LabelPrefix + "task.1.plist",
		LabelPrefix + "task.2.plist",
		"com.apple.something.plist",
		"com.other.app.plist",
		"notes.txt",
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("<plist/>"), 0o644); err != nil {
			t.Fatalf("写入 %s 失败: %v", f, err)
		}
	}

	got, err := inst.ListManaged()
	if err != nil {
		t.Fatalf("列举失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应只列出 2 个本应用任务，实际 %d: %v", len(got), got)
	}
	for _, label := range got {
		if !strings.HasPrefix(label, LabelPrefix) {
			t.Errorf("不应包含外部 label: %s", label)
		}
	}
}

// TestListManagedMissingDir 验证目录不存在时返回空而不是报错。
func TestListManagedMissingDir(t *testing.T) {
	inst := NewInstallerWithoutValidation(filepath.Join(t.TempDir(), "nope"), newFakeRunner())
	got, err := inst.ListManaged()
	if err != nil {
		t.Fatalf("目录不存在不应报错: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("应返回空列表，实际 %v", got)
	}
}

// TestWriteFileAtomicNoLeftoverTemp 验证原子写入不留临时文件。
func TestWriteFileAtomicNoLeftoverTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.plist")

	if err := writeFileAtomic(path, []byte("content"), 0o644); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("目录应只有目标文件，实际: %v", names)
	}
}

// TestWriteFileAtomicReplacesContent 验证覆盖写入。
func TestWriteFileAtomicReplacesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.plist")

	if err := writeFileAtomic(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	if err := writeFileAtomic(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("覆盖写入失败: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(data) != "new" {
		t.Errorf("内容应被替换，实际 %q", string(data))
	}
}

// TestXMLEscape 验证转义表的完整性。
func TestXMLEscape(t *testing.T) {
	cases := map[string]string{
		"a&b":   "a&amp;b",
		"a<b":   "a&lt;b",
		"a>b":   "a&gt;b",
		`a"b`:   "a&quot;b",
		"a'b":   "a&apos;b",
		"plain": "plain",
		"中文路径":  "中文路径",
	}
	for in, want := range cases {
		if got := xmlEscape(in); got != want {
			t.Errorf("%q 应转义为 %q，实际 %q", in, want, got)
		}
	}
}
