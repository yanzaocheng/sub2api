package conversation

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// Format 是网关对外暴露的报文协议（以客户端看到的格式为准，与上游平台无关）。
type Format string

const (
	FormatAnthropic       Format = "anthropic"
	FormatOpenAIChat      Format = "openai_chat"
	FormatOpenAIResponses Format = "openai_responses"
	FormatGemini          Format = "gemini"
)

// Message 是归一化后的一条对话消息。Role 取值：user / assistant / system / tool / note。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request 是从请求体提取的本轮对话信息。
type Request struct {
	Model  string
	Stream bool
	// System 系统提示词（已截断）。
	System string
	// FirstUser 对话中第一条用户消息的文本（已截断），仅用于会话分组。
	FirstUser string
	// Messages 本轮新增的输入，即最后一条助手消息之后的所有消息。
	// 更早的历史已由之前的记录保存，不在每一轮重复落库。
	Messages []Message
}

// ParseRequest 按协议解析请求体。解析失败时返回零值，调用方据此跳过记录。
func ParseRequest(format Format, body []byte) Request {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return Request{}
	}
	var req Request
	switch format {
	case FormatAnthropic:
		req = parseAnthropicRequest(root)
	case FormatOpenAIChat:
		req = parseChatRequest(root)
	case FormatOpenAIResponses:
		req = parseResponsesRequest(root)
	case FormatGemini:
		req = parseGeminiRequest(root)
	default:
		return Request{}
	}
	return finalizeRequest(req)
}

// msgKind 标记一条消息在「新输入」划分中的角色。
type msgKind int

const (
	kindSkip      msgKind = iota // 不属于对话正文（系统消息等，由调用方另行收集）
	kindUser                     // 用户说的话
	kindInput                    // 工具结果等：属于输入，但不是用户说的话
	kindAssistant                // 助手一侧：新输入从其之后开始
)

// collectTurn 遍历消息数组，返回第一条用户消息，以及最后一条助手消息之后的全部消息。
// 数组以助手消息结尾（预填）时，退化为只取最后一条消息。
func collectTurn(arr gjson.Result, visit func(m gjson.Result) msgKind) (firstUser gjson.Result, trailing []gjson.Result) {
	var last gjson.Result
	arr.ForEach(func(_, m gjson.Result) bool {
		switch visit(m) {
		case kindAssistant:
			trailing = trailing[:0]
			last = m
		case kindUser:
			if !firstUser.Exists() {
				firstUser = m
			}
			trailing = append(trailing, m)
			last = m
		case kindInput:
			trailing = append(trailing, m)
			last = m
		}
		return true
	})
	if len(trailing) == 0 && last.Exists() {
		trailing = append(trailing, last)
	}
	return firstUser, trailing
}

// topLevel 单次扫描取出对象的若干顶层字段（按 keys 的顺序返回，缺失为零值）。
func topLevel(root gjson.Result, keys ...string) []gjson.Result {
	found := make([]gjson.Result, len(keys))
	remaining := len(keys)
	root.ForEach(func(k, v gjson.Result) bool {
		name := k.String()
		for i, key := range keys {
			if name == key && !found[i].Exists() {
				found[i] = v
				remaining--
				break
			}
		}
		return remaining > 0
	})
	return found
}

// userText 取一组消息中用户所说的文本。
func userText(msgs []Message) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "user" {
			parts = append(parts, m.Content)
		}
	}
	return joinNonEmpty("\n", parts...)
}

// ---- Anthropic /v1/messages ----

func parseAnthropicRequest(root gjson.Result) Request {
	f := topLevel(root, "model", "stream", "system", "messages")
	req := Request{
		Model:  f[0].String(),
		Stream: f[1].Bool(),
		System: anthropicBlocksText(f[2]),
	}
	first, trailing := collectTurn(f[3], func(m gjson.Result) msgKind {
		if m.Get("role").String() == "assistant" {
			return kindAssistant
		}
		return kindUser
	})
	if first.Exists() {
		req.FirstUser = userText(anthropicMessages(first))
	}
	for _, m := range trailing {
		req.Messages = append(req.Messages, anthropicMessages(m)...)
	}
	return req
}

