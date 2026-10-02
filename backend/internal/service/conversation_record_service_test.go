package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/conversation"
	"github.com/stretchr/testify/require"
)

// convSettingRepoFake 是 SettingRepository 的内存实现，只实现对话记录用到的读写。
type convSettingRepoFake struct {
	mu       sync.Mutex
	values   map[string]string
	getErr   error
	getCalls int
}

func (f *convSettingRepoFake) Get(context.Context, string) (*Setting, error) {
	panic("unexpected Get call")
}

func (f *convSettingRepoFake) GetValue(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.getErr != nil {
		return "", f.getErr
	}
	v, ok := f.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}

func (f *convSettingRepoFake) Set(_ context.Context, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[key] = value
	return nil
}

func (f *convSettingRepoFake) GetMultiple(context.Context, []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}
func (f *convSettingRepoFake) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}
func (f *convSettingRepoFake) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}
func (f *convSettingRepoFake) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

func (f *convSettingRepoFake) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getErr = err
}

func (f *convSettingRepoFake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getCalls
}

// convRecordRepoFake 是 ConversationRecordRepository 的内存实现。
type convRecordRepoFake struct {
	mu sync.Mutex

	batches   [][]*ConversationRecord
	stored    []*ConversationRecord
	badMarker string // 响应里包含该文本的记录写入时报错，模拟数据库拒绝某一条数据

	deleteBeforeCalls []time.Time
	deleteBeforeLeft  []int64 // 依次返回的每批删除条数
	deletedKeys       [][]string
	threadTotal       int
	threadRecords     []*ConversationRecord
}

func (f *convRecordRepoFake) BatchInsert(_ context.Context, records []*ConversationRecord) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, records)
	if f.badMarker != "" {
		for _, rec := range records {
			if strings.Contains(rec.Response, f.badMarker) {
				return 0, errors.New("invalid byte sequence")
			}
		}
	}
	f.stored = append(f.stored, records...)
	return int64(len(records)), nil
}

func (f *convRecordRepoFake) ListConversations(context.Context, *ConversationFilter) (*ConversationList, error) {
	return &ConversationList{}, nil
}

func (f *convRecordRepoFake) GetThread(_ context.Context, key string, _ int) (*ConversationThread, error) {
	return &ConversationThread{ConversationKey: key, Total: f.threadTotal, Records: f.threadRecords}, nil
}

func (f *convRecordRepoFake) DeleteThreads(_ context.Context, keys []string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedKeys = append(f.deletedKeys, keys)
	return int64(len(keys)), nil
}

func (f *convRecordRepoFake) DeleteBefore(_ context.Context, cutoff time.Time, _ int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteBeforeCalls = append(f.deleteBeforeCalls, cutoff)
	if len(f.deleteBeforeLeft) == 0 {
		return 0, nil
	}
	n := f.deleteBeforeLeft[0]
	f.deleteBeforeLeft = f.deleteBeforeLeft[1:]
	return n, nil
}

func (f *convRecordRepoFake) DeleteAll(context.Context) (int64, error) { return 7, nil }

func (f *convRecordRepoFake) storedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.stored)
}

func newConvService(repo *convRecordRepoFake, settings *convSettingRepoFake) *ConversationRecordService {
	return NewConversationRecordService(repo, settings)
}

func convRecord(id string) *ConversationRecord {
	return &ConversationRecord{
		RequestID:       id,
		ConversationKey: "key-" + id,
		Model:           "m",
		Messages:        []conversation.Message{{Role: "user", Content: "hi " + id}},
		Response:        "answer " + id,
	}
}

func TestConversationRecordService_EnabledDefaultsToOff(t *testing.T) {
	svc := newConvService(&convRecordRepoFake{}, &convSettingRepoFake{})
	require.False(t, svc.Enabled(context.Background()))

	var nilSvc *ConversationRecordService
	require.False(t, nilSvc.Enabled(context.Background()))
}

func TestConversationRecordService_UpdateSettingsTakesEffectImmediately(t *testing.T) {
	settings := &convSettingRepoFake{}
	svc := newConvService(&convRecordRepoFake{}, settings)
	ctx := context.Background()

	require.False(t, svc.Enabled(ctx))
	saved, err := svc.UpdateSettings(ctx, ConversationRecordSettings{Enabled: true, RetentionDays: 30})
	require.NoError(t, err)
	require.Equal(t, ConversationRecordSettings{Enabled: true, RetentionDays: 30}, saved)

	// 保存后无需等待缓存过期，当前进程立刻生效。
	require.True(t, svc.Enabled(ctx))
	require.Contains(t, settings.values[SettingKeyConversationRecordSettings], `"enabled":true`)

	got, err := svc.GetSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, saved, got)

	_, err = svc.UpdateSettings(ctx, ConversationRecordSettings{Enabled: false})
	require.NoError(t, err)
	require.False(t, svc.Enabled(ctx))
}

