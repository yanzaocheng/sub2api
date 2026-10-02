package conversation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRequest_InvalidInput(t *testing.T) {
	require.Equal(t, Request{}, ParseRequest(FormatAnthropic, nil))
	require.Equal(t, Request{}, ParseRequest(FormatAnthropic, []byte("not json")))
	require.Equal(t, Request{}, ParseRequest(FormatAnthropic, []byte(`[1,2]`)))
	require.Equal(t, Request{}, ParseRequest(Format("unknown"), []byte(`{"model":"x"}`)))
}

func TestParseRequest_AnthropicSingleTurn(t *testing.T) {
	body := `{"model":"claude-sonnet-4-5","stream":true,"max_tokens":1024,
		"system":"You are helpful.",
		"messages":[{"role":"user","content":"Hello there"}]}`
	req := ParseRequest(FormatAnthropic, []byte(body))

	require.Equal(t, "claude-sonnet-4-5", req.Model)
	require.True(t, req.Stream)
	require.Equal(t, "You are helpful.", req.System)
	require.Equal(t, "Hello there", req.FirstUser)
	require.Equal(t, []Message{{Role: "user", Content: "Hello there"}}, req.Messages)
}

// 多轮对话只保存最后一条助手消息之后的新输入，历史由此前的记录保存。
func TestParseRequest_AnthropicKeepsOnlyNewInput(t *testing.T) {
	body := `{"model":"m","messages":[
		{"role":"user","content":"first question"},
		{"role":"assistant","content":"first answer"},
		{"role":"user","content":"second question"},
		{"role":"assistant","content":"second answer"},
		{"role":"user","content":"third question"}]}`
	req := ParseRequest(FormatAnthropic, []byte(body))

	require.Equal(t, "first question", req.FirstUser)
	require.Equal(t, []Message{{Role: "user", Content: "third question"}}, req.Messages)
}

// Claude Code 一类的 Agent 客户端：system 是块数组，工具结果与文本混在同一条 user 消息里。
func TestParseRequest_AnthropicAgentTurn(t *testing.T) {
	body := `{"model":"claude-opus-4-1","tools":[{"name":"Read","input_schema":{"type":"object"}}],
		"system":[{"type":"text","text":"You are Claude Code."},{"type":"text","text":"Be brief.","cache_control":{"type":"ephemeral"}}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"<system-reminder>ctx</system-reminder>"},{"type":"text","text":"fix the bug"}]},
			{"role":"assistant","content":[{"type":"thinking","thinking":"hmm","signature":"s"},{"type":"text","text":"Let me look."},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/a.go"}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"package main"}]},
				{"type":"tool_result","tool_use_id":"t2","content":"second result"},
				{"type":"text","text":"continue please"}]}]}`
	req := ParseRequest(FormatAnthropic, []byte(body))

	require.Equal(t, "You are Claude Code.\n\nBe brief.", req.System)
	require.Equal(t, "<system-reminder>ctx</system-reminder>\nfix the bug", req.FirstUser)
	require.Equal(t, []Message{
		{Role: "tool", Content: "package main"},
		{Role: "tool", Content: "second result"},
		{Role: "user", Content: "continue please"},
	}, req.Messages)
}

