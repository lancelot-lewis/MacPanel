package server

import (
	"net/http"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// handleDiagnostics 采集网络诊断快照（路由与监听端口）。
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	raw, err := s.svc.DiagnosticsSnapshot(r.Context())
	if err != nil {
		s.auditRead(r, "diagnostics.snapshot", err)
		writeError(w, r, err)
		return
	}
	writeRawData(w, r, raw)
}

// handleTCPProbe 检测 TCP 端口连通性。
//
// 不需要风险确认（不改系统状态），但会写审计：
// 「谁在什么时候探测了哪个端口」在排查异常访问时有价值。
func (s *Server) handleTCPProbe(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}

	host := stringField(body, "host")
	port := 0
	if v, ok := toFloat(body["port"]); ok {
		port = int(v)
	}
	timeoutMs := 0
	if v, ok := toFloat(body["timeoutMs"]); ok {
		timeoutMs = int(v)
	}

	// Server 侧先做一次形态校验，尽早给出可读错误。
	if err := model.ValidateProbeTarget(host, port, false); err != nil {
		writeError(w, r, err)
		return
	}

	params := map[string]any{"host": host, "port": port, "timeoutMs": timeoutMs}
	raw, err := s.svc.TCPProbe(r.Context(), params)
	if err != nil {
		s.auditRead(r, "diagnostics.tcp_probe", err)
		writeError(w, r, err)
		return
	}
	s.auditReadOK(r, "diagnostics.tcp_probe", model.FormatTarget(host, port))
	writeRawData(w, r, raw)
}

// handleOrbStackContainers 列出容器（只读）。
func (s *Server) handleOrbStackContainers(w http.ResponseWriter, r *http.Request) {
	raw, err := s.svc.OrbStackContainers(r.Context())
	if err != nil {
		s.auditRead(r, "orbstack.containers", err)
		writeError(w, r, err)
		return
	}
	writeRawData(w, r, raw)
}