func TestConversationRecordService_UpdateSettingsValidatesRetention(t *testing.T) {
	svc := newConvService(&convRecordRepoFake{}, &convSettingRepoFake{})
	for _, days := range []int{-1, conversationRetentionDaysMax + 1} {
		_, err := svc.UpdateSettings(context.Background(), ConversationRecordSettings{Enabled: true, RetentionDays: days})
		require.Error(t, err, days)
	}
	_, err := svc.UpdateSettings(context.Background(), ConversationRecordSettings{Enabled: true, RetentionDays: conversationRetentionDaysMax})
	require.NoError(t, err)
}

func TestConversationRecordService_EnabledIsCached(t *testing.T) {
	settings := &convSettingRepoFake{values: map[string]string{
		SettingKeyConversationRecordSettings: `{"enabled":true,"retention_days":0}`,
	}}
	svc := newConvService(&convRecordRepoFake{}, settings)

	for i := 0; i < 100; i++ {
		require.True(t, svc.Enabled(context.Background()))
	}
	// 网关每个请求都会调用 Enabled，必须只在缓存过期时才读数据库。
	require.Equal(t, 1, settings.calls())
}

func TestConversationRecordService_ReloadsAfterTTL(t *testing.T) {
	settings := &convSettingRepoFake{values: map[string]string{
		SettingKeyConversationRecordSettings: `{"enabled":false}`,
	}}
	svc := newConvService(&convRecordRepoFake{}, settings)
	require.False(t, svc.Enabled(context.Background()))

	// 另一个节点修改了配置；本节点在缓存过期后感知到。
	settings.values[SettingKeyConversationRecordSettings] = `{"enabled":true}`
	require.False(t, svc.Enabled(context.Background()), "缓存未过期前沿用旧值")

	cached := svc.settings.Load()
	svc.settings.Store(&cachedConversationSettings{settings: cached.settings, expiresAt: time.Now().Add(-time.Second)})
	require.True(t, svc.Enabled(context.Background()))
}

func TestConversationRecordService_ReadFailureKeepsLastKnownValue(t *testing.T) {
	settings := &convSettingRepoFake{values: map[string]string{
		SettingKeyConversationRecordSettings: `{"enabled":true}`,
	}}
	svc := newConvService(&convRecordRepoFake{}, settings)
	require.True(t, svc.Enabled(context.Background()))

	settings.setErr(errors.New("db down"))
	svc.settings.Store(&cachedConversationSettings{settings: ConversationRecordSettings{Enabled: true}, expiresAt: time.Now().Add(-time.Second)})
	require.True(t, svc.Enabled(context.Background()), "数据库暂时不可用时沿用上一次的开关状态")
}

func TestConversationRecordService_ReadFailureAtStartupMeansOff(t *testing.T) {
	settings := &convSettingRepoFake{getErr: errors.New("db down")}
	svc := newConvService(&convRecordRepoFake{}, settings)
	// 从未成功读取过配置时无法确认管理员已开启，按关闭处理。
	require.False(t, svc.Enabled(context.Background()))
}

func TestConversationRecordService_CorruptSettingsMeansOff(t *testing.T) {
	settings := &convSettingRepoFake{values: map[string]string{
		SettingKeyConversationRecordSettings: `{not json`,
	}}
	svc := newConvService(&convRecordRepoFake{}, settings)
	require.False(t, svc.Enabled(context.Background()))
}

func TestConversationRecordService_WritesQueuedRecordsOnStop(t *testing.T) {
	repo := &convRecordRepoFake{}
	svc := newConvService(repo, &convSettingRepoFake{})
	svc.Start()
	svc.Start() // 重复启动无害

	for _, id := range []string{"a", "b", "c"} {
		svc.Record(convRecord(id))
	}
	svc.Stop()

	require.Equal(t, 3, repo.storedCount())
	for _, rec := range repo.stored {
		require.False(t, rec.CreatedAt.IsZero(), "入队时补全创建时间")
	}
	require.Zero(t, svc.queuedBytes.Load(), "队列字节计数在排空后归零")
	require.Equal(t, uint64(3), svc.written.Load())
}

func TestConversationRecordService_FlushesOnBatchSize(t *testing.T) {
	repo := &convRecordRepoFake{}
	svc := newConvService(repo, &convSettingRepoFake{})
	svc.Start()
	defer svc.Stop()

	for i := 0; i < conversationBatchSize; i++ {
		svc.Record(convRecord(string(rune('a' + i%26))))
	}
	require.Eventually(t, func() bool { return repo.storedCount() == conversationBatchSize }, 3*time.Second, 10*time.Millisecond)
}

func TestConversationRecordService_RecordAfterStopIsIgnored(t *testing.T) {
	repo := &convRecordRepoFake{}
	svc := newConvService(repo, &convSettingRepoFake{})
	svc.Start()
	svc.Stop()

	svc.Record(convRecord("late"))
	require.Zero(t, repo.storedCount())
	require.Zero(t, svc.queuedBytes.Load())
}

