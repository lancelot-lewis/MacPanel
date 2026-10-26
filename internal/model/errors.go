package model

import (
	"errors"
	"fmt"
)

// Code 是跨层（Agent RPC / HTTP API / CLI）共用的错误码。
// 前端依据 Code 做稳定判断，不要依赖 Message 文本。
type Code string

const (
	CodeInvalidArgument Code = "invalid_argument"
	CodeUnauthorized    Code = "unauthorized"
	CodeForbidden       Code = "forbidden"
	CodeNotFound        Code = "not_found"
	CodeConflict        Code = "conflict"
	CodeRateLimited     Code = "rate_limited"
	CodeUnavailable     Code = "unavailable"
	CodeNotImplemented  Code = "not_implemented"
	CodeInternal        Code = "internal"
)

// Error 是带机器可读错误码的业务错误。
// Details 只放非敏感的结构化补充信息（例如字段名、命令退出码）。
type Error struct {
	Code    Code           `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// NewError 构造业务错误。
func NewError(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WithDetail 追加一条结构化补充信息，返回自身便于链式使用。
func (e *Error) WithDetail(key string, value any) *Error {
	if e.Details == nil {
		e.Details = make(map[string]any, 2)
	}
	e.Details[key] = value
	return e
}

// AsError 把任意 error 归一化为 *Error，未知错误统一落到 internal。
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var target *Error
	if errors.As(err, &target) {
		return target
	}
	return &Error{Code: CodeInternal, Message: err.Error()}
}

// CodeOf 返回错误码，nil 返回空串。
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	return AsError(err).Code
}

// Unavailable 构造一个「依赖当前不可用」的错误。
// 用于 PF 状态、系统指标等读取失败场景：宁可报告不可用，也不伪造正常值。
func Unavailable(format string, args ...any) *Error {
	return NewError(CodeUnavailable, format, args...)
}

// NotImplemented 构造未实现错误。M1 阶段不允许伪造成功，只能返回它。
func NotImplemented(feature string) *Error {
	return NewError(CodeNotImplemented, "功能尚未实现：%s", feature).
		WithDetail("feature", feature)
}
