package fake

import (
	"context"
	"testing"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// TestProvidesAllFields 验证模拟数据字段完整，可用于前端开发。
func TestProvidesAllFields(t *testing.T) {
	c := New()
	ctx := context.Background()

	ov, err := c.SystemOverview(ctx)
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if ov.Hostname == "" || ov.OS.ProductVersion == "" {
		t.Error("模拟数据应包含基本字段")
	}
	if ov.Memory.State != model.MetricOK {
		t.Error("模拟数据内存应可用")
	}

	snap, err := c.NetworkSnapshot(ctx)
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if !snap.HasIPv6 || !snap.HasGlobalV6 {
		t.Error("模拟数据应包含公网 IPv6")
	}
	var hasLoopback bool
	for _, i := range snap.Interfaces {
		if i.Loopback {
			hasLoopback = true
		}
	}
	if !hasLoopback {
		t.Error("模拟数据应包含回环接口")
	}

	st, err := c.FirewallStatus(ctx)
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if st.Managed != model.StateNotConfigured {
		t.Error("模拟数据应为未配置状态")
	}
	if st.Writable {
		t.Error("模拟数据 Writable 应为 false")
	}
}

// TestFakeDataIsMarked 验证模拟数据带有明确标识，避免误认为真实状态。
func TestFakeDataIsMarked(t *testing.T) {
	c := New()
	st, err := c.FirewallStatus(context.Background())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}

	var marked bool
	for _, w := range st.Warnings {
		if len(w) > 0 && (contains(w, "模拟") || contains(w, "fake")) {
			marked = true
		}
	}
	if !marked {
		t.Errorf("模拟数据必须带有明确标识，实际 warnings=%v", st.Warnings)
	}
}

// TestInjectError 验证错误注入可用。
func TestInjectError(t *testing.T) {
	wantErr := model.Unavailable("注入的故障")
	c := &Collector{
		OverviewFn: func(context.Context) (model.SystemOverview, error) {
			return model.SystemOverview{}, wantErr
		},
	}
	_, err := c.SystemOverview(context.Background())
	if err == nil {
		t.Fatal("应返回注入的错误")
	}
	if model.CodeOf(err) != model.CodeUnavailable {
		t.Errorf("错误码应为 unavailable，实际 %s", model.CodeOf(err))
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
