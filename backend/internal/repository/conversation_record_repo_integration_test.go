package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/conversation"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 对话记录的真实 PostgreSQL 验证：迁移脚本、COPY 批量写入、聚合列表、删除。
// 设置 CONVERSATION_RECORD_TEST_POSTGRES_DSN 指向一个可随意读写的测试库后运行；未设置时跳过。
// 每次运行使用独立 schema，结束后清理。
const conversationRecordPostgresTestEnv = "CONVERSATION_RECORD_TEST_POSTGRES_DSN"

func openConversationRecordTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(conversationRecordPostgresTestEnv))
	if dsn == "" {
		t.Skip(conversationRecordPostgresTestEnv + " is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer func() { _ = admin.Close() }()
	schema := fmt.Sprintf("conv_test_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, err := sql.Open("postgres", dsn)
		if err == nil {
			_, _ = cleanup.Exec("DROP SCHEMA " + schema + " CASCADE")
			_ = cleanup.Close()
		}
	})

	db, err := sql.Open("postgres", withSearchPath(t, dsn, schema))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.PingContext(ctx))

	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "241_conversation_records.sql"))
	require.NoError(t, err)
	// 迁移重放必须是幂等的（部署中断后会被再次执行）。
	for i := 0; i < 2; i++ {
		_, err = db.ExecContext(ctx, string(migration))
		require.NoError(t, err, "migration run %d", i+1)
	}
	return db
}

func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		require.NoError(t, err)
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return dsn + " search_path=" + schema
}

func convInt64(v int64) *int64 { return &v }

func convTestRecord(base time.Time, offset time.Duration, key string, userID int64, email, model, prompt, response string) *service.ConversationRecord {
	return &service.ConversationRecord{
		CreatedAt:       base.Add(offset),
		RequestID:       fmt.Sprintf("req-%s-%d", key, offset/time.Second),
		ConversationKey: key,
		UserID:          convInt64(userID),
		UserEmail:       email,
		APIKeyID:        convInt64(userID * 10),
		APIKeyName:      "key-" + email,
		GroupID:         convInt64(1),
		Endpoint:        "/v1/messages",
		Model:           model,
		Stream:          true,
		DurationMs:      1234,
		ClientIP:        "203.0.113.5",
		UserAgent:       "claude-cli/2.0",
		SystemPrompt:    "You are helpful.",
		Messages:        []conversation.Message{{Role: "user", Content: prompt}},
		Response:        response,
		PromptPreview:   conversation.Preview(prompt, conversation.PreviewChars),
		ResponsePreview: conversation.Preview(response, conversation.PreviewChars),
	}
}

