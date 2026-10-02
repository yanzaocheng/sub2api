package routes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterConversationRecordRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := &handler.Handlers{Admin: &handler.AdminHandlers{ConversationRecord: admin.NewConversationRecordHandler(nil)}}
	registerConversationRecordRoutes(router.Group("/api/v1/admin"), h)

	var got []string
	for _, route := range router.Routes() {
		got = append(got, route.Method+" "+route.Path)
	}
	sort.Strings(got)
	require.Equal(t, []string{
		"DELETE /api/v1/admin/conversation-records/threads/:key",
		"GET /api/v1/admin/conversation-records",
		"GET /api/v1/admin/conversation-records/settings",
		"GET /api/v1/admin/conversation-records/threads/:key",
		"POST /api/v1/admin/conversation-records/batch-delete",
		"POST /api/v1/admin/conversation-records/clear",
		"PUT /api/v1/admin/conversation-records/settings",
	}, got)
}

// 没有管理端处理器时（测试里手工构造的 Handlers）原样返回被包的中间件，不记录也不报错。
func TestWithConversationRecorderIsPassThroughWithoutHandlers(t *testing.T) {
	calls := 0
	next := func(c *gin.Context) { calls++; c.Next() }

	for _, h := range []*handler.Handlers{nil, {}, {Admin: &handler.AdminHandlers{}}} {
		gin.SetMode(gin.TestMode)
		router := gin.New()
		router.Use(withConversationRecorder(h, next))
		router.POST("/v1/messages", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		require.Equal(t, http.StatusNoContent, w.Code)
	}
	require.Equal(t, 3, calls)
}

// 沿用 gateway_model_allowlist_test.go 的源码级断言：对话记录包在两个未分组 Key 拦截中间件的外层，
// 所有以它们收尾的网关链（含根路径别名与 codex 直连）因此都会记录对话。
func TestGatewayRoutesConversationRecorderWrapsGroupGuards(t *testing.T) {
	routeSource, err := os.ReadFile("gateway.go")
	require.NoError(t, err)
	source := string(routeSource)

	require.Regexp(t, regexp.MustCompile(regexp.QuoteMeta(`requireGroupAnthropic := withConversationRecorder(h, middleware.RequireGroupAssignment(settingService, middleware.AnthropicErrorWriter))`)), source)
	require.Regexp(t, regexp.MustCompile(regexp.QuoteMeta(`requireGroupGoogle := withConversationRecorder(h, middleware.RequireGroupAssignment(settingService, middleware.GoogleErrorWriter))`)), source)

	// 每条会承载对话的链都必须以这两个拦截中间件之一收尾。
	for _, chain := range []string{
		`gateway.Use(requireGroupAnthropic)`,
		`gemini.Use(requireGroupGoogle)`,
		`antigravityV1.Use(requireGroupAnthropic)`,
		`antigravityV1Beta.Use(requireGroupGoogle)`,
		`compositeTarget, requireGroupAnthropic, handler)`,
		`compositeTarget, requireGroupAnthropic)`,
	} {
		require.Contains(t, source, chain)
	}
}
