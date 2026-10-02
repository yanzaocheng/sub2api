package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// convSinkFake 记录中间件产出的记录，并按 enabled 决定是否放行。
type convSinkFake struct {
	mu          sync.Mutex
	enabled     bool
	enabledSeen int
	records     []*service.ConversationRecord
}

func (s *convSinkFake) Enabled(context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabledSeen++
	return s.enabled
}

func (s *convSinkFake) Record(rec *service.ConversationRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, rec)
}

func (s *convSinkFake) recorded() []*service.ConversationRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*service.ConversationRecord(nil), s.records...)
}

func convTestAPIKey() *service.APIKey {
	groupID := int64(9)
	return &service.APIKey{ID: 21, Name: "my-key", GroupID: &groupID, User: &service.User{ID: 7, Email: "alice@example.com"}}
}

// convTestEngine 搭一条与网关相同的链路：先注入已认证的 API Key，再挂记录中间件。
func convTestEngine(sink conversationSink, apiKey *service.APIKey, routes func(r *gin.Engine)) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if apiKey != nil {
			c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
		}
		c.Next()
	})
	r.Use(ConversationRecorderMiddleware(sink))
	routes(r)
	return r
}

func convPost(r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-cli/2.0")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

const convAnthropicRequest = `{"model":"claude-sonnet-4-5","stream":false,"system":"Be brief.","messages":[{"role":"user","content":"Hello"}]}`

const convAnthropicResponse = `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"Hi there!"}]}`

func TestConversationRecorder_RecordsAnthropicExchange(t *testing.T) {
	sink := &convSinkFake{enabled: true}
	var handlerSaw string
	r := convTestEngine(sink, convTestAPIKey(), func(r *gin.Engine) {
		r.POST("/v1/messages", func(c *gin.Context) {
			body, _ := io.ReadAll(c.Request.Body)
			handlerSaw = string(body)
			c.Data(http.StatusOK, "application/json", []byte(convAnthropicResponse))
		})
	})

	w := convPost(r, "/v1/messages", convAnthropicRequest)

	// 记录是旁路行为：响应与下游 handler 看到的请求体都必须原样不变。
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, convAnthropicResponse, w.Body.String())
	require.Equal(t, convAnthropicRequest, handlerSaw)

	recs := sink.recorded()
	require.Len(t, recs, 1)
	rec := recs[0]
	require.Equal(t, "claude-sonnet-4-5", rec.Model)
	require.Equal(t, "/v1/messages", rec.Endpoint)
	require.Equal(t, "Be brief.", rec.SystemPrompt)
	require.Equal(t, "Hi there!", rec.Response)
	require.Equal(t, "Hello", rec.PromptPreview)
	require.Equal(t, "Hi there!", rec.ResponsePreview)
	require.Len(t, rec.Messages, 1)
	require.Equal(t, "user", rec.Messages[0].Role)
	require.Equal(t, "Hello", rec.Messages[0].Content)
	require.False(t, rec.Stream)
	require.NotEmpty(t, rec.ConversationKey)
	require.Equal(t, int64(7), *rec.UserID)
	require.Equal(t, "alice@example.com", rec.UserEmail)
	require.Equal(t, int64(21), *rec.APIKeyID)
	require.Equal(t, "my-key", rec.APIKeyName)
	require.Equal(t, int64(9), *rec.GroupID)
	require.Equal(t, "claude-cli/2.0", rec.UserAgent)
	require.False(t, rec.CreatedAt.IsZero())
}

func TestConversationRecorder_RecordsStreamingResponse(t *testing.T) {
	sink := &convSinkFake{enabled: true}
	r := convTestEngine(sink, convTestAPIKey(), func(r *gin.Engine) {
		r.POST("/v1/messages", func(c *gin.Context) {
			c.Header("Content-Type", "text/event-stream")
			c.Status(http.StatusOK)
			flusher, ok := c.Writer.(http.Flusher)
			require.True(t, ok, "旁路 Writer 必须保留 Flusher，否则流式响应会被缓冲")
			for _, frame := range []string{
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hel\"}}\n\n",
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}\n\n",
			} {
				_, _ = c.Writer.WriteString(frame)
				flusher.Flush()
			}
		})
	})

	w := convPost(r, "/v1/messages", strings.Replace(convAnthropicRequest, `"stream":false`, `"stream":true`, 1))

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, w.Flushed)
	recs := sink.recorded()
	require.Len(t, recs, 1)
	require.Equal(t, "Hello", recs[0].Response)
	require.True(t, recs[0].Stream)
}

