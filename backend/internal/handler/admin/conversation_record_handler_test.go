package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/conversation"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type convHandlerRepo struct {
	mu          sync.Mutex
	lastFilter  *service.ConversationFilter
	list        *service.ConversationList
	thread      *service.ConversationThread
	deletedKeys [][]string
	cleared     bool
}

func (r *convHandlerRepo) BatchInsert(context.Context, []*service.ConversationRecord) (int64, error) {
	return 0, nil
}

func (r *convHandlerRepo) ListConversations(_ context.Context, f *service.ConversationFilter) (*service.ConversationList, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastFilter = f
	if r.list != nil {
		return r.list, nil
	}
	return &service.ConversationList{Items: []*service.ConversationSummary{}, Page: f.Page, PageSize: f.PageSize}, nil
}

func (r *convHandlerRepo) GetThread(_ context.Context, key string, _ int) (*service.ConversationThread, error) {
	if r.thread != nil && r.thread.ConversationKey == key {
		return r.thread, nil
	}
	return &service.ConversationThread{ConversationKey: key}, nil
}

func (r *convHandlerRepo) DeleteThreads(_ context.Context, keys []string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletedKeys = append(r.deletedKeys, keys)
	return int64(len(keys)) * 3, nil
}

func (r *convHandlerRepo) DeleteBefore(context.Context, time.Time, int) (int64, error) { return 0, nil }

func (r *convHandlerRepo) DeleteAll(context.Context) (int64, error) {
	r.cleared = true
	return 11, nil
}

type convHandlerSettings struct {
	values map[string]string
}

func (s *convHandlerSettings) Get(context.Context, string) (*service.Setting, error) {
	panic("unexpected Get call")
}

func (s *convHandlerSettings) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}

func (s *convHandlerSettings) Set(_ context.Context, key, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}

func (s *convHandlerSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}
func (s *convHandlerSettings) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}
func (s *convHandlerSettings) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}
func (s *convHandlerSettings) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

func newConvHandlerRouter(repo *convHandlerRepo, settings *convHandlerSettings) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewConversationRecordHandler(service.NewConversationRecordService(repo, settings))
	r := gin.New()
	g := r.Group("/api/v1/admin/conversation-records")
	g.GET("", h.List)
	g.GET("/settings", h.GetSettings)
	g.PUT("/settings", h.UpdateSettings)
	g.POST("/batch-delete", h.BatchDelete)
	g.POST("/clear", h.Clear)
	g.GET("/threads/:key", h.GetThread)
	g.DELETE("/threads/:key", h.DeleteThread)
	return r
}

type convEnvelope struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

func convDo(t *testing.T, r *gin.Engine, method, path, body string) (*httptest.ResponseRecorder, convEnvelope) {
	t.Helper()
	var reader *bytes.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var env convEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w, env
}

func TestConversationRecordHandler_ListParsesFilters(t *testing.T) {
	repo := &convHandlerRepo{list: &service.ConversationList{
		Items:    []*service.ConversationSummary{{ConversationKey: "k1", Turns: 3, UserEmail: "alice@example.com"}},
		Total:    1,
		Page:     2,
		PageSize: 10,
	}}
	r := newConvHandlerRouter(repo, &convHandlerSettings{})

	w, env := convDo(t, r, http.MethodGet,
		"/api/v1/admin/conversation-records?page=2&page_size=10&user=alice&model=claude&q=hello&user_id=7"+
			"&start_time=2026-09-01T00:00:00Z&end_time=2026-09-30T23:59:59Z", "")

	require.Equal(t, http.StatusOK, w.Code)
	require.Zero(t, env.Code)
	var page struct {
		Items []service.ConversationSummary `json:"items"`
		Total int                           `json:"total"`
	}
	require.NoError(t, json.Unmarshal(env.Data, &page))
	require.Equal(t, 1, page.Total)
	require.Equal(t, "k1", page.Items[0].ConversationKey)

	f := repo.lastFilter
	require.Equal(t, 2, f.Page)
	require.Equal(t, 10, f.PageSize)
	require.Equal(t, "alice", f.User)
	require.Equal(t, "claude", f.Model)
	require.Equal(t, "hello", f.Query)
	require.Equal(t, int64(7), *f.UserID)
	require.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), f.StartTime.UTC())
	require.Equal(t, time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC), f.EndTime.UTC())
}

