package conversation

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestClean(t *testing.T) {
	require.Equal(t, "", Clean(""))
	require.Equal(t, "hello", Clean("hello"))
	require.Equal(t, "ab", Clean("a\x00b"))
	// 非法 UTF-8 字节被替换，结果必须是合法 UTF-8，PostgreSQL 才能存下。
	cleaned := Clean("ok\xff\xfeend")
	require.True(t, utf8.ValidString(cleaned))
	require.Contains(t, cleaned, "ok")
	require.Contains(t, cleaned, "end")
}

func TestTruncate(t *testing.T) {
	require.Equal(t, "", Truncate("abc", 0))
	require.Equal(t, "abc", Truncate("abc", 3))
	require.Equal(t, "abc…[truncated 2 chars]", Truncate("abcde", 3))

	// 按字符而不是字节截断，多字节字符不会被切坏。
	cjk := strings.Repeat("中", 10)
	got := Truncate(cjk, 4)
	require.Equal(t, "中中中中…[truncated 6 chars]", got)
	require.True(t, utf8.ValidString(got))

	// 字节数超过上限但字符数没有超过时保持原样。
	require.Equal(t, "中中中", Truncate("中中中", 3))
}

func TestPreview(t *testing.T) {
	require.Equal(t, "", Preview("", 10))
	require.Equal(t, "a b c", Preview("  a \n\t b\r\n c  ", 10))
	require.Equal(t, "hello…", Preview("hello world", 5))
	require.Equal(t, "你好世…", Preview("你好世界", 3))

	// 超长输入只看有界前缀，结果依旧合法且长度受控。
	long := Preview(strings.Repeat("字", 100000), 50)
	require.True(t, utf8.ValidString(long))
	require.Equal(t, 51, utf8.RuneCountInString(long))
}
