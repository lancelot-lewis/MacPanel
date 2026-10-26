// Package darwin 实现 macOS 上的只读状态采集。
//
// 全部实现只读取系统信息，不修改任何系统状态。需要 root 才能读取的项
// （如 PF 状态）在权限不足时返回「不可用」，绝不用猜测值代替。
package darwin

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/platform"
	"github.com/lancelot-lewis/MacPanel/internal/platform/execx"
)

// cmdTimeout 单条系统命令超时。
const cmdTimeout = 3 * time.Second

// Collector 是 macOS 实现。
type Collector struct {
	cfg platform.Config
}

// New 创建 macOS 采集器。
func New(cfg platform.Config) *Collector {
	return &Collector{cfg: cfg}
}

var _ platform.Collector = (*Collector)(nil)

// SystemOverview 采集系统概览。
func (c *Collector) SystemOverview(ctx context.Context) (model.SystemOverview, error) {
	now := time.Now().UTC()
	ov := model.SystemOverview{CollectedAt: now}
	var missing []model.MissingMetric

	if host, err := os.Hostname(); err == nil {
		ov.Hostname = host
	} else {
		missing = append(missing, model.MissingMetric{Metric: "hostname", Reason: err.Error()})
	}

	// 版本信息
	ov.OS.Architecture = runtime.GOARCH
	if out, err := sysctlOne(ctx, "kern.ostype"); err == nil {
		if out2, err2 := sysctlOne(ctx, "kern.osrelease"); err2 == nil {
			ov.OS.Kernel = strings.TrimSpace(out) + " " + strings.TrimSpace(out2)
		}
	}
	if out, err := execx.Run(ctx, cmdTimeout, "/usr/bin/sw_vers", "-productVersion"); err == nil {
		ov.OS.ProductVersion = strings.TrimSpace(out.Stdout)
	} else {
		missing = append(missing, model.MissingMetric{Metric: "os.productVersion", Reason: reasonOf(err)})
	}
	if out, err := execx.Run(ctx, cmdTimeout, "/usr/bin/sw_vers", "-buildVersion"); err == nil {
		ov.OS.Build = strings.TrimSpace(out.Stdout)
	}

	// CPU
	if out, err := sysctlOne(ctx, "machdep.cpu.brand_string"); err == nil {
		ov.CPU.Model = strings.TrimSpace(out)
	} else if out, err := sysctlOne(ctx, "hw.model"); err == nil {
		// Apple Silicon 上 machdep.cpu.brand_string 可能不可用，回退到机型标识
		ov.CPU.Model = strings.TrimSpace(out)
	} else {
		missing = append(missing, model.MissingMetric{Metric: "cpu.model", Reason: reasonOf(err)})
	}
	if n, err := sysctlInt(ctx, "hw.physicalcpu"); err == nil {
		ov.CPU.PhysicalCores = n
	}
	if n, err := sysctlInt(ctx, "hw.logicalcpu"); err == nil {
		ov.CPU.LogicalCores = n
	}
	if load, err := loadAverage(ctx); err == nil {
		ov.CPU.LoadAverage = load
	} else {
		missing = append(missing, model.MissingMetric{Metric: "cpu.loadAverage", Reason: reasonOf(err)})
	}

	// 内存
	ov.Memory = collectMemory(ctx)
	if ov.Memory.State != model.MetricOK {
		missing = append(missing, model.MissingMetric{Metric: "memory", Reason: "无法读取内存统计"})
	}

	// 磁盘
	disks, diskErr := collectDisks(ctx)
	ov.Disks = disks
	if diskErr != nil {
		missing = append(missing, model.MissingMetric{Metric: "disks", Reason: diskErr.Error()})
	}

	// 启动时间
	if boot, err := bootTime(ctx); err == nil {
		b := boot.UTC()
		ov.BootTime = &b
		up := now.Sub(b).Seconds()
		ov.UptimeSeconds = &up
	} else {
		missing = append(missing, model.MissingMetric{Metric: "bootTime", Reason: reasonOf(err)})
	}

	ov.Missing = missing
	return ov, nil
}