func TestConversationRecorder_DisabledDoesNotTouchRequest(t *testing.T) {
	sink := &convSinkFake{enabled: false}
	r := convTestEngine(sink, convTestAPIKey(), func(r *gin.Engine) {
		r.POST("/v1/messages", func(c *gin.Context) {
			_, isPreread := c.Request.Body.(interface{ Bytes() []byte })
			require.False(t, isPreread, "关闭时不能提前读取、回填请求体")
			c.Data(http.StatusOK, "application/json", []byte(convAnthropicResponse))
		})
	})

	w := convPost(r, "/v1/messages", convAnthropicRequest)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, sink.enabledSeen)
	require.Empty(t, sink.recorded())
}

func TestConversationRecorder_SkipsRoutesThatAreNotConversations(t *testing.T) {
	sink := &convSinkFake{enabled: true}
	r := convTestEngine(sink, convTestAPIKey(), func(r *gin.Engine) {
		ok := func(c *gin.Context) { c.Data(http.StatusOK, "application/json", []byte(convAnthropicResponse)) }
		r.POST("/v1/messages/count_tokens", ok)
		r.POST("/v1/embeddings", ok)
		r.POST("/v1/responses/*subpath", ok)
		r.GET("/v1/messages", ok)
		r.POST("/v1beta/models/*modelAction", ok)
	})

	convPost(r, "/v1/messages/count_tokens", convAnthropicRequest)
	convPost(r, "/v1/embeddings", convAnthropicRequest)
	convPost(r, "/v1/responses/compact", convAnthropicRequest)
	convPost(r, "/v1beta/models/gemini-2.5-pro:countTokens", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	require.Empty(t, sink.recorded())
	require.Zero(t, sink.enabledSeen, "不承载对话的路由连开关都不必查询")
}

func TestConversationRecorder_SkipsFailedAndEmptyResponses(t *testing.T) {
	cases := map[string]func(c *gin.Context){
		"upstream error": func(c *gin.Context) {
			c.Data(http.StatusTooManyRequests, "application/json", []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
		},
		"no visible text": func(c *gin.Context) {
			c.Data(http.StatusOK, "application/json", []byte(`{"content":[{"type":"thinking","thinking":"hmm"}]}`))
		},
		"in-band stream error": func(c *gin.Context) {
			c.Header("Content-Type", "text/event-stream")
			_, _ = c.Writer.WriteString("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}\n\n")
		},
		"not parseable": func(c *gin.Context) {
			c.Data(http.StatusOK, "text/html", []byte("<html>oops</html>"))
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			sink := &convSinkFake{enabled: true}
			r := convTestEngine(sink, convTestAPIKey(), func(r *gin.Engine) { r.POST("/v1/messages", handler) })
			convPost(r, "/v1/messages", convAnthropicRequest)
			require.Empty(t, sink.recorded())
		})
	}
}

func TestConversationRecorder_SkipsUnauthenticated(t *testing.T) {
	sink := &convSinkFake{enabled: true}
	r := convTestEngine(sink, nil, func(r *gin.Engine) {
		r.POST("/v1/messages", func(c *gin.Context) {
			c.Data(http.StatusOK, "application/json", []byte(convAnthropicResponse))
		})
	})
	convPost(r, "/v1/messages", convAnthropicRequest)
	require.Empty(t, sink.recorded())

	// 没有关联用户的 Key 也不记录。
	sink2 := &convSinkFake{enabled: true}
	r2 := convTestEngine(sink2, &service.APIKey{ID: 1}, func(r *gin.Engine) {
		r.POST("/v1/messages", func(c *gin.Context) {
			c.Data(http.StatusOK, "application/json", []byte(convAnthropicResponse))
		})
	})
	convPost(r2, "/v1/messages", convAnthropicRequest)
	require.Empty(t, sink2.recorded())
}

func TestConversationRecorder_ChatCompletions(t *testing.T) {
	sink := &convSinkFake{enabled: true}
	r := convTestEngine(sink, convTestAPIKey(), func(r *gin.Engine) {
		r.POST("/v1/chat/completions", func(c *gin.Context) {
			c.Data(http.StatusOK, "application/json", []byte(`{"choices":[{"message":{"role":"assistant","content":"4"}}]}`))
		})
	})
	convPost(r, "/v1/chat/completions", `{"model":"gpt-5","messages":[{"role":"user","content":"2+2?"}]}`)

	recs := sink.recorded()
	require.Len(t, recs, 1)
	require.Equal(t, "/v1/chat/completions", recs[0].Endpoint)
	require.Equal(t, "gpt-5", recs[0].Model)
	require.Equal(t, "4", recs[0].Response)
}

func TestConversationRecorder_GeminiTakesModelFromPath(t *testing.T) {
	sink := &convSinkFake{enabled: true}
	r := convTestEngine(sink, convTestAPIKey(), func(r *gin.Engine) {
		r.POST("/v1beta/models/*modelAction", func(c *gin.Context) {
			c.Data(http.StatusOK, "application/json", []byte(`{"candidates":[{"content":{"parts":[{"text":"Go is a language."}]}}]}`))
		})
	})
	convPost(r, "/v1beta/models/gemini-2.5-pro:streamGenerateContent", `{"contents":[{"role":"user","parts":[{"text":"what is Go?"}]}]}`)

	recs := sink.recorded()
	require.Len(t, recs, 1)
	require.Equal(t, "gemini-2.5-pro", recs[0].Model)
	require.True(t, recs[0].Stream)
	require.Equal(t, "/v1beta/models", recs[0].Endpoint)
	require.Equal(t, "Go is a language.", recs[0].Response)
}

// 同一场对话的多轮请求得到同一个会话键；换一个首条消息就是另一场对话。
func TestConversationRecorder_ConversationKeyGroupsTurns(t *testing.T) {
	sink := &convSinkFake{enabled: true}
	r := convTestEngine(sink, convTestAPIKey(), func(r *gin.Engine) {
		r.POST("/v1/messages", func(c *gin.Context) {
			c.Data(http.StatusOK, "application/json", []byte(convAnthropicResponse))
		})
	})
	convPost(r, "/v1/messages", `{"model":"m","messages":[{"role":"user","content":"first"}]}`)
	convPost(r, "/v1/messages", `{"model":"m","messages":[{"role":"user","content":"first"},{"role":"assistant","content":"a1"},{"role":"user","content":"second"}]}`)
	convPost(r, "/v1/messages", `{"model":"m","messages":[{"role":"user","content":"another topic"}]}`)

	recs := sink.recorded()
	require.Len(t, recs, 3)
	require.Equal(t, recs[0].ConversationKey, recs[1].ConversationKey)
	require.NotEqual(t, recs[0].ConversationKey, recs[2].ConversationKey)
	// 第二轮只保存新增的输入，不重复保存历史。
	require.Equal(t, "second", recs[1].Messages[0].Content)
	require.Len(t, recs[1].Messages, 1)
}

func TestConversationRecorder_CapturesAtMostLimitBytes(t *testing.T) {
	w := &conversationCaptureWriter{ResponseWriter: newTestGinWriter(httptest.NewRecorder()), limit: 10}
	n, err := w.Write([]byte("0123456"))
	require.NoError(t, err)
	require.Equal(t, 7, n)
	n, err = w.WriteString("789abcdef")
	require.NoError(t, err)
	require.Equal(t, 9, n, "超出缓存上限只影响旁路缓存，写给客户端的字节一个不少")
	require.Equal(t, "0123456789", string(w.buf))

	n, err = w.Write([]byte("more"))
	require.NoError(t, err)
	require.Equal(t, 4, n)
	require.Equal(t, "0123456789", string(w.buf))
}

func TestConversationRecorder_DoesNotCaptureErrorBodies(t *testing.T) {
	inner := newTestGinWriter(httptest.NewRecorder())
	inner.WriteHeader(http.StatusBadGateway)
	w := &conversationCaptureWriter{ResponseWriter: inner, limit: 1024}
	_, _ = w.Write([]byte(`{"error":"upstream"}`))
	require.Empty(t, w.buf)
}

// 读取请求体失败（例如超过大小上限）时，记录中间件不能改变下游看到的错误。
func TestConversationRecorder_PassesBodyReadErrorToHandler(t *testing.T) {
	sink := &convSinkFake{enabled: true}
	var readErr error
	r := convTestEngine(sink, convTestAPIKey(), func(r *gin.Engine) {
		r.POST("/v1/messages", func(c *gin.Context) {
			_, readErr = io.ReadAll(c.Request.Body)
			c.Status(http.StatusRequestEntityTooLarge)
		})
	})

	maxErr := &http.MaxBytesError{Limit: 8}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", io.NopCloser(&errReader{err: maxErr}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	var got *http.MaxBytesError
	require.True(t, errors.As(readErr, &got), "下游 handler 读到的仍是原始的超限错误")
	require.Empty(t, sink.recorded())
}

type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error) { return 0, r.err }

