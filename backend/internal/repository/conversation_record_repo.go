package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/conversation"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// conversationRecordRepository 对话记录仓储（raw SQL）。
type conversationRecordRepository struct {
	db *sql.DB
}

// NewConversationRecordRepository 创建对话记录仓储。
func NewConversationRecordRepository(db *sql.DB) service.ConversationRecordRepository {
	return &conversationRecordRepository{db: db}
}

var conversationInsertColumns = []string{
	"created_at", "request_id", "conversation_key", "user_id", "user_email", "api_key_id", "api_key_name",
	"group_id", "endpoint", "model", "stream", "duration_ms", "client_ip", "user_agent",
	"system_prompt", "messages", "response", "prompt_preview", "response_preview",
}

func conversationInsertValues(rec *service.ConversationRecord) ([]any, error) {
	messages := rec.Messages
	if messages == nil {
		messages = []conversation.Message{}
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return nil, fmt.Errorf("encode conversation messages: %w", err)
	}
	createdAt := rec.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return []any{
		createdAt.UTC(),
		truncateConversationColumn(rec.RequestID, 128),
		truncateConversationColumn(rec.ConversationKey, 64),
		nullInt64Ptr(rec.UserID),
		truncateConversationColumn(rec.UserEmail, 255),
		nullInt64Ptr(rec.APIKeyID),
		truncateConversationColumn(rec.APIKeyName, 255),
		nullInt64Ptr(rec.GroupID),
		truncateConversationColumn(rec.Endpoint, 64),
		truncateConversationColumn(rec.Model, 255),
		rec.Stream,
		rec.DurationMs,
		truncateConversationColumn(rec.ClientIP, 64),
		truncateConversationColumn(rec.UserAgent, 255),
		rec.SystemPrompt,
		string(encoded),
		rec.Response,
		truncateConversationColumn(rec.PromptPreview, 255),
		truncateConversationColumn(rec.ResponsePreview, 255),
	}, nil
}

