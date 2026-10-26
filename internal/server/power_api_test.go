package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/lancelot-lewis/MacPanel/internal/agent"
)

// TestRebootRequiresRiskConfirmation 验证重启必须显式确认风险。
//
// 这不是安全边界（攻击者也能带上标记），而是防误触：
// 面板上「重启」按钮离「刷新」可能只有几十像素。
func TestRebootRequiresRiskConfirmation(t *testing.T) {
	env := newTestEnv(t, false)

	// 不带确认 → 400，且不应触达 Agent
	rec := env.do(t, http.MethodPost, "/api/v1/power/reboot", map[string]any{
		"delaySeconds": 60,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺少风险确认应返回 400，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if env.caller.fwCallCount != 0 {
		t.Error("被拒绝的请求不应触达 Agent")
	}

	// 带确认 → 通过
	rec = env.do(t, http.MethodPost, "/api/v1/power/reboot", map[string]any{
		"delaySeconds": 60,
		"reason":       "测试",
		"confirmsRisk": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("带确认应通过，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if env.caller.fwMethod != agent.MethodPowerReboot {
		t.Errorf("应调用 power.reboot，实际 %s", env.caller.fwMethod)
	}
}

// TestRebootAcceptsConfirmationHeader 验证可用请求头确认。
func TestRebootAcceptsConfirmationHeader(t *testing.T) {
	env := newTestEnv(t, false)

	rec := env.do(t, http.MethodPost, "/api/v1/power/reboot",
		map[string]any{"delaySeconds": 0},
		func(r *http.Request) { r.Header.Set("X-HLM-Confirm-Risk", "1") })

	if rec.Code != http.StatusOK {
		t.Fatalf("带确认头应通过，实际 %d（%s）", rec.Code, rec.Body.String())
	}
}

// TestRebootRejectsFalseConfirmation 验证 confirmsRisk=false 不算确认。
func TestRebootRejectsFalseConfirmation(t *testing.T) {
	env := newTestEnv(t, false)

	rec := env.do(t, http.MethodPost, "/api/v1/power/reboot", map[string]any{
		"confirmsRisk": false,
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("confirmsRisk=false 应被拒绝，实际 %d", rec.Code)
	}
}

// TestRebootValidatesDelay 验证延后时间的边界。
func TestRebootValidatesDelay(t *testing.T) {
	env := newTestEnv(t, false)

	cases := []any{-1, 3601, "abc"}
	for _, v := range cases {
		rec := env.do(t, http.MethodPost, "/api/v1/power/reboot", map[string]any{
			"delaySeconds": v,
			"confirmsRisk": true,
		})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("delaySeconds=%v 应返回 400，实际 %d", v, rec.Code)
		}
	}
}

// TestRebootDeniedIsAudited 验证被拒绝的重启写入审计。
//
// 只记成功不够：「有人在反复尝试重启但没带确认」这类信号
// 恰恰是排查误操作或异常调用的关键线索。
func TestRebootDeniedIsAudited(t *testing.T) {
	env := newTestEnv(t, false)

	_ = env.do(t, http.MethodPost, "/api/v1/power/reboot", map[string]any{})

	entries := env.recentAudit(t)
	found := false
	for _, e := range entries {
		if e["action"] == auditPowerReboot && e["result"] == "denied" {
			found = true
		}
	}
	if !found {
		t.Errorf("应记录 denied 审计，实际: %v", actionsOf(entries))
	}
}

// TestScheduleCreateAndList 验证创建与列表。
func TestScheduleCreateAndList(t *testing.T) {
	env := newTestEnv(t, false)

	rec := env.do(t, http.MethodPost, "/api/v1/schedules", map[string]any{
		"name":     "每日重启",
		"kind":     "reboot",
		"timezone": "Asia/Shanghai",
		"recurrence": map[string]any{
			"kind":      "daily",
			"timeOfDay": "04:30",
		},
		"enabled":            true,
		"minIntervalSeconds": 1800,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("创建应成功，实际 %d（%s）", rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodGet, "/api/v1/schedules", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应成功，实际 %d", rec.Code)
	}
}

// TestScheduleUpdateUsesPathID 验证路径 ID 是唯一权威。
//
// 若允许请求体覆盖 ID，就会出现「路径写 1、请求体写 2」导致
// 改错任务的情况。这类问题在界面上完全看不出来。
func TestScheduleUpdateUsesPathID(t *testing.T) {
	env := newTestEnv(t, false)

	rec := env.do(t, http.MethodPut, "/api/v1/schedules/7", map[string]any{
		"id":       999, // 试图改别的任务
		"name":     "改名",
		"kind":     "reboot",
		"timezone": "Asia/Shanghai",
		"recurrence": map[string]any{
			"kind":      "daily",
			"timeOfDay": "04:30",
		},
		"enabled":            true,
		"minIntervalSeconds": 1800,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("更新应成功，实际 %d（%s）", rec.Code, rec.Body.String())
	}

	params, ok := env.caller.fwParams.(map[string]any)
	if !ok {
		t.Fatalf("参数类型不符: %T", env.caller.fwParams)
	}
	task, ok := params["task"].(map[string]any)
	if !ok {
		t.Fatalf("应带 task 参数: %v", params)
	}
	// Server 直接把路径 ID 塞进 body，因此是 int64 而非 JSON 解码出的 float64。
	// 两种都接受，只关心数值。
	var gotID int64
	switch v := task["id"].(type) {
	case int64:
		gotID = v
	case float64:
		gotID = int64(v)
	case int:
		gotID = int64(v)
	}
	if gotID != 7 {
		t.Errorf("任务 ID 应取自路径（7），实际 %v", task["id"])
	}
}

// TestScheduleDeleteValidatesID 验证非法 ID 被拒绝。
func TestScheduleDeleteValidatesID(t *testing.T) {
	env := newTestEnv(t, false)

	for _, path := range []string{"/api/v1/schedules/0", "/api/v1/schedules/abc", "/api/v1/schedules/-1"} {
		rec := env.do(t, http.MethodDelete, path, nil)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Errorf("%s 应返回 400/404，实际 %d", path, rec.Code)
		}
	}
}

// TestScheduleRunNowRequiresConfirmation 验证手动执行也需确认。
//
// 「立即执行」等价于把计划重启提前到现在，风险与直接重启相同。
func TestScheduleRunNowRequiresConfirmation(t *testing.T) {
	env := newTestEnv(t, false)

	rec := env.do(t, http.MethodPost, "/api/v1/schedules/1/run", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺少确认应返回 400，实际 %d", rec.Code)
	}

	rec = env.do(t, http.MethodPost, "/api/v1/schedules/1/run", map[string]any{
		"confirmsRisk": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("带确认应通过，实际 %d（%s）", rec.Code, rec.Body.String())
	}
}

// TestScheduleExecutionsValidatesQuery 验证查询参数校验。
func TestScheduleExecutionsValidatesQuery(t *testing.T) {
	env := newTestEnv(t, false)

	cases := []string{
		"/api/v1/schedules/executions?limit=501",
		"/api/v1/schedules/executions?limit=-1",
		"/api/v1/schedules/executions?taskId=abc",
		"/api/v1/schedules/executions?taskId=-5",
	}
	for _, path := range cases {
		rec := env.do(t, http.MethodGet, path, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s 应返回 400，实际 %d", path, rec.Code)
		}
	}

	// 合法的查询应通过
	rec := env.do(t, http.MethodGet, "/api/v1/schedules/executions?limit=20&taskId=3", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("合法查询应通过，实际 %d", rec.Code)
	}
}

// TestScheduleCreateIsAudited 验证创建任务留审计。
func TestScheduleCreateIsAudited(t *testing.T) {
	env := newTestEnv(t, false)

	_ = env.do(t, http.MethodPost, "/api/v1/schedules", map[string]any{
		"name":     "每日重启",
		"kind":     "reboot",
		"timezone": "Asia/Shanghai",
		"recurrence": map[string]any{
			"kind":      "daily",
			"timeOfDay": "04:30",
		},
		"enabled":            true,
		"minIntervalSeconds": 1800,
	})

	entries := env.recentAudit(t)
	found := false
	for _, e := range entries {
		if e["action"] == auditScheduleCreate && e["result"] == "success" {
			found = true
		}
	}
	if !found {
		t.Errorf("创建任务应写审计，实际: %v", actionsOf(entries))
	}
}

// TestPowerWriteRequiresAuth 验证远程模式下未登录不能操作电源。
//
// 前提：远程模式必须已启用。这里直接验证 handler 链上的认证中间件，
// 因为「未登录就能重启机器」是这类面板最严重的缺陷。
func TestPowerWriteRequiresAuth(t *testing.T) {
	env := newTestEnv(t, true) // 远程模式

	// 远程模式先校验 X-HLM-Request（CSRF 防护），
	// 满足后才进入认证阶段。因此这里要带上该头，
	// 才能真正验证「未登录不能重启」这条断言。
	rec := env.do(t, http.MethodPost, "/api/v1/power/reboot",
		map[string]any{"confirmsRisk": true},
		func(r *http.Request) { r.Header.Set("X-HLM-Request", "1") })

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应返回 401，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if env.caller.fwCallCount != 0 {
		t.Error("未认证请求不应触达 Agent")
	}
}

// TestScheduleListRequiresAuth 验证远程模式下读接口也需登录。
func TestScheduleListRequiresAuth(t *testing.T) {
	env := newTestEnv(t, true)

	rec := env.do(t, http.MethodGet, "/api/v1/schedules", nil,
		func(r *http.Request) { r.Header.Set("X-HLM-Request", "1") })
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("未登录应返回 401，实际 %d", rec.Code)
	}
}

// actionsOf 提取审计记录里的动作名，便于失败时打印。
func actionsOf(entries []map[string]any) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		action, _ := e["action"].(string)
		result, _ := e["result"].(string)
		out = append(out, action+":"+result)
	}
	return out
}

// TestPathIDNoTrailingSlashBypass 验证带斜杠的 ID 也不被误解析。
func TestPathIDNoTrailingSlashBypass(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodDelete, "/api/v1/schedules/1/extra", nil)
	// 未匹配路由 → 404/405 均可接受，关键是不能当成 ID=1 处理
	if rec.Code == http.StatusOK {
		t.Error("多余的路径段不应被忽略")
	}
	if strings.Contains(rec.Body.String(), "stub") {
		t.Error("不应触达 Agent")
	}
}

// recentAudit 读取最近的审计记录，用于断言动作与结果。
//
// 直接从数据库读而不是走 API：走 API 会多一条 audit 读记录，
// 断言时容易把自己也算进去。
func (e *testEnv) recentAudit(t *testing.T) []map[string]any {
	t.Helper()

	rows, err := e.db.SQL().QueryContext(context.Background(),
		`SELECT action, result FROM audit_logs ORDER BY id DESC LIMIT 50`)
	if err != nil {
		t.Fatalf("查询审计失败: %v", err)
	}
	defer rows.Close()

	out := make([]map[string]any, 0, 8)
	for rows.Next() {
		var action, result string
		if err := rows.Scan(&action, &result); err != nil {
			t.Fatalf("扫描审计失败: %v", err)
		}
		out = append(out, map[string]any{"action": action, "result": result})
	}
	return out
}