func collectMemory(ctx context.Context) model.MemoryInfo {
	info := model.MemoryInfo{State: model.MetricUnavailable}

	totalStr, err := sysctlOne(ctx, "hw.memsize")
	if err != nil {
		return info
	}
	total, err := strconv.ParseInt(strings.TrimSpace(totalStr), 10, 64)
	if err != nil {
		return info
	}
	info.TotalBytes = total

	// 通过 vm_stat 获取分页统计（比 sysctl 键更稳定，不同版本键名有差异）。
	res, err := execx.Run(ctx, cmdTimeout, "/usr/bin/vm_stat")
	if err != nil {
		return info
	}
	pageSize := int64(16384)
	stats := map[string]int64{}
	scanner := bufio.NewScanner(strings.NewReader(res.Stdout))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Mach Virtual Memory Statistics") {
			if i := strings.Index(line, "page size of "); i >= 0 {
				rest := line[i+len("page size of "):]
				if j := strings.Index(rest, " bytes"); j > 0 {
					if v, err := strconv.ParseInt(strings.TrimSpace(rest[:j]), 10, 64); err == nil {
						pageSize = v
					}
				}
			}
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		valStr := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(parts[1]), "."))
		if v, err := strconv.ParseInt(valStr, 10, 64); err == nil {
			stats[key] = v
		}
	}

	// 可用内存口径：free + speculative + purgeable + inactive。
	// macOS 的 Pages inactive 是可被立即回收的文件缓存，用户视角下属于「可用」；
	// 若只算 free，会把 16 GiB 的机器显示成「已用 15.8 GiB」这种明显失真的结果。
	// 这里采用与「活动监视器→内存已用」接近的口径，并在注释中说明。
	free := stats["Pages free"]
	speculative := stats["Pages speculative"]
	purgeable := stats["Pages purgeable"]
	inactive := stats["Pages inactive"]
	if free == 0 && speculative == 0 {
		return info
	}

	availPages := free + speculative + purgeable + inactive
	freeBytes := (free + speculative) * pageSize
	availBytes := availPages * pageSize
	if availBytes > total {
		availBytes = total
	}
	used := total - availBytes
	if used < 0 {
		used = 0
	}

	info.FreeBytes = &freeBytes
	info.AvailableBytes = &availBytes
	info.UsedBytes = &used
	info.State = model.MetricOK
	return info
}

func collectDisks(ctx context.Context) ([]model.DiskUsage, error) {
	// 只报告根卷与数据卷，避免噪声；使用 statfs 而非 df，减少解析脆弱性。
	candidates := []string{"/", "/System/Volumes/Data"}
	seen := map[string]bool{}
	out := make([]model.DiskUsage, 0, len(candidates))

	for _, path := range candidates {
		du, err := statFS(path)
		if err != nil {
			continue
		}
		// 根卷与数据卷在同一容器上时会出现重复，按挂载点去重。
		if seen[du.MountPoint] {
			continue
		}
		seen[du.MountPoint] = true
		out = append(out, du)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("无法读取任何卷容量信息")
	}
	return out, nil
}

func bootTime(ctx context.Context) (time.Time, error) {
	// kern.boottime 输出形如：{ sec = 1792892051, usec = 984008 } Sun Oct ...
	out, err := sysctlOne(ctx, "kern.boottime")
	if err != nil {
		return time.Time{}, err
	}
	sec := extractKV(out, "sec")
	usec := extractKV(out, "usec")
	if sec <= 0 {
		return time.Time{}, fmt.Errorf("无法解析 kern.boottime: %q", out)
	}
	return time.Unix(sec, usec*1000), nil
}

