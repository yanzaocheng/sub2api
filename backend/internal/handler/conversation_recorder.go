package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/conversation"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requestmodel"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// conversationCaptureLimit 单个响应最多缓存的原始字节数。SSE 的帧开销远大于正文，
// 4MiB 足以容纳 MaxResponseChars 个字符的回复；更长的回复只记录开头部分。
const conversationCaptureLimit = 4 << 20

// conversationSink 是记录中间件依赖的服务端口，由 *service.ConversationRecordService 实现。
type conversationSink interface {
	Enabled(ctx context.Context) bool
	Record(rec *service.ConversationRecord)
}

// ConversationRecorderMiddleware 记录网关上的聊天对话（用户输入与模型回复），
// 开关在「系统设置 → 功能开关」中，默认关闭。
//
// 挂载位置：每条网关链的末端（认证、分组白名单之后、业务 handler 之前；路由层把它包在未分组 Key 拦截外层），
// 这样被拒绝的请求不会进入；只处理 POST 的聊天类路由（见 conversation.FormatForRoute），
// 其余请求原样放行。
//
// 关闭时开销只有一次进程内缓存读取。开启后：
//   - 读取并回填请求体（与分组模型白名单中间件相同的 PrereadBody 机制，下游零拷贝）；
//   - 用旁路 Writer 缓存响应，请求结束后解析；
//   - 记录入队后由后台协程批量落库，任何环节失败都只丢记录，不影响转发。
//
// 只记录成功（2xx）且产生了可见回复的请求。
func ConversationRecorderMiddleware(sink conversationSink) gin.HandlerFunc {
	return WrapWithConversationRecorder(sink, nil)
}

// WrapWithConversationRecorder 与 ConversationRecorderMiddleware 行为相同，只是把「放行」交给 next：
// next 通常是另一个会调用 c.Next() 的中间件，记录逻辑包在它的外层；next 为 nil 时直接 c.Next()。
func WrapWithConversationRecorder(sink conversationSink, next gin.HandlerFunc) gin.HandlerFunc {
	proceed := func(c *gin.Context) {
		if next != nil {
			next(c)
			return
		}
		c.Next()
	}
	return func(c *gin.Context) {
		turn := prepareConversationTurn(c, sink)
		if turn == nil {
			proceed(c)
			return
		}

		started := time.Now()
		original := c.Writer
		capture := &conversationCaptureWriter{ResponseWriter: original, limit: conversationCaptureLimit}
		c.Writer = capture
		func() {
			// 即使 handler panic 也要还原 Writer，交给外层 Recovery 用原 Writer 响应。
			defer func() { c.Writer = original }()
			proceed(c)
		}()

		recordConversation(c, sink, turn, capture, started)
	}
}

// conversationTurn 是转发之前解析好的本轮请求信息。
type conversationTurn struct {
	apiKey *service.APIKey
	format conversation.Format
	req    conversation.Request
}

// prepareConversationTurn 判断本次请求是否需要记录，需要时读取并解析请求体；返回 nil 表示不记录。
// 它运行在转发之前，任何失败（包括 panic）都只意味着「不记录」，绝不能影响请求本身。
func prepareConversationTurn(c *gin.Context, sink conversationSink) (turn *conversationTurn) {
	defer func() {
		if r := recover(); r != nil {
			logger.L().Error("conversation_record.prepare_panic", zap.Any("panic", r))
			turn = nil
		}
	}()

	if sink == nil || c.Request == nil || c.Request.Method != http.MethodPost {
		return nil
	}
	format, ok := conversation.FormatForRoute(c.FullPath())
	if !ok {
		return nil
	}
	geminiModel, geminiStream := "", false
	if format == conversation.FormatGemini {
		model, action, isGenerate := conversation.GeminiModelAction(c.Param("modelAction"))
		if !isGenerate {
			return nil
		}
		geminiModel, geminiStream = model, action == "streamGenerateContent"
	}
	apiKey, found := middleware2.GetAPIKeyFromContext(c)
	if !found || apiKey == nil || apiKey.User == nil {
		return nil
	}
	if !sink.Enabled(c.Request.Context()) {
		return nil
	}

	body, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		// 读取失败（超限、不支持的压缩格式等）：让后续 handler 读到同一个错误，
		// 由它按各自协议的格式响应，保持与未开启记录时一致。
		c.Request.Body = failedRequestBody{err: err}
		return nil
	}
	requestmodel.ResetRequestBody(c.Request, body)

	req := conversation.ParseRequest(format, body)
	if len(req.Messages) == 0 {
		return nil
	}
	if geminiModel != "" {
		req.Model = geminiModel
	}
	req.Stream = req.Stream || geminiStream
	return &conversationTurn{apiKey: apiKey, format: format, req: req}
}

