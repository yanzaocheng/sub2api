package conversation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatForRoute(t *testing.T) {
	cases := map[string]Format{
		"/v1/messages":                            FormatAnthropic,
		"/antigravity/v1/messages":                FormatAnthropic,
		"/v1/chat/completions":                    FormatOpenAIChat,
		"/chat/completions":                       FormatOpenAIChat,
		"/v1/responses":                           FormatOpenAIResponses,
		"/responses":                              FormatOpenAIResponses,
		"/backend-api/codex/responses":            FormatOpenAIResponses,
		"/v1beta/models/*modelAction":             FormatGemini,
		"/antigravity/v1beta/models/*modelAction": FormatGemini,
	}
	for path, want := range cases {
		got, ok := FormatForRoute(path)
		require.True(t, ok, path)
		require.Equal(t, want, got, path)
	}

	// 这些路由不承载聊天对话，不应被记录。
	for _, path := range []string{
		"/v1/messages/count_tokens",
		"/messages/count_tokens",
		"/v1/responses/*subpath",
		"/v1/embeddings",
		"/v1/images/generations",
		"/v1/models",
		"/api/v1/admin/users",
		"",
	} {
		_, ok := FormatForRoute(path)
		require.False(t, ok, path)
	}
}

func TestGeminiModelAction(t *testing.T) {
	model, action, ok := GeminiModelAction("/gemini-2.5-pro:streamGenerateContent")
	require.Equal(t, "gemini-2.5-pro", model)
	require.Equal(t, "streamGenerateContent", action)
	require.True(t, ok)

	model, action, ok = GeminiModelAction("/gemini-2.5-flash:generateContent")
	require.Equal(t, "gemini-2.5-flash", model)
	require.Equal(t, "generateContent", action)
	require.True(t, ok)

	_, action, ok = GeminiModelAction("/gemini-2.5-pro:countTokens")
	require.Equal(t, "countTokens", action)
	require.False(t, ok)

	model, action, ok = GeminiModelAction("/models-without-action")
	require.Equal(t, "models-without-action", model)
	require.Equal(t, "", action)
	require.False(t, ok)
}

func TestKey(t *testing.T) {
	base := Key(1, 10, "sys", "hello", "req-1")
	require.Len(t, base, 32)

	// 同一场对话的不同轮次（开头相同）得到同一个键，与请求 ID 无关。
	require.Equal(t, base, Key(1, 10, "sys", "hello", "req-2"))

	// 用户、API Key、系统提示词、首条消息任一不同，都分到不同的对话。
	require.NotEqual(t, base, Key(2, 10, "sys", "hello", "req-1"))
	require.NotEqual(t, base, Key(1, 11, "sys", "hello", "req-1"))
	require.NotEqual(t, base, Key(1, 10, "other", "hello", "req-1"))
	require.NotEqual(t, base, Key(1, 10, "sys", "hello!", "req-1"))

	// 没有首条用户文本时无法归组，用请求 ID 让每个请求独立成组。
	require.NotEqual(t, Key(1, 10, "sys", "", "req-1"), Key(1, 10, "sys", "", "req-2"))
	require.Equal(t, Key(1, 10, "sys", " ", "req-1"), Key(1, 10, "sys", " ", "req-1"))
}
