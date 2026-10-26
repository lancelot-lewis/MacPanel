package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// envelope 是所有 API 响应的统一外层结构。
// 成功：{"ok":true,"data":...}
// 失败：{"ok":false,"error":{"code":"...","message":"..."}}
type envelope struct {
	OK    bool         `json:"ok"`
	Data  any          `json:"data,omitempty"`
	Error *model.Error `json:"error,omitempty"`
	Meta  *meta        `json:"meta,omitempty"`
}

// meta 携带非业务性附加信息。
type meta struct {
	RequestID string `json:"requestId,omitempty"`
	// Degraded 表示数据可用但存在降级（例如部分指标不可用）。
	Degraded bool     `json:"degraded,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

// maxJSONBody 请求体上限。本应用没有大负载上传，64 KiB 足够。
const maxJSONBody = 64 << 10

func writeJSON(w http.ResponseWriter, r *http.Request, status int, body envelope) {
	if body.Meta == nil {
		id := requestIDFrom(r.Context())
		if id != "" {
			body.Meta = &meta{RequestID: id}
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// API 响应绝不缓存：状态类数据必须实时。
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(body); err != nil {
		// 响应已开始写入，无法再改状态码，只能记录。
		slog.Default().Warn("写入 JSON 响应失败", "error", err)
	}
}

func writeData(w http.ResponseWriter, r *http.Request, data any) {
	writeJSON(w, r, http.StatusOK, envelope{OK: true, Data: data})
}

func writeDataWithMeta(w http.ResponseWriter, r *http.Request, data any, m *meta) {
	writeJSON(w, r, http.StatusOK, envelope{OK: true, Data: data, Meta: m})
}

// writeError 把任意 error 映射为统一错误响应与合适的状态码。
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	e := model.AsError(err)
	status := statusForCode(e.Code)
	// 内部错误对外只给笼统信息，细节写日志。
	if e.Code == model.CodeInternal {
		slog.Default().Error("请求处理失败",
			"requestId", requestIDFrom(r.Context()),
			"path", r.URL.Path,
			"error", err)
		e = model.NewError(model.CodeInternal, "服务器内部错误")
	}
	writeJSON(w, r, status, envelope{OK: false, Error: e})
}

func statusForCode(c model.Code) int {
	switch c {
	case model.CodeInvalidArgument:
		return http.StatusBadRequest
	case model.CodeUnauthorized:
		return http.StatusUnauthorized
	case model.CodeForbidden:
		return http.StatusForbidden
	case model.CodeNotFound:
		return http.StatusNotFound
	case model.CodeConflict:
		return http.StatusConflict
	case model.CodeRateLimited:
		return http.StatusTooManyRequests
	case model.CodeUnavailable:
		return http.StatusServiceUnavailable
	case model.CodeNotImplemented:
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}

// decodeJSON 严格解析请求体：拒绝未知字段、限制大小、拒绝多余 JSON 值。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if !isJSONContentType(ct) {
			return model.NewError(model.CodeInvalidArgument,
				"请求 Content-Type 必须是 application/json，实际为 %s", ct)
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return model.NewError(model.CodeInvalidArgument, "请求体超过 %d 字节上限", maxJSONBody)
		}
		return model.NewError(model.CodeInvalidArgument, "请求体解析失败: %v", err)
	}
	if dec.More() {
		return model.NewError(model.CodeInvalidArgument, "请求体包含多余内容")
	}
	return nil
}

func isJSONContentType(ct string) bool {
	// 只做前缀判断，忽略 charset 等参数。
	for i := 0; i < len(ct); i++ {
		if ct[i] == ';' {
			ct = ct[:i]
			break
		}
	}
	ct = trimSpace(ct)
	return ct == "application/json" || ct == "application/json-patch+json"
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