func TestConversationRecordHandler_ListRejectsBadInput(t *testing.T) {
	r := newConvHandlerRouter(&convHandlerRepo{}, &convHandlerSettings{})
	for _, query := range []string{"user_id=abc", "user_id=-1", "start_time=yesterday", "end_time=2026-09-01"} {
		w, _ := convDo(t, r, http.MethodGet, "/api/v1/admin/conversation-records?"+query, "")
		require.Equal(t, http.StatusBadRequest, w.Code, query)
	}
}

func TestConversationRecordHandler_ListCapsPageSize(t *testing.T) {
	repo := &convHandlerRepo{}
	r := newConvHandlerRouter(repo, &convHandlerSettings{})
	convDo(t, r, http.MethodGet, "/api/v1/admin/conversation-records?page_size=1000", "")
	require.Equal(t, 100, repo.lastFilter.PageSize)
}

func TestConversationRecordHandler_GetThread(t *testing.T) {
	repo := &convHandlerRepo{thread: &service.ConversationThread{
		ConversationKey: "abc",
		Total:           1,
		Records: []*service.ConversationRecord{{
			ID:       5,
			Model:    "claude",
			Messages: []conversation.Message{{Role: "user", Content: "hi"}},
			Response: "hello",
		}},
	}}
	r := newConvHandlerRouter(repo, &convHandlerSettings{})

	w, env := convDo(t, r, http.MethodGet, "/api/v1/admin/conversation-records/threads/abc", "")
	require.Equal(t, http.StatusOK, w.Code)
	var thread service.ConversationThread
	require.NoError(t, json.Unmarshal(env.Data, &thread))
	require.Equal(t, "abc", thread.ConversationKey)
	require.Len(t, thread.Records, 1)
	require.Equal(t, "hello", thread.Records[0].Response)
	require.Equal(t, "hi", thread.Records[0].Messages[0].Content)

	w, _ = convDo(t, r, http.MethodGet, "/api/v1/admin/conversation-records/threads/missing", "")
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestConversationRecordHandler_Delete(t *testing.T) {
	repo := &convHandlerRepo{}
	r := newConvHandlerRouter(repo, &convHandlerSettings{})

	w, env := convDo(t, r, http.MethodDelete, "/api/v1/admin/conversation-records/threads/abc", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"deleted":3}`, string(env.Data))

	w, env = convDo(t, r, http.MethodPost, "/api/v1/admin/conversation-records/batch-delete", `{"keys":["a","b"]}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"deleted":6}`, string(env.Data))
	require.Equal(t, [][]string{{"abc"}, {"a", "b"}}, repo.deletedKeys)

	w, _ = convDo(t, r, http.MethodPost, "/api/v1/admin/conversation-records/batch-delete", `{"keys":[]}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	w, _ = convDo(t, r, http.MethodPost, "/api/v1/admin/conversation-records/batch-delete", `not json`)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestConversationRecordHandler_Clear(t *testing.T) {
	repo := &convHandlerRepo{}
	r := newConvHandlerRouter(repo, &convHandlerSettings{})
	w, env := convDo(t, r, http.MethodPost, "/api/v1/admin/conversation-records/clear", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"deleted":11}`, string(env.Data))
	require.True(t, repo.cleared)
}

func TestConversationRecordHandler_Settings(t *testing.T) {
	settings := &convHandlerSettings{}
	r := newConvHandlerRouter(&convHandlerRepo{}, settings)

	// 从未配置过：默认关闭、永久保留。
	w, env := convDo(t, r, http.MethodGet, "/api/v1/admin/conversation-records/settings", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"enabled":false,"retention_days":0}`, string(env.Data))

	w, env = convDo(t, r, http.MethodPut, "/api/v1/admin/conversation-records/settings", `{"enabled":true,"retention_days":30}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"enabled":true,"retention_days":30}`, string(env.Data))
	require.JSONEq(t, `{"enabled":true,"retention_days":30}`, settings.values[service.SettingKeyConversationRecordSettings])

	w, env = convDo(t, r, http.MethodGet, "/api/v1/admin/conversation-records/settings", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"enabled":true,"retention_days":30}`, string(env.Data))

	w, _ = convDo(t, r, http.MethodPut, "/api/v1/admin/conversation-records/settings", `{"enabled":true,"retention_days":-5}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	w, _ = convDo(t, r, http.MethodPut, "/api/v1/admin/conversation-records/settings", `{"enabled":"yes"}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
}