func TestConversationRecorder_NilSinkPassesThrough(t *testing.T) {
	r := convTestEngine(nil, convTestAPIKey(), func(r *gin.Engine) {
		r.POST("/v1/messages", func(c *gin.Context) {
			body, _ := io.ReadAll(c.Request.Body)
			c.Data(http.StatusOK, "application/json", body)
		})
	})
	w := convPost(r, "/v1/messages", convAnthropicRequest)
	require.Equal(t, convAnthropicRequest, w.Body.String())
}

// 下游先用 PrereadBody 零拷贝读取，再被改写时，记录的仍是客户端发来的原始内容。
func TestConversationRecorder_RecordsOriginalRequestEvenIfHandlerMutatesBody(t *testing.T) {
	sink := &convSinkFake{enabled: true}
	r := convTestEngine(sink, convTestAPIKey(), func(r *gin.Engine) {
		r.POST("/v1/messages", func(c *gin.Context) {
			body, _ := io.ReadAll(c.Request.Body)
			for i := range body {
				body[i] = 'x'
			}
			c.Data(http.StatusOK, "application/json", []byte(convAnthropicResponse))
		})
	})
	convPost(r, "/v1/messages", convAnthropicRequest)

	recs := sink.recorded()
	require.Len(t, recs, 1)
	require.Equal(t, "Hello", recs[0].Messages[0].Content)
	require.NotContains(t, bytes.NewBufferString(recs[0].PromptPreview).String(), "xxx")
}

