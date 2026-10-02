package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	// 写入队列同时受条数和总字节数限制：单条记录最大约 0.3MB，只限条数无法约束内存。
	conversationQueueCapacity = 2048
	conversationQueueMaxBytes = 64 << 20
	conversationBatchSize     = 50
	conversationFlushInterval = 2 * time.Second
	conversationFlushTimeout  = 15 * time.Second

	// 开关在网关热路径上读取，走进程内缓存；本进程保存配置后立即生效，其他节点最迟 TTL 后生效。
	conversationSettingsTTL      = 30 * time.Second
	conversationSettingsErrorTTL = 5 * time.Second
	conversationSettingsTimeout  = 2 * time.Second

	conversationRetentionStartupDelay = 2 * time.Minute
	conversationRetentionInterval     = time.Hour
	conversationRetentionBatchSize    = 2000
	conversationRetentionMaxBatches   = 500

	conversationRetentionDaysMax = 3650

	// conversationThreadLimit 单场对话详情最多返回的轮数。
	conversationThreadLimit = 500
	// conversationDeleteMaxKeys 一次批量删除最多允许的对话数。
	conversationDeleteMaxKeys = 200

	conversationDropLogInterval = 30 * time.Second
)

type cachedConversationSettings struct {
	settings  ConversationRecordSettings
	expiresAt time.Time
}

// ConversationRecordService 对话记录服务。
//
// 写入端：网关请求结束后非阻塞入队，由后台协程批量落库，数据库变慢或不可用
// 只会丢弃记录，不会拖慢或阻塞网关转发。
// 读取端：为管理后台提供列表、详情、删除；并按保留天数定期清理过期记录。
type ConversationRecordService struct {
	repo        ConversationRecordRepository
	settingRepo SettingRepository

	queue         chan *ConversationRecord
	queuedBytes   atomic.Int64
	queueMaxBytes int64

	settings  atomic.Pointer[cachedConversationSettings]
	refreshMu sync.Mutex

	startOnce sync.Once
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup

	dropped     atomic.Uint64
	written     atomic.Uint64
	failed      atomic.Uint64
	lastDropLog atomic.Int64
}

// NewConversationRecordService 创建对话记录服务。后台协程在 Start 时启动。
func NewConversationRecordService(repo ConversationRecordRepository, settingRepo SettingRepository) *ConversationRecordService {
	ctx, cancel := context.WithCancel(context.Background())
	return &ConversationRecordService{
		repo:          repo,
		settingRepo:   settingRepo,
		queue:         make(chan *ConversationRecord, conversationQueueCapacity),
		queueMaxBytes: conversationQueueMaxBytes,
		ctx:           ctx,
		cancel:        cancel,
	}
}

// Start 启动异步写入与保留期清理协程，可重复调用。
func (s *ConversationRecordService) Start() {
	if s == nil || s.repo == nil {
		return
	}
	s.startOnce.Do(func() {
		s.wg.Add(2)
		go s.runWriter()
		go s.runRetention()
	})
}

// Stop 停止服务并尽量落盘队列中剩余的记录。
func (s *ConversationRecordService) Stop() {
	if s == nil {
		return
	}
	s.cancel()
	s.wg.Wait()
}

// ---- 配置 ----

func (s *ConversationRecordService) loadSettings(ctx context.Context) (ConversationRecordSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyConversationRecordSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return ConversationRecordSettings{}, nil
		}
		return ConversationRecordSettings{}, err
	}
	if strings.TrimSpace(value) == "" {
		return ConversationRecordSettings{}, nil
	}
	var settings ConversationRecordSettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		// 配置损坏时按关闭处理，避免在管理员并未开启的情况下保存对话。
		logger.L().Warn("conversation_record.settings_invalid", zap.Error(err))
		return ConversationRecordSettings{}, nil
	}
	return normalizeConversationSettings(settings), nil
}

func normalizeConversationSettings(in ConversationRecordSettings) ConversationRecordSettings {
	if in.RetentionDays < 0 {
		in.RetentionDays = 0
	}
	if in.RetentionDays > conversationRetentionDaysMax {
		in.RetentionDays = conversationRetentionDaysMax
	}
	return in
}

// GetSettings 直接读取持久化的配置，供管理端读写路径使用。
func (s *ConversationRecordService) GetSettings(ctx context.Context) (ConversationRecordSettings, error) {
	return s.loadSettings(ctx)
}