// anthropicBlocksText 取 system 这类「字符串或 text 块数组」字段的文本。
func anthropicBlocksText(v gjson.Result) string {
	if v.Type == gjson.String {
		return v.String()
	}
	if !v.IsArray() {
		return ""
	}
	var parts []string
	v.ForEach(func(_, b gjson.Result) bool {
		if b.Get("type").String() == "text" {
			parts = append(parts, b.Get("text").String())
		}
		return true
	})
	return joinNonEmpty("\n\n", parts...)
}

// anthropicMessages 把一条消息展开成若干归一化消息：
// 连续的文本 / 图片块合并为一条，tool_result 块单独成为 tool 消息。
func anthropicMessages(m gjson.Result) []Message {
	role := m.Get("role").String()
	content := m.Get("content")
	if content.Type == gjson.String {
		return []Message{{Role: role, Content: content.String()}}
	}
	var out []Message
	var text []string
	flush := func() {
		if len(text) > 0 {
			out = append(out, Message{Role: role, Content: strings.Join(text, "\n")})
			text = text[:0]
		}
	}
	content.ForEach(func(_, b gjson.Result) bool {
		switch typ := b.Get("type").String(); typ {
		case "text":
			text = append(text, b.Get("text").String())
		case "image":
			text = append(text, "[image]")
		case "document":
			text = append(text, "[document]")
		case "tool_result":
			flush()
			out = append(out, Message{Role: "tool", Content: Truncate(anthropicToolResultText(b.Get("content")), MaxToolResultChars)})
		case "tool_use", "server_tool_use":
			text = append(text, toolUseLine(b.Get("name").String(), b.Get("input").Raw))
		case "thinking", "redacted_thinking", "":
			// 思考内容不属于可见对话。
		default:
			text = append(text, "["+typ+"]")
		}
		return true
	})
	flush()
	return out
}

func anthropicToolResultText(v gjson.Result) string {
	if v.Type == gjson.String {
		return v.String()
	}
	if !v.IsArray() {
		return ""
	}
	var parts []string
	v.ForEach(func(_, b gjson.Result) bool {
		switch b.Get("type").String() {
		case "text":
			parts = append(parts, b.Get("text").String())
		case "image":
			parts = append(parts, "[image]")
		}
		return true
	})
	return strings.Join(parts, "\n")
}

// toolUseLine 渲染一次工具调用：名称加截断后的参数。
func toolUseLine(name, rawInput string) string {
	line := "[tool_use: " + name + "]"
	if input := strings.TrimSpace(rawInput); input != "" && input != "{}" {
		line += " " + Truncate(input, MaxToolInputChars)
	}
	return line
}

// ---- OpenAI /v1/chat/completions ----

func parseChatRequest(root gjson.Result) Request {
	f := topLevel(root, "model", "stream", "messages")
	req := Request{Model: f[0].String(), Stream: f[1].Bool()}
	var systems []string
	first, trailing := collectTurn(f[2], func(m gjson.Result) msgKind {
		switch m.Get("role").String() {
		case "system", "developer":
			systems = append(systems, chatContentText(m.Get("content")))
			return kindSkip
		case "assistant":
			return kindAssistant
		case "tool", "function":
			return kindInput
		default:
			return kindUser
		}
	})
	req.System = joinNonEmpty("\n\n", systems...)
	if first.Exists() {
		req.FirstUser = chatContentText(first.Get("content"))
	}
	for _, m := range trailing {
		req.Messages = append(req.Messages, chatMessage(m))
	}
	return req
}

func chatMessage(m gjson.Result) Message {
	role := m.Get("role").String()
	text := chatContentText(m.Get("content"))
	switch role {
	case "tool", "function":
		return Message{Role: "tool", Content: Truncate(text, MaxToolResultChars)}
	case "assistant":
		var lines []string
		m.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
			lines = append(lines, toolUseLine(tc.Get("function.name").String(), tc.Get("function.arguments").String()))
			return true
		})
		return Message{Role: role, Content: joinNonEmpty("\n", append([]string{text}, lines...)...)}
	case "system", "developer":
		return Message{Role: "system", Content: text}
	default:
		return Message{Role: "user", Content: text}
	}
}