func extractKV(s, key string) int64 {
	idx := strings.Index(s, key+" = ")
	if idx < 0 {
		return 0
	}
	rest := s[idx+len(key)+3:]
	end := strings.IndexAny(rest, ",}")
	if end < 0 {
		end = len(rest)
	}
	v, err := strconv.ParseInt(strings.TrimSpace(rest[:end]), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func loadAverage(ctx context.Context) (*model.LoadAvg, error) {
	out, err := sysctlOne(ctx, "vm.loadavg")
	if err != nil {
		return nil, err
	}
	clean := strings.NewReplacer("{", " ", "}", " ").Replace(out)
	fields := strings.Fields(clean)
	if len(fields) < 3 {
		return nil, fmt.Errorf("无法解析 vm.loadavg: %q", out)
	}
	vals := make([]float64, 3)
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return nil, fmt.Errorf("无法解析负载值 %q", fields[i])
		}
		vals[i] = v
	}
	return &model.LoadAvg{One: vals[0], Five: vals[1], Fifteen: vals[2]}, nil
}

func sysctlOne(ctx context.Context, key string) (string, error) {
	res, err := execx.Run(ctx, cmdTimeout, "/usr/sbin/sysctl", "-n", key)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

func sysctlInt(ctx context.Context, key string) (int, error) {
	s, err := sysctlOne(ctx, key)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(s))
}

func reasonOf(err error) string {
	if err == nil {
		return ""
	}
	var ce *execx.CommandError
	if ok := asCommandError(err, &ce); ok {
		return strings.TrimSpace(ce.Stderr)
	}
	return err.Error()
}

func asCommandError(err error, target **execx.CommandError) bool {
	if ce, ok := err.(*execx.CommandError); ok {
		*target = ce
		return true
	}
	return false
}

// NetworkSnapshot 采集网络接口。
// 使用 net.Interfaces 而非解析 ifconfig，避免不同 macOS 版本的文本差异。
func (c *Collector) NetworkSnapshot(ctx context.Context) (model.NetworkSnapshot, error) {
	snap := model.NetworkSnapshot{CollectedAt: time.Now().UTC()}

	ifaces, err := net.Interfaces()
	if err != nil {
		return snap, model.Unavailable("读取网络接口失败：%v", err)
	}

	kinds := hardwarePortKinds(ctx)

	for _, iface := range ifaces {
		item := model.Interface{
			Name:     iface.Name,
			Index:    iface.Index,
			Up:       iface.Flags&net.FlagUp != 0,
			Loopback: iface.Flags&net.FlagLoopback != 0,
			MTU:      iface.MTU,
			Flags:    flagsToStrings(iface.Flags),
			IPv4:     []model.Address{},
			IPv6:     []model.Address{},
		}
		if iface.HardwareAddr != nil {
			item.HardwareAddr = iface.HardwareAddr.String()
		}
		item.Kind = kindOf(iface, kinds)

		addrs, err := iface.Addrs()
		if err != nil {
			snap.Missing = append(snap.Missing, model.MissingMetric{
				Metric: "interface.addresses." + iface.Name,
				Reason: err.Error(),
			})
		}
		for _, a := range addrs {
			addr := toAddress(a)
			if addr == nil {
				continue
			}
			if addr.Family == "ipv4" {
				item.IPv4 = append(item.IPv4, *addr)
			} else {
				item.IPv6 = append(item.IPv6, *addr)
				snap.HasIPv6 = true
				if !addr.IsLoopback && !addr.IsLinkLocal {
					snap.HasGlobalV6 = true
				}
			}
		}
		snap.Interfaces = append(snap.Interfaces, item)
	}
	return snap, nil
}

func toAddress(a net.Addr) *model.Address {
	switch v := a.(type) {
	case *net.IPNet:
		return buildAddress(v.IP, v.Mask)
	case *net.IPAddr:
		return buildAddress(v.IP, nil)
	}
	// 兜底解析 "addr/prefix"
	s := a.String()
	if i := strings.Index(s, "/"); i > 0 {
		ip := net.ParseIP(s[:i])
		if ip == nil {
			return nil
		}
		plen, err := strconv.Atoi(s[i+1:])
		if err != nil {
			plen = 0
		}
		return buildAddress(ip, net.CIDRMask(plen, 128))
	}
	return nil
}

