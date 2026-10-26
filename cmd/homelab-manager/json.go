package main

import (
	"encoding/json"
	"io"
)

// newJSONEncoder 统一 JSON 输出格式（带缩进，便于人工阅读）。
func newJSONEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc
}
