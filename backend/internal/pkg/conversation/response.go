package conversation

import (
	"bytes"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// Response 是从响应体提取的助手回复。
type Response struct {
	// Text 助手可见的回复文本，思考内容不计入。
	// 工具调用以 "[tool_use: 名称] 参数" 的形式单独成行，附在出现的位置。
	Text string
}

// ParseResponse 按协议解析响应体，支持普通 JSON 与 SSE 流。
// body 可以是被截断的流（只取到前一段），此时返回已解析出的部分。
func ParseResponse(format Format, body []byte, contentType string) Response {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return Response{}
	}
	var b builder
	if isSSE(contentType, body) {
		switch format {
		case FormatAnthropic:
			sseAnthropic(body, &b)
		case FormatOpenAIChat:
			sseChat(body, &b)
		case FormatOpenAIResponses:
			sseResponses(body, &b)
		case FormatGemini:
			forEachSSE(body, func(_ string, data []byte) {
				geminiChunk(gjson.ParseBytes(data), &b)
			})
		}
	} else {
		root := gjson.ParseBytes(body)
		switch format {
		case FormatAnthropic:
			anthropicOutput(root, &b)
		case FormatOpenAIChat:
			chatOutput(root, &b)
		case FormatOpenAIResponses:
			responsesOutput(root, &b)
		case FormatGemini:
			if root.IsArray() {
				// 未启用 alt=sse 的 Gemini 流式响应是 JSON 数组。
				root.ForEach(func(_, chunk gjson.Result) bool {
					geminiChunk(chunk, &b)
					return true
				})
			} else {
				geminiChunk(root, &b)
			}
		}
	}
	return Response{Text: b.String()}
}

func isSSE(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return true
	}
	return bytes.HasPrefix(body, []byte("event:")) || bytes.HasPrefix(body, []byte("data:")) || bytes.HasPrefix(body, []byte(":"))
}

// builder 累积回复文本，总量受 MaxResponseChars 限制。
type builder struct {
	sb          strings.Builder
	chars       int
	dropped     int
	atLineStart bool
}

func (b *builder) write(s string) {
	if s == "" {
		return
	}
	n := utf8.RuneCountInString(s)
	room := MaxResponseChars - b.chars
	switch {
	case room <= 0:
		b.dropped += n
		return
	case n > room:
		b.dropped += n - room
		s = s[:runeOffset(s, room)]
		n = room
	}
	b.sb.WriteString(s)
	b.chars += n
	b.atLineStart = strings.HasSuffix(s, "\n")
}

func (b *builder) text(s string) { b.write(s) }

// breakLine 保证后续内容从新的一行开始。
func (b *builder) breakLine() {
	if b.chars > 0 && !b.atLineStart {
		b.write("\n")
	}
}

func (b *builder) tool(name, input string) {
	b.breakLine()
	b.write(toolUseLine(name, input) + "\n")
}

// absorb 把另一个 builder 的内容并入当前 builder。
func (b *builder) absorb(o *builder) {
	b.write(o.sb.String())
	b.dropped += o.dropped
}

func (b *builder) String() string {
	out := strings.TrimSpace(b.sb.String())
	if b.dropped > 0 {
		out += truncationMarker(b.dropped)
	}
	return Clean(out)
}

// forEachSSE 按 SSE 帧回调 (event, data)。data 在回调返回后即失效，调用方不得持有。
func forEachSSE(body []byte, fn func(event string, data []byte)) {
	var event string
	var data []byte
	flush := func() {
		if len(data) > 0 {
			fn(event, data)
		}
		event, data = "", data[:0]
	}
	for len(body) > 0 {
		var line []byte
		if i := bytes.IndexByte(body, '\n'); i >= 0 {
			line, body = body[:i], body[i+1:]
		} else {
			line, body = body, nil
		}
		line = bytes.TrimRight(line, "\r")
		if len(line) == 0 {
			flush()
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "event":
			event = string(value)
		case "data":
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, value...)
		}
	}
	flush()
}

// toolBuf 累积流式工具调用的名称与参数。
type toolBuf struct {
	name string
	args strings.Builder
}

func flushTools(tools map[int64]*toolBuf, b *builder) {
	keys := make([]int64, 0, len(tools))
	for k := range tools {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		b.tool(tools[k].name, tools[k].args.String())
	}
}

// ---- Anthropic ----

func anthropicOutput(root gjson.Result, b *builder) {
	root.Get("content").ForEach(func(_, blk gjson.Result) bool {
		switch blk.Get("type").String() {
		case "text":
			b.breakLine()
			b.text(blk.Get("text").String())
		case "tool_use", "server_tool_use":
			b.tool(blk.Get("name").String(), blk.Get("input").Raw)
		}
		return true
	})
}

func sseAnthropic(body []byte, b *builder) {
	open := map[int64]*toolBuf{}
	forEachSSE(body, func(_ string, data []byte) {
		ev := gjson.ParseBytes(data)
		idx := ev.Get("index").Int()
		switch ev.Get("type").String() {
		case "content_block_start":
			cb := ev.Get("content_block")
			switch cb.Get("type").String() {
			case "text":
				b.breakLine()
				b.text(cb.Get("text").String())
			case "tool_use", "server_tool_use":
				open[idx] = &toolBuf{name: cb.Get("name").String()}
			}
		case "content_block_delta":
			d := ev.Get("delta")
			switch d.Get("type").String() {
			case "text_delta":
				b.text(d.Get("text").String())
			case "input_json_delta":
				if t := open[idx]; t != nil {
					t.args.WriteString(d.Get("partial_json").String())
				}
			}
		case "content_block_stop":
			if t := open[idx]; t != nil {
				b.tool(t.name, t.args.String())
				delete(open, idx)
			}
		}
	})
	// 流被截断时，把尚未结束的工具调用也带上。
	flushTools(open, b)
}