// UpdateSettings 校验并保存配置，同时刷新本进程缓存。
func (s *ConversationRecordService) UpdateSettings(ctx context.Context, in ConversationRecordSettings) (ConversationRecordSettings, error) {
	if in.RetentionDays < 0 || in.RetentionDays > conversationRetentionDaysMax {
		return ConversationRecordSettings{}, infraerrors.BadRequest("INVALID_RETENTION_DAYS", "retention_days must be between 0 and 3650")
	}
	data, err := json.Marshal(in)
	if err != nil {
		return ConversationRecordSettings{}, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyConversationRecordSettings, string(data)); err != nil {
		return ConversationRecordSettings{}, err
	}
	s.settings.Store(&cachedConversationSettings{settings: in, expiresAt: time.Now().Add(conversationSettingsTTL)})
	return in, nil
}

// cachedSettings 返回进程内缓存的配置。缓存过期时只让一个请求去刷新，其余沿用旧值，
// 避免热路径上的并发请求同时打到数据库。读取失败沿用上一次的值，从未成功读取过则按关闭处理。
func (s *ConversationRecordService) cachedSettings(ctx context.Context) ConversationRecordSettings {
	now := time.Now()
	if c := s.settings.Load(); c != nil && now.Before(c.expiresAt) {
		return c.settings
	}
	if !s.refreshMu.TryLock() {
		if c := s.settings.Load(); c != nil {
			return c.settings
		}
		return ConversationRecordSettings{}
	}
	defer s.refreshMu.Unlock()
	if c := s.settings.Load(); c != nil && time.Now().Before(c.expiresAt) {
		return c.settings
	}

	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), conversationSettingsTimeout)
	defer cancel()
	settings, err := s.loadSettings(loadCtx)
	if err != nil {
		var previous ConversationRecordSettings
		if c := s.settings.Load(); c != nil {
			previous = c.settings
		}
		logger.L().Warn("conversation_record.settings_load_failed", zap.Error(err))
		s.settings.Store(&cachedConversationSettings{settings: previous, expiresAt: time.Now().Add(conversationSettingsErrorTTL)})
		return previous
	}
	s.settings.Store(&cachedConversationSettings{settings: settings, expiresAt: time.Now().Add(conversationSettingsTTL)})
	return settings
}

// Enabled 报告当前是否应当保存对话记录。网关每个请求都会调用，不会访问数据库（缓存过期的那一次除外）。
func (s *ConversationRecordService) Enabled(ctx context.Context) bool {
	if s == nil || s.repo == nil || s.settingRepo == nil {
		return false
	}
	return s.cachedSettings(ctx).Enabled
}

// ---- 写入 ----

// Record 非阻塞入队一条记录；队列已满或超出字节预算时丢弃并计数。
func (s *ConversationRecordService) Record(rec *ConversationRecord) {
	if s == nil || rec == nil {
		return
	}
	select {
	case <-s.ctx.Done():
		return
	default:
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	size := rec.approxSize()
	if s.queuedBytes.Add(size) > s.queueMaxBytes {
		s.queuedBytes.Add(-size)
		s.countDropped()
		return
	}
	select {
	case s.queue <- rec:
	default:
		s.queuedBytes.Add(-size)
		s.countDropped()
	}
}

func (s *ConversationRecordService) countDropped() {
	total := s.dropped.Add(1)
	now := time.Now().UnixNano()
	last := s.lastDropLog.Load()
	if now-last < int64(conversationDropLogInterval) || !s.lastDropLog.CompareAndSwap(last, now) {
		return
	}
	logger.L().Warn("conversation_record.queue_full_dropped", zap.Uint64("dropped_total", total))
}

func (s *ConversationRecordService) runWriter() {
	defer s.wg.Done()

	ticker := time.NewTicker(conversationFlushInterval)
	defer ticker.Stop()

	batch := make([]*ConversationRecord, 0, conversationBatchSize)
	flush := func() {
		s.flush(batch)
		batch = batch[:0]
	}
	take := func(rec *ConversationRecord) {
		if rec == nil {
			return
		}
		s.queuedBytes.Add(-rec.approxSize())
		batch = append(batch, rec)
		if len(batch) >= conversationBatchSize {
			flush()
		}
	}

	for {
		select {
		case <-s.ctx.Done():
			// 停机前排空队列。
			for {
				select {
				case rec := <-s.queue:
					take(rec)
				default:
					flush()
					return
				}
			}
		case rec := <-s.queue:
			take(rec)
		case <-ticker.C:
			flush()
		}
	}
}

// flush 写入一批记录。整批失败时（多半是其中某一条内容数据库无法接受）逐条重试，
// 避免一条坏记录连累同批其他记录。
func (s *ConversationRecordService) flush(batch []*ConversationRecord) {
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), conversationFlushTimeout)
	defer cancel()

	_, err := s.repo.BatchInsert(ctx, batch)
	if err == nil {
		s.written.Add(uint64(len(batch)))
		return
	}
	logger.L().Warn("conversation_record.batch_insert_failed", zap.Error(err), zap.Int("batch", len(batch)))
	for _, rec := range batch {
		if _, err := s.repo.BatchInsert(ctx, []*ConversationRecord{rec}); err != nil {
			s.failed.Add(1)
			logger.L().Warn("conversation_record.insert_failed", zap.Error(err), zap.String("request_id", rec.RequestID))
			continue
		}
		s.written.Add(1)
	}
}