func newTestGinWriter(rec *httptest.ResponseRecorder) gin.ResponseWriter {
	c, _ := gin.CreateTestContext(rec)
	return c.Writer
}

// 路由层把记录逻辑包在未分组 Key 拦截的外层：被包的中间件照常执行，
// 放行时记录，拒绝时（不调用 c.Next）下游 handler 不会被执行，也不会产生记录。
func TestWrapWithConversationRecorder_WrapsNextMiddleware(t *testing.T) {
	newEngine := func(sink conversationSink, guard gin.HandlerFunc, handlerRan *bool) *gin.Engine {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(string(middleware2.ContextKeyAPIKey), convTestAPIKey())
			c.Next()
		})
		r.Use(WrapWithConversationRecorder(sink, guard))
		r.POST("/v1/messages", func(c *gin.Context) {
			*handlerRan = true
			c.Data(http.StatusOK, "application/json", []byte(convAnthropicResponse))
		})
		return r
	}

	t.Run("guard lets the request through", func(t *testing.T) {
		sink := &convSinkFake{enabled: true}
		guardCalls, handlerRan := 0, false
		r := newEngine(sink, func(c *gin.Context) { guardCalls++; c.Next() }, &handlerRan)

		w := convPost(r, "/v1/messages", convAnthropicRequest)

		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, 1, guardCalls)
		require.True(t, handlerRan)
		require.Len(t, sink.recorded(), 1)
	})

	t.Run("guard rejects the request", func(t *testing.T) {
		sink := &convSinkFake{enabled: true}
		handlerRan := false
		r := newEngine(sink, func(c *gin.Context) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "no group"})
		}, &handlerRan)

		w := convPost(r, "/v1/messages", convAnthropicRequest)

		require.Equal(t, http.StatusForbidden, w.Code)
		require.JSONEq(t, `{"error":"no group"}`, w.Body.String(), "拒绝响应原样返回")
		require.False(t, handlerRan)
		require.Empty(t, sink.recorded())
	})

	t.Run("guard is optional", func(t *testing.T) {
		sink := &convSinkFake{enabled: true}
		handlerRan := false
		r := newEngine(sink, nil, &handlerRan)
		convPost(r, "/v1/messages", convAnthropicRequest)
		require.True(t, handlerRan)
		require.Len(t, sink.recorded(), 1)
	})
}