// ---- OpenAI Chat Completions ----

func chatOutput(root gjson.Result, b *builder) {
	msg := root.Get("choices.0.message")
	text := chatContentText(msg.Get("content"))
	if text == "" {
		text = root.Get("choices.0.text").String()
	}
	b.text(text)
	msg.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
		b.tool(tc.Get("function.name").String(), tc.Get("function.arguments").String())
		return true
	})
	if fc := msg.Get("function_call"); fc.Exists() {
		b.tool(fc.Get("name").String(), fc.Get("arguments").String())
	}
}

func sseChat(body []byte, b *builder) {
	calls := map[int64]*toolBuf{}
	forEachSSE(body, func(_ string, data []byte) {
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return
		}
		delta := gjson.ParseBytes(data).Get("choices.0.delta")
		b.text(chatContentText(delta.Get("content")))
		b.text(delta.Get("refusal").String())
		delta.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
			idx := tc.Get("index").Int()
			call := calls[idx]
			if call == nil {
				call = &toolBuf{}
				calls[idx] = call
			}
			if name := tc.Get("function.name").String(); name != "" {
				call.name = name
			}
			call.args.WriteString(tc.Get("function.arguments").String())
			return true
		})
	})
	flushTools(calls, b)
}

// ---- OpenAI Responses ----

func responsesOutput(root gjson.Result, b *builder) {
	root.Get("output").ForEach(func(_, item gjson.Result) bool {
		switch item.Get("type").String() {
		case "message":
			b.breakLine()
			item.Get("content").ForEach(func(_, part gjson.Result) bool {
				switch part.Get("type").String() {
				case "output_text", "text":
					b.text(part.Get("text").String())
				case "refusal":
					b.text(part.Get("refusal").String())
				}
				return true
			})
		case "function_call", "custom_tool_call":
			args := item.Get("arguments").String()
			if args == "" {
				args = item.Get("input").String()
			}
			b.tool(item.Get("name").String(), args)
		}
		return true
	})
}

func sseResponses(body []byte, b *builder) {
	var streamed builder
	var completed gjson.Result
	open := map[int64]*toolBuf{}
	forEachSSE(body, func(event string, data []byte) {
		ev := gjson.ParseBytes(data)
		typ := ev.Get("type").String()
		if typ == "" {
			typ = event
		}
		idx := ev.Get("output_index").Int()
		switch typ {
		case "response.output_text.delta", "response.refusal.delta":
			streamed.text(ev.Get("delta").String())
		case "response.output_item.added":
			switch item := ev.Get("item"); item.Get("type").String() {
			case "message":
				streamed.breakLine()
			case "function_call", "custom_tool_call":
				open[idx] = &toolBuf{name: item.Get("name").String()}
			}
		case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
			if t := open[idx]; t != nil {
				t.args.WriteString(ev.Get("delta").String())
			}
		case "response.output_item.done":
			item := ev.Get("item")
			switch item.Get("type").String() {
			case "function_call", "custom_tool_call":
				args := item.Get("arguments").String()
				if args == "" {
					args = item.Get("input").String()
				}
				name := item.Get("name").String()
				if t := open[idx]; t != nil {
					if args == "" {
						args = t.args.String()
					}
					if name == "" {
						name = t.name
					}
					delete(open, idx)
				}
				streamed.tool(name, args)
			}
		case "response.completed", "response.incomplete":
			completed = ev.Get("response")
		}
	})
	flushTools(open, &streamed)
	// 终态事件里的 output 最完整；部分上游会把它留空，此时退回到流式增量。
	if completed.Exists() {
		var final builder
		responsesOutput(completed, &final)
		if final.chars > 0 {
			b.absorb(&final)
			return
		}
	}
	b.absorb(&streamed)
}

// ---- Gemini ----

func geminiChunk(chunk gjson.Result, b *builder) {
	if !chunk.Get("candidates").Exists() {
		// 部分上游把响应包在 response 字段里。
		if inner := chunk.Get("response"); inner.Exists() {
			chunk = inner
		}
	}
	chunk.Get("candidates.0.content.parts").ForEach(func(_, p gjson.Result) bool {
		switch {
		case p.Get("text").Exists():
			if !p.Get("thought").Bool() {
				b.text(p.Get("text").String())
			}
		case geminiPart(p, "functionCall", "function_call").Exists():
			call := geminiPart(p, "functionCall", "function_call")
			b.tool(call.Get("name").String(), call.Get("args").Raw)
		case geminiPart(p, "inlineData", "inline_data").Exists():
			data := geminiPart(p, "inlineData", "inline_data")
			mime := data.Get("mimeType").String()
			if mime == "" {
				mime = data.Get("mime_type").String()
			}
			b.breakLine()
			b.write(mimePlaceholder(mime) + "\n")
		}
		return true
	})
}
