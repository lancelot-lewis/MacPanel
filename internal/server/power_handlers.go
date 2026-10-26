package server

import (
	"net/http"
	"strconv"

	"github.com/lancelot-lewis/MacPanel/internal/audit"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// M3 写操作的审计动作名。
const (
	auditPowerReboot    = "power.reboot.request"
	auditPowerCancel    = "power.reboot.cancel"
	auditScheduleCreate = "schedule.create"
	auditScheduleUpdate = "schedule.update"
	auditScheduleDelete = "schedule.delete"
	auditScheduleRunNow = "schedule.run_now"
	auditScheduleSync   = "schedule.sync"
)

// handlePowerStatus 读取电源与调度状态。
func (s *Server) handlePowerStatus(w http.ResponseWriter, r *http.Request) {
	raw, err := s.svc.PowerStatus(r.Context())
	if err != nil {
		s.auditRead(r, "power.status", err)
		writeError(w, r, err)
		return
	}
	writeRawData(w, r, raw)
}

// handlePowerReboot 排定重启。
//
// 与防火墙应用同样的风险确认要求，但理由不同：
// 防火墙的确认是防「改错了连不上」，重启的确认是防「误点导致
// 正在跑的服务中断」。两者都不能靠前端按钮状态代替——请求可能被
// 直接构造，也可能在倒计时期间被重放。
func (s *Server) handlePowerReboot(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}

	if !hasRiskConfirmation(r, body) {
		s.auditWrite(r, auditPowerReboot, audit.ResultDenied, "缺少风险确认标记")
		writeError(w, r, model.NewError(model.CodeInvalidArgument,
			"重启会中断所有正在运行的服务。请在请求中携带 confirmsRisk=true 以确认你已了解风险"))
		return
	}

	// 延后秒数：缺省 0（尽快重启）。上限与 Agent / execx 一致（60 分钟），
	// 这里提前拦一次，给出更早的错误反馈。
	delay := 0
	if v, ok := body["delaySeconds"]; ok && v != nil {
		f, ok := toFloat(v)
		if !ok {
			writeError(w, r, model.NewError(model.CodeInvalidArgument, "delaySeconds 必须是数字"))
			return
		}
		delay = int(f)
	}
	if delay < 0 || delay > 3600 {
		writeError(w, r, model.NewError(model.CodeInvalidArgument,
			"延后时间必须在 0-3600 秒之间（最多 60 分钟）"))
		return
	}

	reason := ""
	if v, ok := body["reason"].(string); ok {
		reason = v
	}
	if len(reason) > 200 {
		writeError(w, r, model.NewError(model.CodeInvalidArgument, "原因不能超过 200 个字符"))
		return
	}

	params := map[string]any{
		"delaySeconds": delay,
		"reason":       reason,
		"actor":        ident(r),
		"remoteAddr":   remoteHost(r),
		"requestId":    requestIDFrom(r.Context()),
	}

	raw, err := s.svc.PowerReboot(r.Context(), params)
	if err != nil {
		s.auditWrite(r, auditPowerReboot, audit.ResultFailure, err.Error())
		writeError(w, r, err)
		return
	}
	s.auditWrite(r, auditPowerReboot, audit.ResultSuccess,
		"delay="+strconv.Itoa(delay)+"s reason="+reason)
	writeRawData(w, r, raw)
}

// handlePowerCancel 取消待执行的重启。
func (s *Server) handlePowerCancel(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, r, err)
			return
		}
	}

	params := map[string]any{
		"actor":      ident(r),
		"remoteAddr": remoteHost(r),
		"requestId":  requestIDFrom(r.Context()),
	}

	raw, err := s.svc.PowerCancel(r.Context(), params)
	if err != nil {
		s.auditWrite(r, auditPowerCancel, audit.ResultFailure, err.Error())
		writeError(w, r, err)
		return
	}
	s.auditWrite(r, auditPowerCancel, audit.ResultSuccess, "")
	writeRawData(w, r, raw)
}

// handleScheduleList 读取全部计划任务。
func (s *Server) handleScheduleList(w http.ResponseWriter, r *http.Request) {
	raw, err := s.svc.ScheduleList(r.Context())
	if err != nil {
		s.auditRead(r, "schedule.list", err)
		writeError(w, r, err)
		return
	}
	writeRawData(w, r, raw)
}