func buildAddress(ip net.IP, mask net.IPMask) *model.Address {
	if ip == nil {
		return nil
	}
	bits := 128
	family := "ipv6"
	if v4 := ip.To4(); v4 != nil {
		ip = v4
		bits = 32
		family = "ipv4"
	}
	prefix := 0
	if mask != nil {
		if ones, mbits := mask.Size(); mbits > 0 {
			prefix = ones
		}
	}
	if prefix == 0 {
		// 单地址场景：按位长给出未指定前缀，前端显示为 /0 更诚实。
		prefix = bits
	}

	addr := &model.Address{
		Address:     ip.String(),
		PrefixLen:   prefix,
		Family:      family,
		IsLoopback:  ip.IsLoopback(),
		IsLinkLocal: ip.IsLinkLocalUnicast(),
		IsPrivate:   ip.IsPrivate(),
	}
	switch {
	case addr.IsLoopback:
		addr.Scope = "loopback"
	case addr.IsLinkLocal:
		addr.Scope = "link-local"
	case addr.IsPrivate:
		addr.Scope = "private"
	default:
		addr.Scope = "global"
	}
	return addr
}

// kindOf 依据接口名与硬件端口映射判断接口类型。
func kindOf(iface net.Interface, kinds map[string]string) model.InterfaceKind {
	name := iface.Name
	if iface.Flags&net.FlagLoopback != 0 || name == "lo0" {
		return model.KindLoopback
	}
	switch {
	case strings.HasPrefix(name, "utun"), strings.HasPrefix(name, "ipsec"),
		strings.HasPrefix(name, "gif"), strings.HasPrefix(name, "stf"):
		return model.KindTunnel
	case strings.HasPrefix(name, "bridge"), strings.HasPrefix(name, "vmenet"):
		return model.KindBridge
	case strings.HasPrefix(name, "awdl"), strings.HasPrefix(name, "llw"),
		strings.HasPrefix(name, "anpi"), strings.HasPrefix(name, "ap"):
		return model.KindVirtual
	}
	if port, ok := kinds[name]; ok {
		lower := strings.ToLower(port)
		switch {
		case strings.Contains(lower, "wi-fi"), strings.Contains(lower, "wifi"):
			return model.KindWiFi
		case strings.Contains(lower, "ethernet"):
			return model.KindEthernet
		}
	}
	if strings.HasPrefix(name, "en") {
		return model.KindEthernet
	}
	return model.KindOther
}

// hardwarePortKinds 用 networksetup 获取「设备名 → 硬件端口名」映射。
// 该命令不需要 root，失败时退化为空映射（不影响其它字段）。
func hardwarePortKinds(ctx context.Context) map[string]string {
	out := map[string]string{}
	res, err := execx.Run(ctx, cmdTimeout, "/usr/sbin/networksetup", "-listallhardwareports")
	if err != nil {
		return out
	}
	var port string
	scanner := bufio.NewScanner(strings.NewReader(res.Stdout))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if v, ok := strings.CutPrefix(line, "Hardware Port: "); ok {
			port = v
			continue
		}
		if v, ok := strings.CutPrefix(line, "Device: "); ok && port != "" {
			out[v] = port
		}
	}
	return out
}

func flagsToStrings(f net.Flags) []string {
	var out []string
	if f&net.FlagUp != 0 {
		out = append(out, "up")
	}
	if f&net.FlagBroadcast != 0 {
		out = append(out, "broadcast")
	}
	if f&net.FlagLoopback != 0 {
		out = append(out, "loopback")
	}
	if f&net.FlagPointToPoint != 0 {
		out = append(out, "pointtopoint")
	}
	if f&net.FlagMulticast != 0 {
		out = append(out, "multicast")
	}
	if f&net.FlagRunning != 0 {
		out = append(out, "running")
	}
	return out
}
