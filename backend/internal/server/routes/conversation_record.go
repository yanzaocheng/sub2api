package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// registerConversationRecordRoutes 注册用户对话记录的管理接口。
func registerConversationRecordRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	records := admin.Group("/conversation-records")
	{
		records.GET("", h.Admin.ConversationRecord.List)
		records.GET("/settings", h.Admin.ConversationRecord.GetSettings)
		records.PUT("/settings", h.Admin.ConversationRecord.UpdateSettings)
		records.POST("/batch-delete", h.Admin.ConversationRecord.BatchDelete)
		records.POST("/clear", h.Admin.ConversationRecord.Clear)
		records.GET("/threads/:key", h.Admin.ConversationRecord.GetThread)
		records.DELETE("/threads/:key", h.Admin.ConversationRecord.DeleteThread)
	}
}

// withConversationRecorder 把对话记录中间件包在 next 的外层。
// 测试里手工构造的 Handlers 可能没有管理端处理器，此时原样返回 next，不记录。
func withConversationRecorder(h *handler.Handlers, next gin.HandlerFunc) gin.HandlerFunc {
	if h == nil || h.Admin == nil || h.Admin.ConversationRecord == nil {
		return next
	}
	return handler.WrapWithConversationRecorder(h.Admin.ConversationRecord.Service(), next)
}
