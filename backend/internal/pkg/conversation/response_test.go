package conversation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const sseType = "text/event-stream"

func TestParseResponse_Empty(t *testing.T) {
	require.Equal(t, Response{}, ParseResponse(FormatAnthropic, nil, ""))
	require.Equal(t, Response{}, ParseResponse(FormatAnthropic, []byte("  \n"), "application/json"))
	require.Equal(t, Response{}, ParseResponse(FormatAnthropic, []byte("<html>bad gateway</html>"), "text/html"))
}

func TestParseResponse_AnthropicJSON(t *testing.T) {
	body := `{"id":"msg_1","type":"message","role":"assistant","content":[
		{"type":"thinking","thinking":"secret reasoning","signature":"s"},
		{"type":"text","text":"Let me check."},
		{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/a.go"}},
		{"type":"text","text":"Done."}],"usage":{"input_tokens":3,"output_tokens":9}}`
	got := ParseResponse(FormatAnthropic, []byte(body), "application/json")

	require.Equal(t, "Let me check.\n[tool_use: Read] {\"file_path\":\"/a.go\"}\nDone.", got.Text)
	require.NotContains(t, got.Text, "secret reasoning")
}

func TestParseResponse_AnthropicSSE(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","content":[],"usage":{"input_tokens":5}}}`,
		``,
		`event: ping`,
		`data: {"type":"ping"}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"private"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hel"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"lo, 世界"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"t1","name":"Bash","input":{}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":2}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	got := ParseResponse(FormatAnthropic, []byte(body), sseType)

	require.Equal(t, "Hello, 世界\n[tool_use: Bash] {\"command\":\"ls\"}", got.Text)
	require.NotContains(t, got.Text, "private")
}

// CRLF 行尾、被截断的尾帧、尚未结束的工具调用都不能让解析出错。
func TestParseResponse_AnthropicSSETruncated(t *testing.T) {
	body := "event: content_block_start\r\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\r\n\r\n" +
		"event: content_block_delta\r\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial answer\"}}\r\n\r\n" +
		"event: content_block_start\r\n" +
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"name\":\"Edit\",\"input\":{}}}\r\n\r\n" +
		"event: content_block_delta\r\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"a\\\":1}\"}}\r\n\r\n" +
		"event: content_block_delta\r\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"typ"
	got := ParseResponse(FormatAnthropic, []byte(body), sseType)

	require.Equal(t, "partial answer\n[tool_use: Edit] {\"a\":1}", got.Text)
}

func TestParseResponse_ChatJSON(t *testing.T) {
	body := `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Sunny.","reasoning_content":"hidden","tool_calls":[{"id":"x","type":"function","function":{"name":"log","arguments":"{\"ok\":true}"}}]},"finish_reason":"stop"}]}`
	got := ParseResponse(FormatOpenAIChat, []byte(body), "application/json")

	require.Equal(t, "Sunny.\n[tool_use: log] {\"ok\":true}", got.Text)
	require.NotContains(t, got.Text, "hidden")
}

func TestParseResponse_ChatSSE(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"content":"Hel"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"thinking"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"content":"lo"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"search","arguments":""}}]}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"go\"}"}}]}}]}`,
		``,
		`data: {"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	got := ParseResponse(FormatOpenAIChat, []byte(body), sseType)

	require.Equal(t, "Hello\n[tool_use: search] {\"q\":\"go\"}", got.Text)
}

func TestParseResponse_ResponsesJSON(t *testing.T) {
	body := `{"id":"resp_1","output":[
		{"type":"reasoning","summary":[]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Here you go."}]},
		{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}","call_id":"c1"}]}`
	got := ParseResponse(FormatOpenAIResponses, []byte(body), "application/json")

	require.Equal(t, "Here you go.\n[tool_use: shell] {\"cmd\":\"ls\"}", got.Text)
}

func TestParseResponse_ResponsesSSEUsesCompletedOutput(t *testing.T) {
	body := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"stream"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"final text"}]}]}}`,
		``,
	}, "\n")
	got := ParseResponse(FormatOpenAIResponses, []byte(body), sseType)

	require.Equal(t, "final text", got.Text)
}

// 部分上游的 response.completed 事件里 output 为空，此时必须退回到流式增量，否则回复会丢失。
func TestParseResponse_ResponsesSSEFallsBackToDeltas(t *testing.T) {
	body := strings.Join([]string{
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","content":[]}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"Hello "}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"world"}`,
		``,
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","name":"shell","arguments":""}}`,
		``,
		`event: response.function_call_arguments.delta`,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"cmd\":"}`,
		``,
		`event: response.function_call_arguments.delta`,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"\"pwd\"}"}`,
		``,
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"pwd\"}"}}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"output":[]}}`,
		``,
	}, "\n")
	got := ParseResponse(FormatOpenAIResponses, []byte(body), sseType)

	require.Equal(t, "Hello world\n[tool_use: shell] {\"cmd\":\"pwd\"}", got.Text)
}

func TestParseResponse_GeminiJSON(t *testing.T) {
	body := `{"candidates":[{"content":{"role":"model","parts":[
		{"text":"hidden thought","thought":true},
		{"text":"Go is a language."},
		{"functionCall":{"name":"lookup","args":{"q":"go"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2}}`
	got := ParseResponse(FormatGemini, []byte(body), "application/json")

	require.Equal(t, "Go is a language.\n[tool_use: lookup] {\"q\":\"go\"}", got.Text)
}

func TestParseResponse_GeminiSSE(t *testing.T) {
	body := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}]}`,
		``,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":", world"}]}}],"usageMetadata":{"promptTokenCount":2}}`,
		``,
	}, "\n")
	got := ParseResponse(FormatGemini, []byte(body), sseType)

	require.Equal(t, "Hello, world", got.Text)
}

// 未启用 alt=sse 时，Gemini 的流式响应是 JSON 数组；有的上游还会多包一层 response。
func TestParseResponse_GeminiJSONArrayAndWrapper(t *testing.T) {
	array := `[{"candidates":[{"content":{"parts":[{"text":"Hi "}]}}]},{"candidates":[{"content":{"parts":[{"text":"there"}]}}]}]`
	require.Equal(t, "Hi there", ParseResponse(FormatGemini, []byte(array), "application/json").Text)

	wrapped := `{"response":{"candidates":[{"content":{"parts":[{"text":"wrapped"}]}}]}}`
	require.Equal(t, "wrapped", ParseResponse(FormatGemini, []byte(wrapped), "application/json").Text)
}

// 没有任何正文的响应（例如只有思考或错误事件）解析为空，调用方据此跳过记录。
func TestParseResponse_NoVisibleContent(t *testing.T) {
	body := `event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}

`
	require.Empty(t, ParseResponse(FormatAnthropic, []byte(body), sseType).Text)
}

func TestParseResponse_TextCap(t *testing.T) {
	body := `{"content":[{"type":"text","text":"` + strings.Repeat("a", MaxResponseChars+250) + `"}]}`
	got := ParseResponse(FormatAnthropic, []byte(body), "application/json")

	require.Equal(t, strings.Repeat("a", MaxResponseChars)+"…[truncated 250 chars]", got.Text)
}

func TestParseResponse_SniffsSSEWithoutContentType(t *testing.T) {
	body := `data: {"choices":[{"delta":{"content":"ok"}}]}

data: [DONE]
`
	require.Equal(t, "ok", ParseResponse(FormatOpenAIChat, []byte(body), "").Text)
}