// handleScheduleCreate 创建计划任务。
//
// 不需要风险确认：创建任务只是写数据库与 plist，不会立即执行。
// 真正的执行仍有「最短间隔 + 熔断 + 确认」等多道保护。
func (s *Server) handleScheduleCreate(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}

	raw, err := s.svc.ScheduleCreate(r.Context(), map[string]any{"task": body})
	if err != nil {
		s.auditWrite(r, auditScheduleCreate, audit.ResultFailure, err.Error())
		writeError(w, r, err)
		return
	}
	s.auditWrite(r, auditScheduleCreate, audit.ResultSuccess, taskNameOf(body))
	writeRawData(w, r, raw)
}

// handleScheduleUpdate 更新计划任务。任务 ID 从路径取。
func (s *Server) handleScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	var body map[string]any
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	// 路径里的 ID 是唯一权威：忽略请求体里的 id，
	// 避免「路径写 1、请求体写 2」这种不一致导致的误改。
	body["id"] = id

	raw, err := s.svc.ScheduleUpdate(r.Context(), map[string]any{"task": body})
	if err != nil {
		s.auditWrite(r, auditScheduleUpdate, audit.ResultFailure, err.Error())
		writeError(w, r, err)
		return
	}
	s.auditWrite(r, auditScheduleUpdate, audit.ResultSuccess, taskNameOf(body))
	writeRawData(w, r, raw)
}

// handleScheduleDelete 删除计划任务。
func (s *Server) handleScheduleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	raw, err := s.svc.ScheduleDelete(r.Context(), map[string]any{"id": id})
	if err != nil {
		s.auditWrite(r, auditScheduleDelete, audit.ResultFailure, err.Error())
		writeError(w, r, err)
		return
	}
	s.auditWrite(r, auditScheduleDelete, audit.ResultSuccess, "id="+strconv.FormatInt(id, 10))
	writeRawData(w, r, raw)
}

// handleScheduleExecutions 读取执行记录。
func (s *Server) handleScheduleExecutions(w http.ResponseWriter, r *http.Request) {
	taskID := int64(0)
	if v := r.URL.Query().Get("taskId"); v != "" {
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, r, model.NewError(model.CodeInvalidArgument, "taskId 必须是非负整数"))
			return
		}
		taskID = parsed
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 || parsed > 500 {
			writeError(w, r, model.NewError(model.CodeInvalidArgument, "limit 必须在 0-500 之间"))
			return
		}
		limit = parsed
	}

	raw, err := s.svc.ScheduleExecutions(r.Context(), map[string]any{
		"taskId": taskID,
		"limit":  limit,
	})
	if err != nil {
		s.auditRead(r, "schedule.executions", err)
		writeError(w, r, err)
		return
	}
	writeRawData(w, r, raw)
}

// handleScheduleRunNow 立即执行一次任务。
//
// 需要风险确认：它等价于把「几分钟后的计划重启」提前到现在，
// 用户很可能是在远程连接上操作。
func (s *Server) handleScheduleRunNow(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	var body map[string]any
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, r, err)
			return
		}
	}

	if !hasRiskConfirmation(r, body) {
		s.auditWrite(r, auditScheduleRunNow, audit.ResultDenied, "缺少风险确认标记")
		writeError(w, r, model.NewError(model.CodeInvalidArgument,
			"立即执行任务会马上触发重启。请在请求中携带 confirmsRisk=true"))
		return
	}

	raw, err := s.svc.ScheduleRunNow(r.Context(), map[string]any{"id": id})
	if err != nil {
		s.auditWrite(r, auditScheduleRunNow, audit.ResultFailure, err.Error())
		writeError(w, r, err)
		return
	}
	s.auditWrite(r, auditScheduleRunNow, audit.ResultSuccess, "id="+strconv.FormatInt(id, 10))
	writeRawData(w, r, raw)
}

// handleScheduleSync 手动触发一次同步（把任务推到 launchd）。
func (s *Server) handleScheduleSync(w http.ResponseWriter, r *http.Request) {
	raw, err := s.svc.ScheduleSync(r.Context())
	if err != nil {
		s.auditWrite(r, auditScheduleSync, audit.ResultFailure, err.Error())
		writeError(w, r, err)
		return
	}
	s.auditWrite(r, auditScheduleSync, audit.ResultSuccess, "")
	writeRawData(w, r, raw)
}

// pathID 从路径参数 {id} 解析正整数 ID。
func pathID(r *http.Request) (int64, error) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, model.NewError(model.CodeInvalidArgument, "任务 ID 必须是正整数")
	}
	return id, nil
}

// taskNameOf 尽力取出任务名用于审计（取不到就返回空串）。
func taskNameOf(body map[string]any) string {
	if name, ok := body["name"].(string); ok {
		return name
	}
	return ""
}

// toFloat 把 JSON 数字转换成 float64。
//
// encoding/json 解码到 any 时数字是 float64，但测试或其它调用方
// 可能传入 int，因此两种都要接受。
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}