// handler panic 后 Writer 必须还原，外层 Recovery 才能用原 Writer 写出 500。
func TestConversationRecorder_RestoresWriterWhenHandlerPanics(t *testing.T) {
	sink := &convSinkFake{enabled: true}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyAPIKey), convTestAPIKey())
		c.Next()
	})
	r.Use(ConversationRecorderMiddleware(sink))
	r.POST("/v1/messages", func(c *gin.Context) { panic("boom") })

	w := convPost(r, "/v1/messages", convAnthropicRequest)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Empty(t, sink.recorded())
}

// 压缩的请求体（如 Codex 的 zstd / gzip）由中间件解码后回填：记录的是明文，
// 下游 handler 读到的也是已解码的明文，且 Content-Encoding 头已被移除，不会二次解码。
func TestConversationRecorder_DecodesCompressedRequestBody(t *testing.T) {
	sink := &convSinkFake{enabled: true}
	var handlerBody, handlerEncoding string
	r := convTestEngine(sink, convTestAPIKey(), func(r *gin.Engine) {
		r.POST("/v1/messages", func(c *gin.Context) {
			body, _ := io.ReadAll(c.Request.Body)
			handlerBody, handlerEncoding = string(body), c.GetHeader("Content-Encoding")
			c.Data(http.StatusOK, "application/json", []byte(convAnthropicResponse))
		})
	})

	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	_, err := zw.Write([]byte(convAnthropicRequest))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", &compressed)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, convAnthropicRequest, handlerBody)
	require.Empty(t, handlerEncoding)
	recs := sink.recorded()
	require.Len(t, recs, 1)
	require.Equal(t, "Hello", recs[0].Messages[0].Content)
}

// 记录是附属功能：准备阶段（读开关、读请求体、解析）哪怕 panic，请求也必须照常转发。
type convPanickySink struct{}

func (convPanickySink) Enabled(context.Context) bool       { panic("settings backend exploded") }
func (convPanickySink) Record(*service.ConversationRecord) {}

func TestConversationRecorder_PreparationPanicNeverBreaksTheRequest(t *testing.T) {
	var handlerBody string
	r := convTestEngine(convPanickySink{}, convTestAPIKey(), func(r *gin.Engine) {
		r.POST("/v1/messages", func(c *gin.Context) {
			body, _ := io.ReadAll(c.Request.Body)
			handlerBody = string(body)
			c.Data(http.StatusOK, "application/json", []byte(convAnthropicResponse))
		})
	})

	w := convPost(r, "/v1/messages", convAnthropicRequest)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, convAnthropicResponse, w.Body.String())
	require.Equal(t, convAnthropicRequest, handlerBody)
}
