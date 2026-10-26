package services

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// PlistInfo 是从 plist 中提取的服务配置摘要。
//
// 只取「展示与安全判断需要」的字段，不做完整 plist 解析：
// 完整解析需要处理 date/data 等二进制类型，而我们只关心
// 「这个服务要执行什么、什么时候启动、日志在哪」。
type PlistInfo struct {
	Label             string   `json:"label"`
	Program           string   `json:"program,omitempty"`
	ProgramArguments  []string `json:"programArguments,omitempty"`
	RunAtLoad         bool     `json:"runAtLoad"`
	KeepAlive         bool     `json:"keepAlive"`
	Disabled          bool     `json:"disabled"`
	StandardOutPath   string   `json:"standardOutPath,omitempty"`
	StandardErrorPath string   `json:"standardErrorPath,omitempty"`
	WorkingDirectory  string   `json:"workingDirectory,omitempty"`
}

// Executable 返回实际会被执行的程序路径。
//
// ProgramArguments 首项优先：launchd 的语义是「有 ProgramArguments
// 就用它，否则用 Program」。展示时给出真正会跑的那个，
// 而不是 plist 里恰好存在的那一个。
func (p PlistInfo) Executable() string {
	if len(p.ProgramArguments) > 0 {
		return p.ProgramArguments[0]
	}
	return p.Program
}

// ParsePlist 解析 plist XML，提取已知字段。
//
// 遇到未知的键会跳过对应的值（可能是嵌套 dict 或 array），
// 因此对 StartCalendarInterval、EnvironmentVariables 这类
// 复杂结构不会报错——它们对本应用的展示没有价值。
func ParsePlist(data []byte) (PlistInfo, error) {
	dec := xml.NewDecoder(bytesReader(data))

	var info PlistInfo

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return info, fmt.Errorf("解析 plist 失败: %w", err)
		}

		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "dict" {
			continue
		}

		// 进入顶层 dict。
		if err := parseDict(dec, &info); err != nil {
			return info, err
		}
		return info, nil
	}

	return info, fmt.Errorf("plist 中未找到顶层 dict")
}

// parseDict 解析一个 dict 的键值对，直到遇到 </dict>。
func parseDict(dec *xml.Decoder, info *PlistInfo) error {
	var pendingKey string

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return fmt.Errorf("plist 结构不完整：dict 未闭合")
		}
		if err != nil {
			return fmt.Errorf("解析 plist 失败: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "key":
				var key string
				if err := dec.DecodeElement(&key, &t); err != nil {
					return fmt.Errorf("读取 key 失败: %w", err)
				}
				pendingKey = key

			case "string":
				var v string
				if err := dec.DecodeElement(&v, &t); err != nil {
					return fmt.Errorf("读取 string 失败: %w", err)
				}
				applyString(info, pendingKey, v)
				pendingKey = ""

			case "true", "false":
				v := t.Name.Local == "true"
				applyBool(info, pendingKey, v)
				pendingKey = ""
				// 自闭合元素没有内容，这里不需要 DecodeElement。

			case "integer":
				var raw string
				if err := dec.DecodeElement(&raw, &t); err != nil {
					return fmt.Errorf("读取 integer 失败: %w", err)
				}
				// 整数字段目前都不参与展示，跳过。
				pendingKey = ""

			case "array":
				if pendingKey == "ProgramArguments" {
					arr, err := parseStringArray(dec)
					if err != nil {
						return err
					}
					info.ProgramArguments = arr
				} else if err := skipElement(dec, t); err != nil {
					return err
				}
				pendingKey = ""

			case "dict":
				// 嵌套 dict（如 KeepAlive 的详细条件）。
				// KeepAlive 为 dict 时语义是「按条件保活」，等价于开启保活。
				if pendingKey == "KeepAlive" {
					info.KeepAlive = true
				}
				if err := skipElement(dec, t); err != nil {
					return err
				}
				pendingKey = ""

			default:
				// date / data / real 等类型：跳过内容。
				if err := skipElement(dec, t); err != nil {
					return err
				}
				pendingKey = ""
			}

		case xml.EndElement:
			if t.Name.Local == "dict" {
				return nil
			}
		}
	}
}

// parseStringArray 读取 <array> 内的所有 <string>。
func parseStringArray(dec *xml.Decoder) ([]string, error) {
	var out []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, fmt.Errorf("plist 结构不完整：array 未闭合")
		}
		if err != nil {
			return out, fmt.Errorf("解析 array 失败: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "string" {
				var v string
				if err := dec.DecodeElement(&v, &t); err != nil {
					return out, fmt.Errorf("读取数组元素失败: %w", err)
				}
				out = append(out, v)
				continue
			}
			if err := skipElement(dec, t); err != nil {
				return out, err
			}
		case xml.EndElement:
			if t.Name.Local == "array" {
				return out, nil
			}
		}
	}
}

// skipElement 跳过当前元素及其全部子元素。
func skipElement(dec *xml.Decoder, start xml.StartElement) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err == io.EOF {
			return fmt.Errorf("plist 结构不完整：%s 未闭合", start.Name.Local)
		}
		if err != nil {
			return fmt.Errorf("跳过元素失败: %w", err)
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

// applyString 把字符串值写入对应字段。
func applyString(info *PlistInfo, key, value string) {
	switch key {
	case "Label":
		info.Label = value
	case "Program":
		info.Program = value
	case "StandardOutPath":
		info.StandardOutPath = value
	case "StandardErrorPath":
		info.StandardErrorPath = value
	case "WorkingDirectory":
		info.WorkingDirectory = value
	}
}

// applyBool 把布尔值写入对应字段。
func applyBool(info *PlistInfo, key string, value bool) {
	switch key {
	case "RunAtLoad":
		info.RunAtLoad = value
	case "KeepAlive":
		info.KeepAlive = value
	case "Disabled":
		info.Disabled = value
	}
}

// bytesReader 是 strings.Reader 的轻包装，便于替换为 bytes.Reader。
func bytesReader(b []byte) *strings.Reader {
	return strings.NewReader(string(b))
}