// recordConversation 把已完成的请求整理成记录入队。任何异常都被吞掉：记录是附属功能，
// 不能让它的故障影响已经发出的响应。
func recordConversation(c *gin.Context, sink conversationSink, turn *conversationTurn, capture *conversationCaptureWriter, started time.Time) {
	defer func() {
		if r := recover(); r != nil {
			logger.L().Error("conversation_record.panic", zap.Any("panic", r))
		}
	}()

	apiKey, format, req := turn.apiKey, turn.format, turn.req
	if status := capture.Status(); status < 200 || status >= 300 {
		return
	}
	if enc := capture.Header().Get("Content-Encoding"); enc != "" && !strings.EqualFold(enc, "identity") {
		return
	}
	resp := conversation.ParseResponse(format, capture.buf, capture.Header().Get("Content-Type"))
	if resp.Text == "" {
		return
	}

	requestID, _ := c.Request.Context().Value(ctxkey.RequestID).(string)
	if strings.TrimSpace(requestID) == "" {
		requestID, _ = c.Request.Context().Value(ctxkey.ClientRequestID).(string)
	}
	userID, keyID := apiKey.User.ID, apiKey.ID
	rec := &service.ConversationRecord{
		CreatedAt:       started.UTC(),
		RequestID:       requestID,
		ConversationKey: conversation.Key(userID, keyID, req.System, req.FirstUser, requestID),
		UserID:          &userID,
		UserEmail:       apiKey.User.Email,
		APIKeyID:        &keyID,
		APIKeyName:      apiKey.Name,
		GroupID:         apiKey.GroupID,
		Endpoint:        NormalizeInboundEndpoint(c.Request.URL.Path),
		Model:           req.Model,
		Stream:          req.Stream,
		DurationMs:      time.Since(started).Milliseconds(),
		ClientIP:        ip.GetClientIP(c),
		UserAgent:       conversation.Truncate(conversation.Clean(c.GetHeader("User-Agent")), 255),
		SystemPrompt:    req.System,
		Messages:        req.Messages,
		Response:        resp.Text,
		PromptPreview:   conversation.Preview(promptPreviewSource(req.Messages), conversation.PreviewChars),
		ResponsePreview: conversation.Preview(resp.Text, conversation.PreviewChars),
	}
	sink.Record(rec)
}

// promptPreviewSource 选出列表摘要使用的输入文本：优先取本轮最后一条用户消息，
// 没有用户消息（例如 Agent 的纯工具结果轮）时取最后一条消息。
func promptPreviewSource(msgs []conversation.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	if len(msgs) == 0 {
		return ""
	}
	return msgs[len(msgs)-1].Content
}

// conversationCaptureWriter 旁路复制写给客户端的响应字节，上限 limit。
// 嵌入 gin.ResponseWriter，Flush / Hijack 等其余方法原样透传，不影响流式响应。
type conversationCaptureWriter struct {
	gin.ResponseWriter
	limit int
	buf   []byte
}

func (w *conversationCaptureWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	if keep := w.keep(n); keep > 0 {
		w.buf = append(w.buf, b[:keep]...)
	}
	return n, err
}

func (w *conversationCaptureWriter) WriteString(s string) (int, error) {
	n, err := w.ResponseWriter.WriteString(s)
	if keep := w.keep(n); keep > 0 {
		w.buf = append(w.buf, s[:keep]...)
	}
	return n, err
}

// keep 返回刚写出的 n 个字节中应当缓存的字节数。
func (w *conversationCaptureWriter) keep(n int) int {
	if n <= 0 || len(w.buf) >= w.limit {
		return 0
	}
	// 错误响应不会被记录，不必缓存。
	if status := w.ResponseWriter.Status(); status < 200 || status >= 300 {
		return 0
	}
	if room := w.limit - len(w.buf); n > room {
		return room
	}
	return n
}

// failedRequestBody 在读取时返回一个已发生的错误。
type failedRequestBody struct{ err error }

func (b failedRequestBody) Read([]byte) (int, error) { return 0, b.err }
func (b failedRequestBody) Close() error             { return nil }
