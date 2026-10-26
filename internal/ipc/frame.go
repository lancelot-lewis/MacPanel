package ipc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// writeFrame 写入长度前缀帧。payload 长度必须已校验。
func writeFrame(w io.Writer, payload []byte, limit int) error {
	if len(payload) > limit {
		return &FrameTooLargeError{Limit: limit, Got: len(payload)}
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	// 一次 Write 组装头+体，减少系统调用与半包问题。
	buf := make([]byte, 4+len(payload))
	copy(buf, header[:])
	copy(buf[4:], payload)
	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("ipc: 写入帧失败: %w", err)
	}
	return nil
}

// readFrame 读取长度前缀帧，并强制长度上限。
// 先读长度再按长度分配内存，避免被超长声明拖垮。
func readFrame(r io.Reader, limit int) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 {
		return nil, fmt.Errorf("%w: 空帧", ErrInvalidRequest)
	}
	if int(n) > limit {
		return nil, &FrameTooLargeError{Limit: limit, Got: int(n)}
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("%w: 帧被截断", ErrInvalidRequest)
		}
		return nil, err
	}
	return buf, nil
}

// decodeStrict 使用 DisallowUnknownFields 严格解码，拒绝多余字段。
// 这样拼错的字段名不会被静默忽略。
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	return nil
}