func TestConversationRecordRepository_Postgres(t *testing.T) {
	db := openConversationRecordTestDB(t)
	repo := NewConversationRecordRepository(db)
	ctx := context.Background()
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)

	// 内容里的特殊字符必须原样往返：换行、制表符、反斜杠、引号、HTML、中日文、emoji、
	// 以及看起来像 JSON / COPY 控制序列的文本。
	nasty := "line1\nline2\ttab \\N \\. \"quoted\" <script>alert(1)</script> 你好，世界 🚀 {\"a\":[1,2]}"
	longResponse := strings.Repeat("长文本", conversation.MaxResponseChars/3)

	records := []*service.ConversationRecord{
		convTestRecord(base, 0, "conv-a", 1, "alice@example.com", "claude-sonnet-4-5", "first question", "first answer"),
		convTestRecord(base, 10*time.Second, "conv-a", 1, "alice@example.com", "claude-opus-4-1", nasty, "second answer about kubernetes"),
		convTestRecord(base, 20*time.Second, "conv-a", 1, "alice@example.com", "claude-opus-4-1", "third question", longResponse),
		convTestRecord(base, 5*time.Second, "conv-b", 2, "bob@example.com", "gpt-5", "bob asks", "bob gets answer"),
	}
	records[3].UserID, records[3].APIKeyID, records[3].GroupID = convInt64(2), nil, nil
	inserted, err := repo.BatchInsert(ctx, records)
	require.NoError(t, err)
	require.Equal(t, int64(4), inserted)

	t.Run("thread roundtrip", func(t *testing.T) {
		thread, err := repo.GetThread(ctx, "conv-a", 10)
		require.NoError(t, err)
		require.Equal(t, 3, thread.Total)
		require.Len(t, thread.Records, 3)
		// 按时间正序。
		require.Equal(t, "first answer", thread.Records[0].Response)
		require.Equal(t, nasty, thread.Records[1].Messages[0].Content, "特殊字符必须原样保存")
		require.Equal(t, longResponse, thread.Records[2].Response)

		first := thread.Records[0]
		require.NotZero(t, first.ID)
		require.Equal(t, "alice@example.com", first.UserEmail)
		require.Equal(t, int64(1), *first.UserID)
		require.Equal(t, int64(10), *first.APIKeyID)
		require.Equal(t, int64(1), *first.GroupID)
		require.Equal(t, "/v1/messages", first.Endpoint)
		require.True(t, first.Stream)
		require.Equal(t, int64(1234), first.DurationMs)
		require.Equal(t, "You are helpful.", first.SystemPrompt)
		require.WithinDuration(t, base, first.CreatedAt, time.Second)

		bob, err := repo.GetThread(ctx, "conv-b", 10)
		require.NoError(t, err)
		require.Nil(t, bob.Records[0].APIKeyID, "可空列应还原为 nil")
		require.Nil(t, bob.Records[0].GroupID)
	})

	t.Run("thread limit keeps the most recent turns", func(t *testing.T) {
		thread, err := repo.GetThread(ctx, "conv-a", 2)
		require.NoError(t, err)
		require.Equal(t, 3, thread.Total)
		require.Len(t, thread.Records, 2)
		require.Equal(t, longResponse, thread.Records[1].Response)
		require.Equal(t, "second answer about kubernetes", thread.Records[0].Response)
	})

	t.Run("missing thread", func(t *testing.T) {
		thread, err := repo.GetThread(ctx, "nope", 10)
		require.NoError(t, err)
		require.Zero(t, thread.Total)
		require.Empty(t, thread.Records)
	})

	t.Run("list groups turns into conversations ordered by latest activity", func(t *testing.T) {
		list, err := repo.ListConversations(ctx, &service.ConversationFilter{})
		require.NoError(t, err)
		require.Equal(t, 2, list.Total)
		require.Len(t, list.Items, 2)

		// conv-a 最近一轮在 +20s，conv-b 在 +5s。
		a, b := list.Items[0], list.Items[1]
		require.Equal(t, "conv-a", a.ConversationKey)
		require.Equal(t, 3, a.Turns)
		require.Equal(t, "alice@example.com", a.UserEmail)
		require.Equal(t, "first question", a.FirstPrompt)
		require.Equal(t, "claude-opus-4-1", a.Model, "模型取最近一轮")
		require.Equal(t, conversation.Preview(longResponse, conversation.PreviewChars), a.LastResponse)
		require.True(t, a.LastAt.After(a.FirstAt))

		require.Equal(t, "conv-b", b.ConversationKey)
		require.Equal(t, 1, b.Turns)
		require.Equal(t, int64(2), *b.UserID)
	})

	t.Run("list pagination", func(t *testing.T) {
		page1, err := repo.ListConversations(ctx, &service.ConversationFilter{Page: 1, PageSize: 1})
		require.NoError(t, err)
		require.Equal(t, 2, page1.Total)
		require.Len(t, page1.Items, 1)
		require.Equal(t, "conv-a", page1.Items[0].ConversationKey)

		page2, err := repo.ListConversations(ctx, &service.ConversationFilter{Page: 2, PageSize: 1})
		require.NoError(t, err)
		require.Len(t, page2.Items, 1)
		require.Equal(t, "conv-b", page2.Items[0].ConversationKey)

		page3, err := repo.ListConversations(ctx, &service.ConversationFilter{Page: 3, PageSize: 1})
		require.NoError(t, err)
		require.Empty(t, page3.Items)
	})

	t.Run("list filters", func(t *testing.T) {
		keys := func(f *service.ConversationFilter) []string {
			list, err := repo.ListConversations(ctx, f)
			require.NoError(t, err)
			out := make([]string, 0, len(list.Items))
			for _, item := range list.Items {
				out = append(out, item.ConversationKey)
			}
			return out
		}
		require.Equal(t, []string{"conv-b"}, keys(&service.ConversationFilter{User: "BOB"}), "邮箱模糊匹配，不区分大小写")
		require.Equal(t, []string{"conv-a"}, keys(&service.ConversationFilter{UserID: convInt64(1)}))
		require.Equal(t, []string{"conv-b"}, keys(&service.ConversationFilter{Model: "gpt"}))
		require.Equal(t, []string{"conv-a"}, keys(&service.ConversationFilter{Model: "OPUS"}))
		require.Equal(t, []string{"conv-a"}, keys(&service.ConversationFilter{Query: "kubernetes"}), "命中回复")
		require.Equal(t, []string{"conv-a"}, keys(&service.ConversationFilter{Query: "你好，世界"}), "命中输入消息（JSON 内）")
		require.Equal(t, []string{"conv-b"}, keys(&service.ConversationFilter{Query: "bob asks"}), "命中摘要")
		require.Empty(t, keys(&service.ConversationFilter{Query: "no such text"}))
		// 通配符按字面量匹配，不能被用户输入当作模式。
		require.Empty(t, keys(&service.ConversationFilter{Query: "%"}))
		require.Empty(t, keys(&service.ConversationFilter{User: "_"}))

		start := base.Add(8 * time.Second)
		require.ElementsMatch(t, []string{"conv-a"}, keys(&service.ConversationFilter{StartTime: &start}))
		end := base.Add(7 * time.Second)
		require.ElementsMatch(t, []string{"conv-a", "conv-b"}, keys(&service.ConversationFilter{EndTime: &end}))

		// 条件作用于轮次：只统计命中的轮次。
		list, err := repo.ListConversations(ctx, &service.ConversationFilter{Model: "sonnet"})
		require.NoError(t, err)
		require.Len(t, list.Items, 1)
		require.Equal(t, 1, list.Items[0].Turns)
		require.Equal(t, "first question", list.Items[0].FirstPrompt)
	})

	t.Run("failed batch rolls back completely", func(t *testing.T) {
		good := convTestRecord(base, time.Minute, "conv-rollback", 3, "carol@example.com", "m", "q", "a")
		bad := convTestRecord(base, time.Minute, "conv-rollback", 3, "carol@example.com", "m", "q", "bad \xff byte")
		_, err := repo.BatchInsert(ctx, []*service.ConversationRecord{good, bad})
		require.Error(t, err)
		thread, err := repo.GetThread(ctx, "conv-rollback", 10)
		require.NoError(t, err)
		require.Zero(t, thread.Total, "整批回滚，好记录也不能留下")
	})

	t.Run("delete threads", func(t *testing.T) {
		deleted, err := repo.DeleteThreads(ctx, []string{"conv-b", "does-not-exist"})
		require.NoError(t, err)
		require.Equal(t, int64(1), deleted)
		list, err := repo.ListConversations(ctx, &service.ConversationFilter{})
		require.NoError(t, err)
		require.Equal(t, 1, list.Total)
	})

	t.Run("delete before cutoff in batches", func(t *testing.T) {
		// conv-a 三轮分别在 base、+10s、+20s。
		cutoff := base.Add(15 * time.Second)
		deleted, err := repo.DeleteBefore(ctx, cutoff, 1)
		require.NoError(t, err)
		require.Equal(t, int64(1), deleted, "受批量上限约束")
		deleted, err = repo.DeleteBefore(ctx, cutoff, 100)
		require.NoError(t, err)
		require.Equal(t, int64(1), deleted)
		deleted, err = repo.DeleteBefore(ctx, cutoff, 100)
		require.NoError(t, err)
		require.Zero(t, deleted)

		thread, err := repo.GetThread(ctx, "conv-a", 10)
		require.NoError(t, err)
		require.Equal(t, 1, thread.Total)
		require.Equal(t, longResponse, thread.Records[0].Response)
	})

	t.Run("delete all", func(t *testing.T) {
		deleted, err := repo.DeleteAll(ctx)
		require.NoError(t, err)
		require.Equal(t, int64(1), deleted)
		list, err := repo.ListConversations(ctx, &service.ConversationFilter{})
		require.NoError(t, err)
		require.Zero(t, list.Total)
		require.Empty(t, list.Items)
	})
}