func TestParseRequest_AnthropicPlaceholders(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":[
		{"type":"text","text":"what is this?"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},
		{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"BBBB"}}]}]}`
	req := ParseRequest(FormatAnthropic, []byte(body))

	require.Equal(t, []Message{{Role: "user", Content: "what is this?\n[image]\n[document]"}}, req.Messages)
	// 图片的 base64 内容绝不能进入记录。
	require.NotContains(t, req.Messages[0].Content, "AAAA")
}

// 请求以助手消息（预填）结尾时，退化为只记录最后一条。
func TestParseRequest_AnthropicAssistantPrefill(t *testing.T) {
	body := `{"model":"m","messages":[
		{"role":"user","content":"write json"},
		{"role":"assistant","content":"{"}]}`
	req := ParseRequest(FormatAnthropic, []byte(body))

	require.Equal(t, "write json", req.FirstUser)
	require.Equal(t, []Message{{Role: "assistant", Content: "{"}}, req.Messages)
}

func TestParseRequest_ChatCompletions(t *testing.T) {
	body := `{"model":"gpt-5","stream":false,"messages":[
		{"role":"system","content":"Be concise."},
		{"role":"developer","content":[{"type":"text","text":"Reply in Chinese."}]},
		{"role":"user","content":"hi"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SZ\"}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"sunny"},
		{"role":"user","content":[{"type":"text","text":"and tomorrow?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`
	req := ParseRequest(FormatOpenAIChat, []byte(body))

	require.Equal(t, "gpt-5", req.Model)
	require.False(t, req.Stream)
	require.Equal(t, "Be concise.\n\nReply in Chinese.", req.System)
	require.Equal(t, "hi", req.FirstUser)
	// 工具结果和随后的追问都排在最后一条助手消息之后，同属本轮新输入。
	require.Equal(t, []Message{
		{Role: "tool", Content: "sunny"},
		{Role: "user", Content: "and tomorrow?\n[image]"},
	}, req.Messages)
}

func TestParseRequest_ChatCompletionsToolStep(t *testing.T) {
	body := `{"model":"m","messages":[
		{"role":"user","content":"weather?"},
		{"role":"assistant","content":"","tool_calls":[{"id":"c1","function":{"name":"get_weather","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"sunny"}]}`
	req := ParseRequest(FormatOpenAIChat, []byte(body))

	require.Equal(t, []Message{{Role: "tool", Content: "sunny"}}, req.Messages)
}

func TestParseRequest_ResponsesStringInput(t *testing.T) {
	req := ParseRequest(FormatOpenAIResponses, []byte(`{"model":"gpt-5","instructions":"Be nice.","input":"hello","stream":true}`))

	require.Equal(t, "gpt-5", req.Model)
	require.True(t, req.Stream)
	require.Equal(t, "Be nice.", req.System)
	require.Equal(t, "hello", req.FirstUser)
	require.Equal(t, []Message{{Role: "user", Content: "hello"}}, req.Messages)
}

func TestParseRequest_ResponsesItems(t *testing.T) {
	body := `{"model":"gpt-5","instructions":"base rules","input":[
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"dev rules"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"run ls"}]},
		{"type":"reasoning","summary":[]},
		{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}","call_id":"c1"},
		{"type":"function_call_output","call_id":"c1","output":"a.txt\nb.txt"},
		{"type":"item_reference","id":"x"},
		{"role":"user","content":"thanks"}]}`
	req := ParseRequest(FormatOpenAIResponses, []byte(body))

	require.Equal(t, "base rules\n\ndev rules", req.System)
	require.Equal(t, "run ls", req.FirstUser)
	require.Equal(t, []Message{
		{Role: "tool", Content: "a.txt\nb.txt"},
		{Role: "user", Content: "thanks"},
	}, req.Messages)
}

func TestParseRequest_Gemini(t *testing.T) {
	body := `{"systemInstruction":{"parts":[{"text":"Answer in one line."}]},
		"contents":[
			{"role":"user","parts":[{"text":"what is Go?"}]},
			{"role":"model","parts":[{"text":"A language."},{"functionCall":{"name":"lookup","args":{"q":"go"}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"result":"ok"}}}]},
			{"role":"user","parts":[{"text":"more detail"},{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}]}`
	req := ParseRequest(FormatGemini, []byte(body))

	require.Equal(t, "Answer in one line.", req.System)
	require.Equal(t, "what is Go?", req.FirstUser)
	require.Equal(t, []Message{
		{Role: "tool", Content: `{"result":"ok"}`},
		{Role: "user", Content: "more detail\n[image]"},
	}, req.Messages)
}

func TestParseRequest_GeminiSnakeCase(t *testing.T) {
	body := `{"system_instruction":{"parts":[{"text":"sys"}]},
		"contents":[{"role":"user","parts":[{"text":"hi"},{"inline_data":{"mime_type":"audio/wav","data":"AAAA"}}]}]}`
	req := ParseRequest(FormatGemini, []byte(body))

	require.Equal(t, "sys", req.System)
	require.Equal(t, []Message{{Role: "user", Content: "hi\n[audio]"}}, req.Messages)
}

func TestParseRequest_BudgetsAndCleaning(t *testing.T) {
	long := strings.Repeat("字", MaxMessageChars+500)
	body := `{"model":"m","system":"` + strings.Repeat("s", MaxSystemChars+100) + `",
		"messages":[{"role":"user","content":"` + long + `"}]}`
	req := ParseRequest(FormatAnthropic, []byte(body))

	require.Len(t, req.Messages, 1)
	require.Contains(t, req.Messages[0].Content, "…[truncated 500 chars]")
	require.Contains(t, req.System, "…[truncated 100 chars]")

	// 控制字符里的 NUL 会让 PostgreSQL 拒绝整行，必须在入库前清除。
	nul := ParseRequest(FormatAnthropic, []byte(`{"model":"m","messages":[{"role":"user","content":"a\u0000b"}]}`))
	require.Equal(t, "ab", nul.Messages[0].Content)
}

// 单轮新增输入超出总预算时，保留最近的消息，并提示有更早的被省略。
func TestParseRequest_TotalBudgetKeepsRecent(t *testing.T) {
	chunk := strings.Repeat("x", MaxMessageChars)
	var sb strings.Builder
	sb.WriteString(`{"model":"m","messages":[`)
	for i := 0; i < 5; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"role":"user","content":"` + chunk[:MaxMessageChars-1] + string(rune('a'+i)) + `"}`)
	}
	sb.WriteString(`]}`)
	req := ParseRequest(FormatAnthropic, []byte(sb.String()))

	// 5 条各约 2 万字符，总预算 6 万：只容得下最近 3 条。
	require.Len(t, req.Messages, 4)
	require.Equal(t, "note", req.Messages[0].Role)
	require.Equal(t, "[2 earlier messages omitted]", req.Messages[0].Content)
	require.True(t, strings.HasSuffix(req.Messages[3].Content, "e"))
	require.True(t, strings.HasSuffix(req.Messages[1].Content, "c"))
}

func TestParseRequest_EmptyContentDropped(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"   "}]}`
	req := ParseRequest(FormatAnthropic, []byte(body))
	require.Empty(t, req.Messages)
	require.Equal(t, "m", req.Model)
}
