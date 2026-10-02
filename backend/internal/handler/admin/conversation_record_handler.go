package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ConversationRecordHandler 用户对话记录的管理接口。
// 对话内容属于敏感数据：只有管理员可访问，查看详情会写入操作日志。
type ConversationRecordHandler struct {
	svc *service.ConversationRecordService
}

// NewConversationRecordHandler 创建对话记录处理器。
func NewConversationRecordHandler(svc *service.ConversationRecordService) *ConversationRecordHandler {
	return &ConversationRecordHandler{svc: svc}
}

// Service 返回底层服务，供网关侧的记录中间件复用同一个写入队列。
func (h *ConversationRecordHandler) Service() *service.ConversationRecordService {
	return h.svc
}

// List 分页查询对话列表（每场对话一行）。
// GET /api/v1/admin/conversation-records
func (h *ConversationRecordHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	if pageSize > 100 {
		pageSize = 100
	}
	filter := &service.ConversationFilter{
		Page:     page,
		PageSize: pageSize,
		User:     strings.TrimSpace(c.Query("user")),
		Model:    strings.TrimSpace(c.Query("model")),
		Query:    strings.TrimSpace(c.Query("q")),
	}
	if v := strings.TrimSpace(c.Query("user_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid user_id")
			return
		}
		filter.UserID = &id
	}
	if v := strings.TrimSpace(c.Query("start_time")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			response.BadRequest(c, "Invalid start_time, expect RFC3339")
			return
		}
		filter.StartTime = &t
	}
	if v := strings.TrimSpace(c.Query("end_time")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			response.BadRequest(c, "Invalid end_time, expect RFC3339")
			return
		}
		filter.EndTime = &t
	}

	result, err := h.svc.List(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, result.Items, int64(result.Total), result.Page, result.PageSize)
}

// GetThread 查看一场对话的详情（最近的若干轮，按时间正序）。
// GET /api/v1/admin/conversation-records/threads/:key
func (h *ConversationRecordHandler) GetThread(c *gin.Context) {
	thread, err := h.svc.GetThread(c.Request.Context(), c.Param("key"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, thread)
}

// DeleteThread 删除一场对话的全部记录。
// DELETE /api/v1/admin/conversation-records/threads/:key
func (h *ConversationRecordHandler) DeleteThread(c *gin.Context) {
	deleted, err := h.svc.DeleteThreads(c.Request.Context(), []string{c.Param("key")})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": deleted})
}

type conversationBatchDeleteRequest struct {
	Keys []string `json:"keys"`
}

// BatchDelete 批量删除若干场对话。
// POST /api/v1/admin/conversation-records/batch-delete
func (h *ConversationRecordHandler) BatchDelete(c *gin.Context) {
	var req conversationBatchDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	deleted, err := h.svc.DeleteThreads(c.Request.Context(), req.Keys)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": deleted})
}

// Clear 清空全部对话记录。
// POST /api/v1/admin/conversation-records/clear
func (h *ConversationRecordHandler) Clear(c *gin.Context) {
	deleted, err := h.svc.Clear(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": deleted})
}

// GetSettings 获取对话记录配置。
// GET /api/v1/admin/conversation-records/settings
func (h *ConversationRecordHandler) GetSettings(c *gin.Context) {
	settings, err := h.svc.GetSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

// UpdateSettings 更新对话记录配置，当前进程立即生效。
// PUT /api/v1/admin/conversation-records/settings
func (h *ConversationRecordHandler) UpdateSettings(c *gin.Context) {
	var req service.ConversationRecordSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	settings, err := h.svc.UpdateSettings(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}