// chatContentText 取 content 字段的文本：字符串，或 text / image 等类型化片段数组。
func chatContentText(c gjson.Result) string {
	if c.Type == gjson.String {
		return c.String()
	}
	if !c.IsArray() {
		return ""
	}
	var parts []string
	c.ForEach(func(_, p gjson.Result) bool {
		switch p.Get("type").String() {
		case "text", "input_text", "output_text":
			parts = append(parts, p.Get("text").String())
		case "refusal":
			parts = append(parts, p.Get("refusal").String())
		case "image_url", "input_image", "image":
			parts = append(parts, "[image]")
		case "input_audio", "audio":
			parts = append(parts, "[audio]")
		case "file", "input_file":
			parts = append(parts, "[file]")
		}
		return true
	})
	return joinNonEmpty("\n", parts...)
}

// ---- OpenAI /v1/responses ----

func parseResponsesRequest(root gjson.Result) Request {
	f := topLevel(root, "model", "stream", "instructions", "input")
	req := Request{
		Model:  f[0].String(),
		Stream: f[1].Bool(),
		System: f[2].String(),
	}
	input := f[3]
	if input.Type == gjson.String {
		req.FirstUser = input.String()
		req.Messages = []Message{{Role: "user", Content: input.String()}}
		return req
	}
	var systems []string
	first, trailing := collectTurn(input, func(item gjson.Result) msgKind {
		switch typ := item.Get("type").String(); {
		case typ == "" || typ == "message":
			switch item.Get("role").String() {
			case "assistant":
				return kindAssistant
			case "system", "developer":
				systems = append(systems, chatContentText(item.Get("content")))
				return kindSkip
			default:
				return kindUser
			}
		case typ == "item_reference":
			return kindSkip
		case strings.HasSuffix(typ, "_output"):
			return kindInput
		default:
			// function_call / reasoning / 各类内置工具调用，都是模型一侧产生的条目。
			return kindAssistant
		}
	})
	req.System = joinNonEmpty("\n\n", append([]string{req.System}, systems...)...)
	if first.Exists() {
		req.FirstUser = chatContentText(first.Get("content"))
	}
	for _, item := range trailing {
		req.Messages = append(req.Messages, responsesItemMessage(item))
	}
	return req
}

func responsesItemMessage(item gjson.Result) Message {
	typ := item.Get("type").String()
	switch {
	case typ == "" || typ == "message":
		role := item.Get("role").String()
		if role == "" {
			role = "user"
		}
		return Message{Role: role, Content: chatContentText(item.Get("content"))}
	case strings.HasSuffix(typ, "_output"):
		return Message{Role: "tool", Content: Truncate(responsesOutputText(item.Get("output")), MaxToolResultChars)}
	case typ == "function_call" || typ == "custom_tool_call":
		args := item.Get("arguments").String()
		if args == "" {
			args = item.Get("input").String()
		}
		return Message{Role: "assistant", Content: toolUseLine(item.Get("name").String(), args)}
	default:
		return Message{Role: "assistant", Content: "[" + typ + "]"}
	}
}

func responsesOutputText(v gjson.Result) string {
	if v.Type == gjson.String {
		return v.String()
	}
	return chatContentText(v)
}

// ---- Gemini generateContent ----

func parseGeminiRequest(root gjson.Result) Request {
	f := topLevel(root, "systemInstruction", "system_instruction", "contents")
	system := f[0]
	if !system.Exists() {
		system = f[1]
	}
	req := Request{Stream: false, System: geminiSystemText(system)}
	first, trailing := collectTurn(f[2], func(m gjson.Result) msgKind {
		switch m.Get("role").String() {
		case "model":
			return kindAssistant
		case "function", "tool":
			return kindInput
		default:
			if geminiHasOnlyFunctionResponse(m) {
				return kindInput
			}
			return kindUser
		}
	})
	if first.Exists() {
		req.FirstUser = userText(geminiMessages(first))
	}
	for _, m := range trailing {
		req.Messages = append(req.Messages, geminiMessages(m)...)
	}
	return req
}