// BatchInsert 用 COPY 在一个事务内批量写入；任一条失败整体回滚。
func (r *conversationRecordRepository) BatchInsert(ctx context.Context, records []*service.ConversationRecord) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil conversation record repository")
	}
	if len(records) == 0 {
		return 0, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx, pq.CopyIn("conversation_records", conversationInsertColumns...))
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}

	var inserted int64
	for _, rec := range records {
		if rec == nil {
			continue
		}
		values, err := conversationInsertValues(rec)
		if err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return 0, err
		}
		if _, err := stmt.ExecContext(ctx, values...); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return 0, err
		}
		inserted++
	}
	if _, err := stmt.ExecContext(ctx); err != nil {
		_ = stmt.Close()
		_ = tx.Rollback()
		return 0, err
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

func buildConversationWhere(filter *service.ConversationFilter) (string, []any) {
	clauses := []string{"1=1"}
	var args []any
	add := func(clause string, arg any) {
		args = append(args, arg)
		clauses = append(clauses, strings.ReplaceAll(clause, "$?", "$"+itoa(len(args))))
	}
	like := func(v string) string { return "%" + escapeLikePattern(v) + "%" }

	if filter.StartTime != nil {
		add("r.created_at >= $?", filter.StartTime.UTC())
	}
	if filter.EndTime != nil {
		add("r.created_at <= $?", filter.EndTime.UTC())
	}
	if filter.UserID != nil {
		add("r.user_id = $?", *filter.UserID)
	}
	if v := strings.TrimSpace(filter.User); v != "" {
		add("r.user_email ILIKE $?", like(v))
	}
	if v := strings.TrimSpace(filter.Model); v != "" {
		add("r.model ILIKE $?", like(v))
	}
	if v := strings.TrimSpace(filter.Query); v != "" {
		add("(r.prompt_preview ILIKE $? OR r.response ILIKE $? OR r.messages::text ILIKE $?)", like(v))
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// ListConversations 按对话聚合：每场对话一行，按最近一轮的先后倒序。
func (r *conversationRecordRepository) ListConversations(ctx context.Context, filter *service.ConversationFilter) (*service.ConversationList, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil conversation record repository")
	}
	if filter == nil {
		filter = &service.ConversationFilter{}
	}
	page := filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}

	where, args := buildConversationWhere(filter)
	var total int
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(DISTINCT r.conversation_key) FROM conversation_records r "+where, args...).Scan(&total); err != nil {
		return nil, err
	}

	// 先在命中的轮次上分组并分页，再回表取首轮与末轮的展示字段，
	// 避免为整张表物化摘要列。首轮 / 末轮按请求发起时间判定（同刻按 id 打破平局）：
	// 流式长请求先发起却后落库，按 id 排会和界面上显示的时间对不上。
	query := `
WITH grouped AS (
  SELECT r.conversation_key,
         COUNT(*) AS turns,
         (ARRAY_AGG(r.id ORDER BY r.created_at ASC, r.id ASC))[1] AS first_id,
         (ARRAY_AGG(r.id ORDER BY r.created_at DESC, r.id DESC))[1] AS last_id,
         MAX(r.created_at) AS last_at
  FROM conversation_records r
  ` + where + `
  GROUP BY r.conversation_key
  ORDER BY last_at DESC, last_id DESC
  LIMIT $` + itoa(len(args)+1) + ` OFFSET $` + itoa(len(args)+2) + `
)
SELECT g.conversation_key, g.turns, f.created_at, l.created_at,
       f.user_id, f.user_email, f.api_key_name, f.endpoint, f.prompt_preview,
       l.model, l.response_preview
FROM grouped g
JOIN conversation_records f ON f.id = g.first_id
JOIN conversation_records l ON l.id = g.last_id
ORDER BY g.last_at DESC, g.last_id DESC`
	rows, err := r.db.QueryContext(ctx, query, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]*service.ConversationSummary, 0, pageSize)
	for rows.Next() {
		item := &service.ConversationSummary{}
		var userID sql.NullInt64
		if err := rows.Scan(
			&item.ConversationKey, &item.Turns, &item.FirstAt, &item.LastAt,
			&userID, &item.UserEmail, &item.APIKeyName, &item.Endpoint, &item.FirstPrompt,
			&item.Model, &item.LastResponse,
		); err != nil {
			return nil, err
		}
		if userID.Valid {
			v := userID.Int64
			item.UserID = &v
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &service.ConversationList{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

const conversationThreadColumns = `id, created_at, request_id, conversation_key, user_id, user_email,
api_key_id, api_key_name, group_id, endpoint, model, stream, duration_ms, client_ip, user_agent,
system_prompt, messages::text, response`

// GetThread 取一场对话最近的至多 limit 轮，按发起时间正序返回。
func (r *conversationRecordRepository) GetThread(ctx context.Context, key string, limit int) (*service.ConversationThread, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil conversation record repository")
	}
	thread := &service.ConversationThread{ConversationKey: key, Records: []*service.ConversationRecord{}}
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM conversation_records WHERE conversation_key = $1", key).Scan(&thread.Total); err != nil {
		return nil, err
	}
	if thread.Total == 0 {
		return thread, nil
	}

	rows, err := r.db.QueryContext(ctx, `
SELECT * FROM (
  SELECT `+conversationThreadColumns+`
  FROM conversation_records WHERE conversation_key = $1
  ORDER BY created_at DESC, id DESC LIMIT $2
) t ORDER BY created_at ASC, id ASC`, key, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		rec := &service.ConversationRecord{}
		var userID, apiKeyID, groupID sql.NullInt64
		var messages string
		if err := rows.Scan(
			&rec.ID, &rec.CreatedAt, &rec.RequestID, &rec.ConversationKey, &userID, &rec.UserEmail,
			&apiKeyID, &rec.APIKeyName, &groupID, &rec.Endpoint, &rec.Model, &rec.Stream, &rec.DurationMs,
			&rec.ClientIP, &rec.UserAgent, &rec.SystemPrompt, &messages, &rec.Response,
		); err != nil {
			return nil, err
		}
		if userID.Valid {
			v := userID.Int64
			rec.UserID = &v
		}
		if apiKeyID.Valid {
			v := apiKeyID.Int64
			rec.APIKeyID = &v
		}
		if groupID.Valid {
			v := groupID.Int64
			rec.GroupID = &v
		}
		rec.Messages = []conversation.Message{}
		if err := json.Unmarshal([]byte(messages), &rec.Messages); err != nil {
			// 单条记录的消息损坏不应导致整场对话无法查看。
			rec.Messages = []conversation.Message{}
		}
		thread.Records = append(thread.Records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return thread, nil
}

func (r *conversationRecordRepository) DeleteThreads(ctx context.Context, keys []string) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil conversation record repository")
	}
	if len(keys) == 0 {
		return 0, nil
	}
	res, err := r.db.ExecContext(ctx, "DELETE FROM conversation_records WHERE conversation_key = ANY($1)", pq.Array(keys))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *conversationRecordRepository) DeleteBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil conversation record repository")
	}
	res, err := r.db.ExecContext(ctx, `
WITH batch AS (
  SELECT id FROM conversation_records WHERE created_at < $1 ORDER BY created_at LIMIT $2
)
DELETE FROM conversation_records WHERE id IN (SELECT id FROM batch)`, cutoff.UTC(), limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteAll 清空全表。先计数再 TRUNCATE：整表删除比逐行 DELETE 快得多，也不会留下大量死元组。
func (r *conversationRecordRepository) DeleteAll(ctx context.Context) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil conversation record repository")
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversation_records").Scan(&total); err != nil {
		return 0, err
	}
	if _, err := r.db.ExecContext(ctx, "TRUNCATE TABLE conversation_records"); err != nil {
		return 0, err
	}
	return total, nil
}

// truncateConversationColumn 按字符截断到 VARCHAR(n) 的上限。VARCHAR 的长度以字符计，
// 包内通用的 truncateString 按字节截断，会把中文摘要砍掉三分之二。
func truncateConversationColumn(s string, max int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}
