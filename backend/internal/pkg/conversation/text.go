// Package conversation 把网关上各协议（Anthropic / OpenAI Chat / OpenAI Responses /
// Gemini）的请求与响应报文归一化为可读的对话文本，用于「对话记录」落库。
//
// 本包只做纯函数解析，不依赖 gin / 数据库；所有输出都有大小上限，
// 单条记录的体积因此与上游报文大小（工具定义、base64 图片等）无关。
package conversation

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// 单条对话记录各字段的体积上限（按字符计）。超出部分截断并附带可见标记。
const (
	MaxSystemChars     = 2000
	MaxMessageChars    = 20000
	MaxRequestChars    = 60000
	MaxResponseChars   = 60000
	MaxToolResultChars = 600
	MaxToolInputChars  = 300
	PreviewChars       = 200

	// maxKeyChars 参与会话分组哈希的文本长度上限。
	maxKeyChars = 2000
)

// Clean 去掉 PostgreSQL 文本 / jsonb 列无法存储的内容：NUL 字符，以及非法 UTF-8。
func Clean(s string) string {
	if s == "" {
		return s
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return s
}

// Truncate 把 s 截断到最多 maxChars 个字符，并在末尾标注被丢弃的字符数。
func Truncate(s string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	// 字节数不超过上限时字符数一定不超过，免去逐字符计数。
	if len(s) <= maxChars {
		return s
	}
	total := utf8.RuneCountInString(s)
	if total <= maxChars {
		return s
	}
	return s[:runeOffset(s, maxChars)] + truncationMarker(total-maxChars)
}

// Preview 生成列表页使用的单行摘要：折叠空白，最多 maxChars 个字符。
func Preview(s string, maxChars int) string {
	if maxChars <= 0 || s == "" {
		return ""
	}
	// 折叠空白只会缩短文本，因此只需要看一个有界的前缀。
	if limit := maxChars * 8; len(s) > limit {
		s = strings.ToValidUTF8(s[:limit], "")
	}
	joined := strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(joined) <= maxChars {
		return joined
	}
	return joined[:runeOffset(joined, maxChars)] + "…"
}

// runeOffset 返回 s 中第 n 个字符起始处的字节偏移（n 不超过字符总数）。
func runeOffset(s string, n int) int {
	offset := 0
	for i := 0; i < n && offset < len(s); i++ {
		_, size := utf8.DecodeRuneInString(s[offset:])
		offset += size
	}
	return offset
}

func truncationMarker(dropped int) string {
	return "…[truncated " + strconv.Itoa(dropped) + " chars]"
}

// joinNonEmpty 用 sep 连接非空片段。
func joinNonEmpty(sep string, parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, sep)
}