func geminiSystemText(v gjson.Result) string {
	if v.Type == gjson.String {
		return v.String()
	}
	var parts []string
	v.Get("parts").ForEach(func(_, p gjson.Result) bool {
		parts = append(parts, p.Get("text").String())
		return true
	})
	return joinNonEmpty("\n\n", parts...)
}

func geminiPart(p gjson.Result, keys ...string) gjson.Result {
	for _, key := range keys {
		if v := p.Get(key); v.Exists() {
			return v
		}
	}
	return gjson.Result{}
}

func geminiHasOnlyFunctionResponse(m gjson.Result) bool {
	parts := m.Get("parts")
	if !parts.IsArray() {
		return false
	}
	hasResponse, hasOther := false, false
	parts.ForEach(func(_, p gjson.Result) bool {
		if geminiPart(p, "functionResponse", "function_response").Exists() {
			hasResponse = true
		} else {
			hasOther = true
		}
		return true
	})
	return hasResponse && !hasOther
}

// geminiMessages 展开一条 contents 项：文本 / 附件合并，functionResponse 单独成为 tool 消息。
func geminiMessages(m gjson.Result) []Message {
	role := "user"
	if m.Get("role").String() == "model" {
		role = "assistant"
	}
	var out []Message
	var text []string
	flush := func() {
		if len(text) > 0 {
			out = append(out, Message{Role: role, Content: strings.Join(text, "\n")})
			text = text[:0]
		}
	}
	m.Get("parts").ForEach(func(_, p gjson.Result) bool {
		switch {
		case p.Get("text").Exists():
			if !p.Get("thought").Bool() {
				text = append(text, p.Get("text").String())
			}
		case geminiPart(p, "inlineData", "inline_data").Exists():
			mime := geminiPart(p, "inlineData", "inline_data").Get("mimeType").String()
			if mime == "" {
				mime = geminiPart(p, "inlineData", "inline_data").Get("mime_type").String()
			}
			text = append(text, mimePlaceholder(mime))
		case geminiPart(p, "fileData", "file_data").Exists():
			text = append(text, "[file]")
		case geminiPart(p, "functionCall", "function_call").Exists():
			call := geminiPart(p, "functionCall", "function_call")
			text = append(text, toolUseLine(call.Get("name").String(), call.Get("args").Raw))
		case geminiPart(p, "functionResponse", "function_response").Exists():
			flush()
			resp := geminiPart(p, "functionResponse", "function_response")
			out = append(out, Message{Role: "tool", Content: Truncate(resp.Get("response").Raw, MaxToolResultChars)})
		}
		return true
	})
	flush()
	return out
}

func mimePlaceholder(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "[image]"
	case strings.HasPrefix(mime, "audio/"):
		return "[audio]"
	case strings.HasPrefix(mime, "video/"):
		return "[video]"
	default:
		return "[file]"
	}
}

// ---- 收尾：清洗与体积控制 ----

func finalizeRequest(req Request) Request {
	req.Model = Truncate(Clean(strings.TrimSpace(req.Model)), 255)
	req.System = Truncate(Clean(strings.TrimSpace(req.System)), MaxSystemChars)
	req.FirstUser = Truncate(Clean(strings.TrimSpace(req.FirstUser)), maxKeyChars)
	req.Messages = fitMessages(req.Messages)
	return req
}

// fitMessages 清洗并截断每条消息，总量超限时丢弃较早的消息，保留最近的。
func fitMessages(msgs []Message) []Message {
	cleaned := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		content := strings.TrimSpace(Clean(m.Content))
		if content == "" {
			continue
		}
		cleaned = append(cleaned, Message{Role: m.Role, Content: Truncate(content, MaxMessageChars)})
	}
	start, total := len(cleaned), 0
	for i := len(cleaned) - 1; i >= 0; i-- {
		n := utf8.RuneCountInString(cleaned[i].Content)
		if start < len(cleaned) && total+n > MaxRequestChars {
			break
		}
		total += n
		start = i
	}
	if start == 0 {
		return cleaned
	}
	note := Message{Role: "note", Content: "[" + strconv.Itoa(start) + " earlier messages omitted]"}
	return append([]Message{note}, cleaned[start:]...)
}