// 写入的字段超过列宽时必须被截断，而不是让整批 COPY 失败。
func TestConversationRecordRepository_Postgres_TruncatesOversizedColumns(t *testing.T) {
	db := openConversationRecordTestDB(t)
	repo := NewConversationRecordRepository(db)
	ctx := context.Background()

	rec := convTestRecord(time.Now().UTC(), 0, strings.Repeat("k", 200), 1, strings.Repeat("e", 400)+"@x.com", strings.Repeat("m", 400), "q", "a")
	rec.RequestID = strings.Repeat("r", 300)
	rec.UserAgent = strings.Repeat("u", 600)
	rec.PromptPreview = strings.Repeat("p", 600)
	rec.Messages = nil

	_, err := repo.BatchInsert(ctx, []*service.ConversationRecord{rec})
	require.NoError(t, err)

	list, err := repo.ListConversations(ctx, &service.ConversationFilter{})
	require.NoError(t, err)
	require.Equal(t, 1, list.Total)
	require.LessOrEqual(t, len(list.Items[0].ConversationKey), 64)
	require.LessOrEqual(t, len(list.Items[0].Model), 255)
}

// 流式长请求先发起、后落库：线程与列表都应按发起时间排序，而不是按写入顺序。
func TestConversationRecordRepository_Postgres_OrdersByStartTime(t *testing.T) {
	db := openConversationRecordTestDB(t)
	repo := NewConversationRecordRepository(db)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	slow := convTestRecord(base, 0, "conv-c", 1, "alice@example.com", "m", "slow request", "slow answer")
	fast := convTestRecord(base, 5*time.Second, "conv-c", 1, "alice@example.com", "m", "fast request", "fast answer")
	other := convTestRecord(base, 2*time.Second, "conv-d", 2, "bob@example.com", "m", "other", "other answer")
	// 写入顺序：fast、other、slow（slow 最后写入，但最早发起）。
	_, err := repo.BatchInsert(ctx, []*service.ConversationRecord{fast, other, slow})
	require.NoError(t, err)

	thread, err := repo.GetThread(ctx, "conv-c", 10)
	require.NoError(t, err)
	require.Equal(t, "slow answer", thread.Records[0].Response)
	require.Equal(t, "fast answer", thread.Records[1].Response)

	list, err := repo.ListConversations(ctx, &service.ConversationFilter{})
	require.NoError(t, err)
	require.Len(t, list.Items, 2)
	require.Equal(t, "conv-c", list.Items[0].ConversationKey, "conv-c 最近一轮发起于 +5s，晚于 conv-d 的 +2s")
	require.Equal(t, "slow request", list.Items[0].FirstPrompt, "首轮是最早发起的 slow，而不是最先写入的 fast")
	require.Equal(t, "fast answer", list.Items[0].LastResponse)
}
