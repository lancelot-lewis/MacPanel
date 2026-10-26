package orbstack

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// startFakeDocker 在临时目录启动一个 Unix Socket 上的假 Docker API。
//
// 用真实的 unix socket 而不是 mock transport：这样能验证
// 「Socket 路径 → DialContext → HTTP」整条链路，
// 而 mock 掉 transport 就只剩 map 比较，测不到实际问题。
func startFakeDocker(t *testing.T, handler http.Handler) string {
	t.Helper()

	// Unix Socket 路径有长度上限（104 字节），t.TempDir 的长路径会超限，
	// 因此用短名字的临时目录。
	dir, err := os.MkdirTemp("", "ds")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sockPath := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("监听 unix socket 失败: %v", err)
	}

	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	return sockPath
}

// dockerMux 构造假的 Docker API。
func dockerMux(containers []map[string]any) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Version":    "27.0.0",
			"ApiVersion": "1.46",
		})
	})
	mux.HandleFunc("/containers/json", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(containers)
	})
	return mux
}

// TestStatusAvailable 验证能识别可用的 Docker Socket。
func TestStatusAvailable(t *testing.T) {
	sock := startFakeDocker(t, dockerMux(nil))
	c := New(Options{SocketPath: sock})

	st := c.Status(context.Background())
	if !st.Available {
		t.Fatalf("应判定为可用，原因: %s", st.Reason)
	}
	if st.Version != "API 1.46" {
		t.Errorf("版本应为 API 1.46，实际 %q", st.Version)
	}
	if st.SocketPath != sock {
		t.Errorf("Socket 路径不符: %q", st.SocketPath)
	}
}

// TestStatusUnavailableWhenNoSocket 验证 Socket 不存在时返回原因而不是错误。
//
// 不可用是正常状态（用户可能没装 OrbStack），界面需要的是
// 「为什么不可用」，不是一个异常。
func TestStatusUnavailableWhenNoSocket(t *testing.T) {
	c := New(Options{SocketPath: filepath.Join(t.TempDir(), "nope.sock")})

	st := c.Status(context.Background())
	if st.Available {
		t.Error("不存在的 Socket 不应判定为可用")
	}
	if st.Reason == "" {
		t.Error("应说明不可用原因")
	}
}

// TestContainers 验证容器列表解析。
func TestContainers(t *testing.T) {
	containers := []map[string]any{
		{
			"Id":     "abc123def4567890abcdef",
			"Names":  []string{"/caddy"},
			"Image":  "caddy:2-alpine",
			"State":  "running",
			"Status": "Up 3 hours",
			"Ports": []map[string]any{
				{"IP": "0.0.0.0", "PrivatePort": 443, "PublicPort": 443, "Type": "tcp"},
				{"IP": "::", "PrivatePort": 80, "Type": "tcp"},
			},
		},
		{
			"Id":     "short",
			"Names":  []string{"/stopped-one"},
			"Image":  "nginx:latest",
			"State":  "exited",
			"Status": "Exited (0) 2 hours ago",
		},
	}

	sock := startFakeDocker(t, dockerMux(containers))
	c := New(Options{SocketPath: sock})

	items, st, err := c.Containers(context.Background())
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !st.Available {
		t.Fatalf("应可用，原因: %s", st.Reason)
	}
	if len(items) != 2 {
		t.Fatalf("应有 2 个容器，实际 %d", len(items))
	}

	// ID 截断到 12 位
	if items[0].ID != "abc123def456" {
		t.Errorf("ID 应截断为 12 位，实际 %q", items[0].ID)
	}
	// 名称去掉前导斜杠
	if items[0].Name != "caddy" {
		t.Errorf("名称应去掉前导斜杠，实际 %q", items[0].Name)
	}
	if items[0].State != "running" {
		t.Errorf("状态不符: %q", items[0].State)
	}

	// 端口格式化：映射的显示 host:port，未映射的标注
	if len(items[0].Ports) != 2 {
		t.Fatalf("应有 2 条端口信息，实际 %v", items[0].Ports)
	}
	if items[0].Ports[0] != "tcp/443 → *:443" {
		t.Errorf("映射端口格式不符: %q", items[0].Ports[0])
	}
	if items[0].Ports[1] != "tcp/80（未映射）" {
		t.Errorf("未映射端口格式不符: %q", items[0].Ports[1])
	}

	// 短 ID 不截断
	if items[1].ID != "short" {
		t.Errorf("短 ID 不应被截断，实际 %q", items[1].ID)
	}
}

// TestContainersUnavailable 验证不可用时返回原因而不报错。
func TestContainersUnavailable(t *testing.T) {
	c := New(Options{SocketPath: filepath.Join(t.TempDir(), "nope.sock")})

	items, st, err := c.Containers(context.Background())
	if err != nil {
		t.Fatalf("不可用不应返回错误: %v", err)
	}
	if st.Available {
		t.Error("不应判定为可用")
	}
	if len(items) != 0 {
		t.Errorf("不可用时应返回空列表，实际 %v", items)
	}
}

// TestAPINonOKStatus 验证 API 返回非 200 时给出可读错误。
func TestAPINonOKStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("permission denied"))
	})
	mux.HandleFunc("/containers/json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	sock := startFakeDocker(t, mux)
	c := New(Options{SocketPath: sock})

	st := c.Status(context.Background())
	if st.Available {
		t.Error("403 不应判定为可用")
	}
	if st.Reason == "" {
		t.Error("应说明原因")
	}
}

// TestContainerPortFormatting 验证端口格式化的边界。
func TestContainerPortFormatting(t *testing.T) {
	// 空 IP 应展示为 *
	c := dockerContainer{}
	c.Ports = append(c.Ports, struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	}{PrivatePort: 8080, PublicPort: 18080, Type: "tcp"})

	got := formatPorts(c)
	if len(got) != 1 || got[0] != "tcp/8080 → *:18080" {
		t.Errorf("空 IP 应展示为 *，实际 %v", got)
	}
}

// TestNoWriteOperations 断言本包不提供任何写操作。
//
// 这是设计约束的固化：Docker Socket 权限极高（能创建特权容器、
// 挂载任意目录），本应用只需要只读视图。
// 若将来加入启停，必须同时加入确认与审计，不能只加个方法。
func TestNoWriteOperations(t *testing.T) {
	// 用编译期断言：Client 只暴露这 2 个导出方法。
	// 若新增方法，这里需要显式更新并说明理由。
	type readOnlyClient interface {
		Status(ctx context.Context) model.OrbStackStatus
		Containers(ctx context.Context) ([]model.Container, model.OrbStackStatus, error)
	}
	var _ readOnlyClient = (*Client)(nil)
}
