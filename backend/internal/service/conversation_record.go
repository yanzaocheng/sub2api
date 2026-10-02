package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/conversation"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// SettingKeyConversationRecordSettings 是对话记录配置（JSON）在 settings 表中的键。
const SettingKeyConversationRecordSettings = "conversation_record_settings"

// ErrConversationNotFound 对话不存在。
var ErrConversationNotFound = infraerrors.NotFound("CONVERSATION_NOT_FOUND", "conversation not found")

// ConversationRecordSettings 对话记录的运行配置，在「系统设置 → 功能开关」中维护。
type ConversationRecordSettings struct {
	// Enabled 是否保存用户与模型的对话内容。默认关闭。
	Enabled bool `json:"enabled"`
	// RetentionDays 记录保留天数，超期自动删除；0 表示永久保留。
	RetentionDays int `json:"retention_days"`
}

// ConversationRecord 一次网关请求对应的一轮对话。
//
// 同一场对话的各轮通过 ConversationKey 关联。每条记录只保存本轮新增的输入
// 与模型回复，历史部分已由此前的记录保存，因此体积与对话长度无关。
type ConversationRecord struct {
	ID              int64                  `json:"id"`
	CreatedAt       time.Time              `json:"created_at"`
	RequestID       string                 `json:"request_id"`
	ConversationKey string                 `json:"conversation_key"`
	UserID          *int64                 `json:"user_id,omitempty"`
	UserEmail       string                 `json:"user_email"`
	APIKeyID        *int64                 `json:"api_key_id,omitempty"`
	APIKeyName      string                 `json:"api_key_name"`
	GroupID         *int64                 `json:"group_id,omitempty"`
	Endpoint        string                 `json:"endpoint"`
	Model           string                 `json:"model"`
	Stream          bool                   `json:"stream"`
	DurationMs      int64                  `json:"duration_ms"`
	ClientIP        string                 `json:"client_ip"`
	UserAgent       string                 `json:"user_agent"`
	SystemPrompt    string                 `json:"system_prompt"`
	Messages        []conversation.Message `json:"messages"`
	Response        string                 `json:"response"`

	// 列表页摘要，入库时由内容生成；不随对话详情返回。
	PromptPreview   string `json:"-"`
	ResponsePreview string `json:"-"`
}

// approxSize 估算记录占用的内存字节数，用于限制写入队列的总体积。
func (r *ConversationRecord) approxSize() int64 {
	size := int64(512 + len(r.RequestID) + len(r.UserEmail) + len(r.APIKeyName) + len(r.Model) +
		len(r.SystemPrompt) + len(r.Response) + len(r.PromptPreview) + len(r.ResponsePreview) + len(r.UserAgent))
	for _, m := range r.Messages {
		size += int64(len(m.Role) + len(m.Content))
	}
	return size
}

// ConversationSummary 列表页的一行：一场对话的汇总。
type ConversationSummary struct {
	ConversationKey string    `json:"conversation_key"`
	UserID          *int64    `json:"user_id,omitempty"`
	UserEmail       string    `json:"user_email"`
	APIKeyName      string    `json:"api_key_name"`
	Endpoint        string    `json:"endpoint"`
	Model           string    `json:"model"`
	Turns           int       `json:"turns"`
	FirstAt         time.Time `json:"first_at"`
	LastAt          time.Time `json:"last_at"`
	FirstPrompt     string    `json:"first_prompt"`
	LastResponse    string    `json:"last_response"`
}

// ConversationFilter 对话列表的查询条件。条件作用于轮次：
// 只要某一轮命中，该对话就会出现，汇总里的轮数也只统计命中的轮次。
type ConversationFilter struct {
	Page     int
	PageSize int

	StartTime *time.Time
	EndTime   *time.Time
	UserID    *int64
	// User 对用户邮箱做模糊匹配。
	User string
	// Model 对模型名做模糊匹配。
	Model string
	// Query 对对话内容（用户输入、模型回复）做模糊匹配。
	Query string
}

// ConversationList 对话列表分页结果。
type ConversationList struct {
	Items    []*ConversationSummary `json:"items"`
	Total    int                    `json:"total"`
	Page     int                    `json:"page"`
	PageSize int                    `json:"page_size"`
}

// ConversationThread 一场对话的详情。
type ConversationThread struct {
	ConversationKey string `json:"conversation_key"`
	// Total 是这场对话的总轮数。Records 只包含最近的若干轮，
	// Total 大于 len(Records) 说明更早的轮次未返回。
	Total int `json:"total"`
	// Records 按时间正序排列。
	Records []*ConversationRecord `json:"records"`
}

// ConversationRecordRepository 对话记录持久化端口。
type ConversationRecordRepository interface {
	// BatchInsert 在一个事务内写入多条记录，任一条失败则整体回滚。
	BatchInsert(ctx context.Context, records []*ConversationRecord) (int64, error)
	ListConversations(ctx context.Context, filter *ConversationFilter) (*ConversationList, error)
	// GetThread 返回一场对话最近的至多 limit 轮；对话不存在时 Records 为空。
	GetThread(ctx context.Context, key string, limit int) (*ConversationThread, error)
	// DeleteThreads 删除若干场对话的全部记录，返回删除的行数。
	DeleteThreads(ctx context.Context, keys []string) (int64, error)
	// DeleteBefore 分批删除早于 cutoff 的记录，返回本批删除的行数。
	DeleteBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error)
	// DeleteAll 清空全部记录，返回删除的行数。
	DeleteAll(ctx context.Context) (int64, error)
}