func TestConversationRecordService_DropsWhenQueueIsFull(t *testing.T) {
	repo := &convRecordRepoFake{}
	svc := newConvService(repo, &convSettingRepoFake{}) // 不 Start：队列没人消费

	for i := 0; i < conversationQueueCapacity+10; i++ {
		svc.Record(convRecord("x"))
	}
	require.Equal(t, uint64(10), svc.dropped.Load())
	require.Len(t, svc.queue, conversationQueueCapacity)
}

func TestConversationRecordService_DropsWhenByteBudgetExceeded(t *testing.T) {
	svc := newConvService(&convRecordRepoFake{}, &convSettingRepoFake{})
	svc.queueMaxBytes = 4096

	big := convRecord("big")
	big.Response = strings.Repeat("x", 8192)
	svc.Record(big)
	require.Equal(t, uint64(1), svc.dropped.Load())
	require.Len(t, svc.queue, 0)
	require.Zero(t, svc.queuedBytes.Load(), "被丢弃的记录不能占用字节预算")

	svc.Record(convRecord("small"))
	require.Len(t, svc.queue, 1)
}

// 整批写入失败时逐条重试：一条坏记录不能连累同批的其他记录。
func TestConversationRecordService_BatchFailureFallsBackToPerRecord(t *testing.T) {
	repo := &convRecordRepoFake{badMarker: "BAD"}
	svc := newConvService(repo, &convSettingRepoFake{})

	bad := convRecord("bad")
	bad.Response = "BAD"
	svc.flush([]*ConversationRecord{convRecord("a"), bad, convRecord("c")})

	require.Equal(t, 2, repo.storedCount())
	require.Equal(t, uint64(2), svc.written.Load())
	require.Equal(t, uint64(1), svc.failed.Load())
	require.Len(t, repo.batches, 4, "1 次整批 + 3 次逐条")
}

func TestConversationRecordService_RetentionDeletesInBatches(t *testing.T) {
	repo := &convRecordRepoFake{deleteBeforeLeft: []int64{conversationRetentionBatchSize, conversationRetentionBatchSize, 5}}
	settings := &convSettingRepoFake{values: map[string]string{
		SettingKeyConversationRecordSettings: `{"enabled":false,"retention_days":7}`,
	}}
	svc := newConvService(repo, settings)

	before := time.Now().UTC().AddDate(0, 0, -7)
	svc.runRetentionOnce()
	after := time.Now().UTC().AddDate(0, 0, -7)

	require.Len(t, repo.deleteBeforeCalls, 3, "删满一批就继续，直到某一批不满")
	for _, cutoff := range repo.deleteBeforeCalls {
		require.False(t, cutoff.Before(before) || cutoff.After(after))
	}
}

func TestConversationRecordService_RetentionDisabledWhenZero(t *testing.T) {
	repo := &convRecordRepoFake{}
	settings := &convSettingRepoFake{values: map[string]string{
		SettingKeyConversationRecordSettings: `{"enabled":true,"retention_days":0}`,
	}}
	svc := newConvService(repo, settings)
	svc.runRetentionOnce()
	require.Empty(t, repo.deleteBeforeCalls)
}

func TestConversationRecordService_GetThread(t *testing.T) {
	repo := &convRecordRepoFake{}
	svc := newConvService(repo, &convSettingRepoFake{})

	_, err := svc.GetThread(context.Background(), "  ")
	require.ErrorIs(t, err, ErrConversationNotFound)
	_, err = svc.GetThread(context.Background(), "missing")
	require.ErrorIs(t, err, ErrConversationNotFound)

	repo.threadTotal = 1
	repo.threadRecords = []*ConversationRecord{convRecord("a")}
	thread, err := svc.GetThread(context.Background(), "key-a")
	require.NoError(t, err)
	require.Equal(t, 1, thread.Total)
}

func TestConversationRecordService_DeleteThreadsValidation(t *testing.T) {
	repo := &convRecordRepoFake{}
	svc := newConvService(repo, &convSettingRepoFake{})
	ctx := context.Background()

	_, err := svc.DeleteThreads(ctx, nil)
	require.Error(t, err)
	_, err = svc.DeleteThreads(ctx, []string{" ", ""})
	require.Error(t, err)

	tooMany := make([]string, conversationDeleteMaxKeys+1)
	for i := range tooMany {
		tooMany[i] = strings.Repeat("k", i+1)
	}
	_, err = svc.DeleteThreads(ctx, tooMany)
	require.Error(t, err)

	deleted, err := svc.DeleteThreads(ctx, []string{"a", " b ", "a", ""})
	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)
	require.Equal(t, [][]string{{"a", "b"}}, repo.deletedKeys, "去重并去掉空白")
}

func TestConversationRecordService_Clear(t *testing.T) {
	svc := newConvService(&convRecordRepoFake{}, &convSettingRepoFake{})
	deleted, err := svc.Clear(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), deleted)
}