// ---- 保留期清理 ----

func (s *ConversationRecordService) runRetention() {
	defer s.wg.Done()

	startup := time.NewTimer(conversationRetentionStartupDelay)
	defer startup.Stop()
	select {
	case <-s.ctx.Done():
		return
	case <-startup.C:
	}

	ticker := time.NewTicker(conversationRetentionInterval)
	defer ticker.Stop()
	s.runRetentionOnce()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.runRetentionOnce()
		}
	}
}

// runRetentionOnce 删除超过保留天数的记录。删除是幂等的，多节点同时执行无害，因此无需选主。
// 即使对话记录已被关闭也照常清理，已保存的内容仍应遵守保留期。
func (s *ConversationRecordService) runRetentionOnce() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Minute)
	defer cancel()

	settings, err := s.loadSettings(ctx)
	if err != nil {
		logger.L().Warn("conversation_record.retention_settings_failed", zap.Error(err))
		return
	}
	if settings.RetentionDays <= 0 {
		return
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -settings.RetentionDays)
	var total int64
	for i := 0; i < conversationRetentionMaxBatches; i++ {
		deleted, err := s.repo.DeleteBefore(ctx, cutoff, conversationRetentionBatchSize)
		if err != nil {
			logger.L().Warn("conversation_record.retention_failed", zap.Error(err))
			break
		}
		total += deleted
		if deleted < conversationRetentionBatchSize {
			break
		}
	}
	if total > 0 {
		logger.L().Info("conversation_record.retention_deleted", zap.Int64("deleted", total), zap.Int("retention_days", settings.RetentionDays))
	}
}

// ---- 管理端读写 ----

// List 分页查询对话列表。
func (s *ConversationRecordService) List(ctx context.Context, filter *ConversationFilter) (*ConversationList, error) {
	if filter == nil {
		filter = &ConversationFilter{}
	}
	return s.repo.ListConversations(ctx, filter)
}

// GetThread 返回一场对话最近的若干轮（按时间正序）。
func (s *ConversationRecordService) GetThread(ctx context.Context, key string) (*ConversationThread, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, ErrConversationNotFound
	}
	thread, err := s.repo.GetThread(ctx, key, conversationThreadLimit)
	if err != nil {
		return nil, err
	}
	if thread == nil || len(thread.Records) == 0 {
		return nil, ErrConversationNotFound
	}
	return thread, nil
}

// DeleteThreads 删除若干场对话，返回删除的记录条数。
func (s *ConversationRecordService) DeleteThreads(ctx context.Context, keys []string) (int64, error) {
	cleaned := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		cleaned = append(cleaned, key)
	}
	if len(cleaned) == 0 {
		return 0, infraerrors.BadRequest("CONVERSATION_KEYS_REQUIRED", "at least one conversation key is required")
	}
	if len(cleaned) > conversationDeleteMaxKeys {
		return 0, infraerrors.BadRequest("TOO_MANY_CONVERSATIONS", "too many conversations in one request")
	}
	return s.repo.DeleteThreads(ctx, cleaned)
}

// Clear 清空全部对话记录。
func (s *ConversationRecordService) Clear(ctx context.Context) (int64, error) {
	return s.repo.DeleteAll(ctx)
}
