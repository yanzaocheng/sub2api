//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/websearch"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- merged from gateway_account_selection_test.go ----
// --- helpers ---

func testTimePtr(t time.Time) *time.Time { return &t }

func makeAccWithLoad(id int64, priority int, loadRate int, lastUsed *time.Time, accType string) accountWithLoad {
	return accountWithLoad{
		account: &Account{
			ID:          id,
			Priority:    priority,
			LastUsedAt:  lastUsed,
			Type:        accType,
			Schedulable: true,
			Status:      StatusActive,
		},
		loadInfo: &AccountLoadInfo{
			AccountID:          id,
			CurrentConcurrency: 0,
			LoadRate:           loadRate,
		},
	}
}

// --- sortAccountsByPriorityAndLastUsed ---

func TestSortAccountsByPriorityAndLastUsed_ByPriority(t *testing.T) {
	now := time.Now()
	accounts := []*Account{
		{ID: 1, Priority: 5, LastUsedAt: testTimePtr(now)},
		{ID: 2, Priority: 1, LastUsedAt: testTimePtr(now)},
		{ID: 3, Priority: 3, LastUsedAt: testTimePtr(now)},
	}
	sortAccountsByPriorityAndLastUsed(accounts, false)
	require.Equal(t, int64(2), accounts[0].ID, "优先级最低的排第一")
	require.Equal(t, int64(3), accounts[1].ID)
	require.Equal(t, int64(1), accounts[2].ID)
}

func TestSortAccountsByPriorityAndLastUsed_SamePriorityByLastUsed(t *testing.T) {
	now := time.Now()
	accounts := []*Account{
		{ID: 1, Priority: 1, LastUsedAt: testTimePtr(now)},
		{ID: 2, Priority: 1, LastUsedAt: testTimePtr(now.Add(-1 * time.Hour))},
		{ID: 3, Priority: 1, LastUsedAt: nil},
	}
	sortAccountsByPriorityAndLastUsed(accounts, false)
	require.Equal(t, int64(3), accounts[0].ID, "nil LastUsedAt 排最前")
	require.Equal(t, int64(2), accounts[1].ID, "更早使用的排前面")
	require.Equal(t, int64(1), accounts[2].ID)
}

func TestSortAccountsByPriorityAndLastUsed_PreferOAuth(t *testing.T) {
	accounts := []*Account{
		{ID: 1, Priority: 1, LastUsedAt: nil, Type: AccountTypeAPIKey},
		{ID: 2, Priority: 1, LastUsedAt: nil, Type: AccountTypeOAuth},
	}
	sortAccountsByPriorityAndLastUsed(accounts, true)
	require.Equal(t, int64(2), accounts[0].ID, "preferOAuth 时 OAuth 账号排前面")
}

func TestSortAccountsByPriorityAndLastUsed_StableSort(t *testing.T) {
	accounts := []*Account{
		{ID: 1, Priority: 1, LastUsedAt: nil, Type: AccountTypeAPIKey},
		{ID: 2, Priority: 1, LastUsedAt: nil, Type: AccountTypeAPIKey},
		{ID: 3, Priority: 1, LastUsedAt: nil, Type: AccountTypeAPIKey},
	}

	// sortAccountsByPriorityAndLastUsed 内部会在同组(Priority+LastUsedAt)内做随机打散，
	// 因此这里不再断言“稳定排序”。我们只验证：
	// 1) 元素集合不变；2) 多次运行能产生不同的顺序。
	seenFirst := map[int64]bool{}
	for i := 0; i < 100; i++ {
		cpy := make([]*Account, len(accounts))
		copy(cpy, accounts)
		sortAccountsByPriorityAndLastUsed(cpy, false)
		seenFirst[cpy[0].ID] = true

		ids := map[int64]bool{}
		for _, a := range cpy {
			ids[a.ID] = true
		}
		require.True(t, ids[1] && ids[2] && ids[3])
	}
	require.GreaterOrEqual(t, len(seenFirst), 2, "同组账号应能被随机打散")
}

func TestSortAccountsByPriorityAndLastUsed_MixedPriorityAndTime(t *testing.T) {
	now := time.Now()
	accounts := []*Account{
		{ID: 1, Priority: 2, LastUsedAt: nil},
		{ID: 2, Priority: 1, LastUsedAt: testTimePtr(now)},
		{ID: 3, Priority: 1, LastUsedAt: testTimePtr(now.Add(-1 * time.Hour))},
		{ID: 4, Priority: 2, LastUsedAt: testTimePtr(now.Add(-2 * time.Hour))},
	}
	sortAccountsByPriorityAndLastUsed(accounts, false)
	// 优先级1排前：nil < earlier
	require.Equal(t, int64(3), accounts[0].ID, "优先级1 + 更早")
	require.Equal(t, int64(2), accounts[1].ID, "优先级1 + 现在")
	// 优先级2排后：nil < time
	require.Equal(t, int64(1), accounts[2].ID, "优先级2 + nil")
	require.Equal(t, int64(4), accounts[3].ID, "优先级2 + 有时间")
}

// --- filterByMinPriority ---

func TestFilterByMinPriority_Empty(t *testing.T) {
	result := filterByMinPriority(nil)
	require.Nil(t, result)
}

func TestFilterByMinPriority_SelectsMinPriority(t *testing.T) {
	accounts := []accountWithLoad{
		makeAccWithLoad(1, 5, 10, nil, AccountTypeAPIKey),
		makeAccWithLoad(2, 1, 10, nil, AccountTypeAPIKey),
		makeAccWithLoad(3, 1, 20, nil, AccountTypeAPIKey),
		makeAccWithLoad(4, 2, 10, nil, AccountTypeAPIKey),
	}
	result := filterByMinPriority(accounts)
	require.Len(t, result, 2)
	require.Equal(t, int64(2), result[0].account.ID)
	require.Equal(t, int64(3), result[1].account.ID)
}

// --- filterByMinLoadRate ---

func TestFilterByMinLoadRate_Empty(t *testing.T) {
	result := filterByMinLoadRate(nil)
	require.Nil(t, result)
}

func TestFilterByMinLoadRate_SelectsMinLoadRate(t *testing.T) {
	accounts := []accountWithLoad{
		makeAccWithLoad(1, 1, 30, nil, AccountTypeAPIKey),
		makeAccWithLoad(2, 1, 10, nil, AccountTypeAPIKey),
		makeAccWithLoad(3, 1, 10, nil, AccountTypeAPIKey),
		makeAccWithLoad(4, 1, 20, nil, AccountTypeAPIKey),
	}
	result := filterByMinLoadRate(accounts)
	require.Len(t, result, 2)
	require.Equal(t, int64(2), result[0].account.ID)
	require.Equal(t, int64(3), result[1].account.ID)
}

// --- selectByLRU ---

func TestSelectByLRU_Empty(t *testing.T) {
	result := selectByLRU(nil, false)
	require.Nil(t, result)
}

func TestSelectByLRU_Single(t *testing.T) {
	accounts := []accountWithLoad{makeAccWithLoad(1, 1, 10, nil, AccountTypeAPIKey)}
	result := selectByLRU(accounts, false)
	require.NotNil(t, result)
	require.Equal(t, int64(1), result.account.ID)
}

func TestSelectByLRU_NilLastUsedAtWins(t *testing.T) {
	now := time.Now()
	accounts := []accountWithLoad{
		makeAccWithLoad(1, 1, 10, testTimePtr(now), AccountTypeAPIKey),
		makeAccWithLoad(2, 1, 10, nil, AccountTypeAPIKey),
		makeAccWithLoad(3, 1, 10, testTimePtr(now.Add(-1*time.Hour)), AccountTypeAPIKey),
	}
	result := selectByLRU(accounts, false)
	require.NotNil(t, result)
	require.Equal(t, int64(2), result.account.ID)
}

func TestSelectByLRU_EarliestTimeWins(t *testing.T) {
	now := time.Now()
	accounts := []accountWithLoad{
		makeAccWithLoad(1, 1, 10, testTimePtr(now), AccountTypeAPIKey),
		makeAccWithLoad(2, 1, 10, testTimePtr(now.Add(-1*time.Hour)), AccountTypeAPIKey),
		makeAccWithLoad(3, 1, 10, testTimePtr(now.Add(-2*time.Hour)), AccountTypeAPIKey),
	}
	result := selectByLRU(accounts, false)
	require.NotNil(t, result)
	require.Equal(t, int64(3), result.account.ID)
}

func TestSelectByLRU_TiePreferOAuth(t *testing.T) {
	now := time.Now()
	// 账号 1/2 LastUsedAt 相同，且同为最小值。
	accounts := []accountWithLoad{
		makeAccWithLoad(1, 1, 10, testTimePtr(now), AccountTypeAPIKey),
		makeAccWithLoad(2, 1, 10, testTimePtr(now), AccountTypeOAuth),
		makeAccWithLoad(3, 1, 10, testTimePtr(now.Add(1*time.Hour)), AccountTypeAPIKey),
	}
	for i := 0; i < 50; i++ {
		result := selectByLRU(accounts, true)
		require.NotNil(t, result)
		require.Equal(t, AccountTypeOAuth, result.account.Type)
		require.Equal(t, int64(2), result.account.ID)
	}
}

// ---- merged from gateway_channel_restriction_fallback_test.go ----
func TestSelectAccountForModelWithExclusions_UsesFallbackGroupForChannelRestriction(t *testing.T) {
	t.Parallel()

	groupID := int64(10)
	fallbackID := int64(11)
	ch := Channel{
		ID:             1,
		Status:         StatusActive,
		GroupIDs:       []int64{fallbackID},
		RestrictModels: true,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformAnthropic, Models: []string{"claude-sonnet-4-6"}},
		},
	}
	channelSvc := newTestChannelService(makeStandardRepo(ch, map[int64]string{
		fallbackID: PlatformAnthropic,
	}))
	accountRepo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range accountRepo.accounts {
		accountRepo.accountsByID[accountRepo.accounts[i].ID] = &accountRepo.accounts[i]
	}
	groupRepo := &mockGroupRepoForGateway{
		groups: map[int64]*Group{
			groupID: {
				ID:              groupID,
				Platform:        PlatformAnthropic,
				Status:          StatusActive,
				ClaudeCodeOnly:  true,
				FallbackGroupID: &fallbackID,
				Hydrated:        true,
			},
			fallbackID: {
				ID:       fallbackID,
				Platform: PlatformAnthropic,
				Status:   StatusActive,
				Hydrated: true,
			},
		},
	}

	svc := &GatewayService{
		accountRepo:    accountRepo,
		groupRepo:      groupRepo,
		channelService: channelSvc,
		cfg:            testConfig(),
	}

	ctx := context.WithValue(context.Background(), ctxkey.Group, groupRepo.groups[groupID])
	account, err := svc.SelectAccountForModelWithExclusions(ctx, &groupID, "", "claude-sonnet-4-6", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(1), account.ID)
}

func TestSelectAccountWithLoadAwareness_UsesFallbackGroupForChannelRestriction(t *testing.T) {
	t.Parallel()

	groupID := int64(10)
	fallbackID := int64(11)
	ch := Channel{
		ID:             1,
		Status:         StatusActive,
		GroupIDs:       []int64{fallbackID},
		RestrictModels: true,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformAnthropic, Models: []string{"claude-sonnet-4-6"}},
		},
	}
	channelSvc := newTestChannelService(makeStandardRepo(ch, map[int64]string{
		fallbackID: PlatformAnthropic,
	}))
	accountRepo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range accountRepo.accounts {
		accountRepo.accountsByID[accountRepo.accounts[i].ID] = &accountRepo.accounts[i]
	}
	groupRepo := &mockGroupRepoForGateway{
		groups: map[int64]*Group{
			groupID: {
				ID:              groupID,
				Platform:        PlatformAnthropic,
				Status:          StatusActive,
				ClaudeCodeOnly:  true,
				FallbackGroupID: &fallbackID,
				Hydrated:        true,
			},
			fallbackID: {
				ID:       fallbackID,
				Platform: PlatformAnthropic,
				Status:   StatusActive,
				Hydrated: true,
			},
		},
	}

	svc := &GatewayService{
		accountRepo:    accountRepo,
		groupRepo:      groupRepo,
		channelService: channelSvc,
		cfg:            testConfig(),
	}

	ctx := context.WithValue(context.Background(), ctxkey.Group, groupRepo.groups[groupID])
	result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "", "claude-sonnet-4-6", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	require.Equal(t, int64(1), result.Account.ID)
}

// ---- merged from gateway_channel_restriction_load_aware_test.go ----
// loadAwareRestrictionFixture 构造走负载感知路径（load_batch 开启 + 并发服务存在）的
// GatewayService：分组 10 绑定 upstream 计费基准的渠道，定价列表只允许 claude-sonnet-4-6。
// 账号 1 把 claude-fable-5-1 映射为自身（不在定价列表），账号 2 映射为 claude-sonnet-4-6（在列表）。
type loadAwareRestrictionFixture struct {
	svc              *GatewayService
	ctx              context.Context
	groupID          int64
	cache            *mockGatewayCacheForPlatform
	concurrencyCache *mockConcurrencyCache
}

func newLoadAwareRestrictionFixture(t *testing.T, restrict bool, sessionBindings map[string]int64, routing map[string][]int64) *loadAwareRestrictionFixture {
	t.Helper()

	groupID := int64(10)
	ch := Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{groupID},
		RestrictModels:     restrict,
		BillingModelSource: BillingModelSourceUpstream,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformAnthropic, Models: []string{"claude-sonnet-4-6"}},
		},
	}
	channelSvc := newTestChannelService(makeStandardRepo(ch, map[int64]string{groupID: PlatformAnthropic}))

	accountRepo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID: 1, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"claude-fable-5-1": "claude-fable-5-1"},
				},
			},
			{
				ID: 2, Platform: PlatformAnthropic, Priority: 2, Status: StatusActive, Schedulable: true, Concurrency: 5,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"claude-fable-5-1": "claude-sonnet-4-6"},
				},
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range accountRepo.accounts {
		accountRepo.accountsByID[accountRepo.accounts[i].ID] = &accountRepo.accounts[i]
	}

	group := &Group{
		ID:                  groupID,
		Platform:            PlatformAnthropic,
		Status:              StatusActive,
		Hydrated:            true,
		ModelRoutingEnabled: len(routing) > 0,
		ModelRouting:        routing,
	}
	groupRepo := &mockGroupRepoForGateway{groups: map[int64]*Group{groupID: group}}

	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cache := &mockGatewayCacheForPlatform{sessionBindings: sessionBindings}
	concurrencyCache := &mockConcurrencyCache{}

	svc := &GatewayService{
		accountRepo:        accountRepo,
		groupRepo:          groupRepo,
		channelService:     channelSvc,
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	return &loadAwareRestrictionFixture{
		svc:              svc,
		ctx:              context.WithValue(context.Background(), ctxkey.Group, group),
		groupID:          groupID,
		cache:            cache,
		concurrencyCache: concurrencyCache,
	}
}

func TestSelectAccountWithLoadAwareness_UpstreamRestrictionSkipsDisallowedAccount(t *testing.T) {
	t.Parallel()

	f := newLoadAwareRestrictionFixture(t, true, nil, nil)
	result, err := f.svc.SelectAccountWithLoadAwareness(f.ctx, &f.groupID, "", "claude-fable-5-1", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	require.Equal(t, int64(2), result.Account.ID, "上游模型不在渠道定价列表的高优先级账号必须被跳过")
	require.Equal(t, 1, f.concurrencyCache.loadBatchCalls, "应经过 Layer 2 负载感知选择")
}

func TestSelectAccountWithLoadAwareness_UpstreamRestrictionRejectsWhenAllAccountsDisallowed(t *testing.T) {
	t.Parallel()

	f := newLoadAwareRestrictionFixture(t, true, nil, nil)
	// 账号 2 也改为映射到不在定价列表的模型
	f.svc.accountRepo.(*mockAccountRepoForPlatform).accounts[1].Credentials = map[string]any{
		"model_mapping": map[string]any{"claude-fable-5-1": "claude-fable-5-1"},
	}

	result, err := f.svc.SelectAccountWithLoadAwareness(f.ctx, &f.groupID, "", "claude-fable-5-1", nil, "", 0)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.ErrorContains(t, err, "channel pricing restriction")
	require.Nil(t, result)
	require.Equal(t, 0, f.concurrencyCache.acquireAccountCalls, "没有合规候选时不应尝试占用任何账号槽位")
}

func TestSelectAccountWithLoadAwareness_UpstreamRestrictionIgnoresStickyAccount(t *testing.T) {
	t.Parallel()

	f := newLoadAwareRestrictionFixture(t, true, map[string]int64{"sticky": 1}, nil)
	result, err := f.svc.SelectAccountWithLoadAwareness(f.ctx, &f.groupID, "sticky", "claude-fable-5-1", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	require.Equal(t, int64(2), result.Account.ID, "粘性账号的上游模型不在定价列表时不得沿用粘性会话")
	require.Equal(t, int64(2), f.cache.sessionBindings["sticky"], "粘性会话应重新绑定到合规账号")
}

func TestSelectAccountWithLoadAwareness_UpstreamRestrictionFiltersRoutedAccounts(t *testing.T) {
	t.Parallel()

	f := newLoadAwareRestrictionFixture(t, true, nil, map[string][]int64{"claude-fable-5-1": {1}})
	result, err := f.svc.SelectAccountWithLoadAwareness(f.ctx, &f.groupID, "", "claude-fable-5-1", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	require.Equal(t, int64(2), result.Account.ID, "模型路由指向的账号被渠道限制时应回退到合规账号")
}

func TestSelectAccountWithLoadAwareness_UpstreamRestrictionRoutedStickyAccountNotHonored(t *testing.T) {
	t.Parallel()

	f := newLoadAwareRestrictionFixture(t, true, map[string]int64{"sticky": 1}, map[string][]int64{"claude-fable-5-1": {1, 2}})
	result, err := f.svc.SelectAccountWithLoadAwareness(f.ctx, &f.groupID, "sticky", "claude-fable-5-1", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	require.Equal(t, int64(2), result.Account.ID, "路由列表内的粘性账号被渠道限制时不得沿用")
}

func TestSelectAccountWithLoadAwareness_RestrictModelsDisabledKeepsPriorityOrder(t *testing.T) {
	t.Parallel()

	f := newLoadAwareRestrictionFixture(t, false, nil, nil)
	result, err := f.svc.SelectAccountWithLoadAwareness(f.ctx, &f.groupID, "", "claude-fable-5-1", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	require.Equal(t, int64(1), result.Account.ID, "未开启限制时不应因定价列表过滤账号")
}

// ---- merged from gateway_channel_restriction_test.go ----
// --- billingModelForRestriction ---

func TestBillingModelForRestriction_Requested(t *testing.T) {
	t.Parallel()
	got := billingModelForRestriction(BillingModelSourceRequested, "claude-sonnet-4-5", "claude-sonnet-4-6")
	require.Equal(t, "claude-sonnet-4-5", got)
}

func TestBillingModelForRestriction_ChannelMapped(t *testing.T) {
	t.Parallel()
	got := billingModelForRestriction(BillingModelSourceChannelMapped, "claude-sonnet-4-5", "claude-sonnet-4-6")
	require.Equal(t, "claude-sonnet-4-6", got)
}

func TestBillingModelForRestriction_Upstream(t *testing.T) {
	t.Parallel()
	got := billingModelForRestriction(BillingModelSourceUpstream, "claude-sonnet-4-5", "claude-sonnet-4-6")
	require.Equal(t, "", got, "upstream should return empty (per-account check needed)")
}

func TestBillingModelForRestriction_ResponseModelUsesMappedPrecheck(t *testing.T) {
	t.Parallel()
	got := billingModelForRestriction(BillingModelSourceResponse, "claude-fable-5", "claude-fable-5")
	require.Equal(t, "claude-fable-5", got)
}

func TestBillingModelForRestriction_Empty(t *testing.T) {
	t.Parallel()
	got := billingModelForRestriction("", "claude-sonnet-4-5", "claude-sonnet-4-6")
	require.Equal(t, "claude-sonnet-4-6", got, "empty source defaults to channel_mapped")
}

// --- resolveAccountUpstreamModel ---

func TestResolveAccountUpstreamModel_Antigravity(t *testing.T) {
	t.Parallel()
	account := &Account{
		Platform: PlatformAntigravity,
	}
	// Antigravity 平台使用 DefaultAntigravityModelMapping
	got := resolveAccountUpstreamModel(account, "claude-sonnet-4-6")
	require.Equal(t, "claude-sonnet-4-6", got)
}

func TestResolveAccountUpstreamModel_Antigravity_Unsupported(t *testing.T) {
	t.Parallel()
	account := &Account{
		Platform: PlatformAntigravity,
	}
	got := resolveAccountUpstreamModel(account, "totally-unknown-model")
	require.Equal(t, "", got, "unsupported model should return empty")
}

func TestResolveAccountUpstreamModel_NonAntigravity(t *testing.T) {
	t.Parallel()
	account := &Account{
		Platform: PlatformAnthropic,
	}
	got := resolveAccountUpstreamModel(account, "claude-sonnet-4-6")
	require.Equal(t, "claude-sonnet-4-6", got, "no mapping = passthrough")
}

// --- checkChannelPricingRestriction ---

func TestCheckChannelPricingRestriction_NilGroupID(t *testing.T) {
	t.Parallel()
	svc := &GatewayService{channelService: &ChannelService{}}
	require.False(t, svc.checkChannelPricingRestriction(context.Background(), nil, "claude-sonnet-4"))
}

func TestCheckChannelPricingRestriction_NilChannelService(t *testing.T) {
	t.Parallel()
	svc := &GatewayService{}
	gid := int64(10)
	require.False(t, svc.checkChannelPricingRestriction(context.Background(), &gid, "claude-sonnet-4"))
}

func TestCheckChannelPricingRestriction_EmptyModel(t *testing.T) {
	t.Parallel()
	svc := &GatewayService{channelService: &ChannelService{}}
	gid := int64(10)
	require.False(t, svc.checkChannelPricingRestriction(context.Background(), &gid, ""))
}

func TestCheckChannelPricingRestriction_ChannelMapped_Restricted(t *testing.T) {
	t.Parallel()
	// 渠道映射 claude-sonnet-4-5 → claude-sonnet-4-6，但定价列表只有 claude-opus-4-6
	ch := Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceChannelMapped,
		ModelPricing: []ChannelModelPricing{
			{Platform: "anthropic", Models: []string{"claude-opus-4-6"}},
		},
		ModelMapping: map[string]map[string]string{
			"anthropic": {"claude-sonnet-4-5": "claude-sonnet-4-6"},
		},
	}
	channelSvc := newTestChannelService(makeStandardRepo(ch, map[int64]string{10: "anthropic"}))
	svc := &GatewayService{channelService: channelSvc}

	gid := int64(10)
	require.True(t, svc.checkChannelPricingRestriction(context.Background(), &gid, "claude-sonnet-4-5"),
		"mapped model claude-sonnet-4-6 is NOT in pricing → restricted")
}

func TestCheckChannelPricingRestriction_ChannelMapped_Allowed(t *testing.T) {
	t.Parallel()
	// 渠道映射 claude-sonnet-4-5 → claude-sonnet-4-6，定价列表包含 claude-sonnet-4-6
	ch := Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceChannelMapped,
		ModelPricing: []ChannelModelPricing{
			{Platform: "anthropic", Models: []string{"claude-sonnet-4-6"}},
		},
		ModelMapping: map[string]map[string]string{
			"anthropic": {"claude-sonnet-4-5": "claude-sonnet-4-6"},
		},
	}
	channelSvc := newTestChannelService(makeStandardRepo(ch, map[int64]string{10: "anthropic"}))
	svc := &GatewayService{channelService: channelSvc}

	gid := int64(10)
	require.False(t, svc.checkChannelPricingRestriction(context.Background(), &gid, "claude-sonnet-4-5"),
		"mapped model claude-sonnet-4-6 IS in pricing → allowed")
}

func TestCheckChannelPricingRestriction_Requested_Restricted(t *testing.T) {
	t.Parallel()
	// billing_model_source=requested，定价列表有 claude-sonnet-4-6 但请求的是 claude-sonnet-4-5
	ch := Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceRequested,
		ModelPricing: []ChannelModelPricing{
			{Platform: "anthropic", Models: []string{"claude-sonnet-4-6"}},
		},
	}
	channelSvc := newTestChannelService(makeStandardRepo(ch, map[int64]string{10: "anthropic"}))
	svc := &GatewayService{channelService: channelSvc}

	gid := int64(10)
	require.True(t, svc.checkChannelPricingRestriction(context.Background(), &gid, "claude-sonnet-4-5"),
		"requested model claude-sonnet-4-5 is NOT in pricing → restricted")
}

func TestCheckChannelPricingRestriction_Requested_Allowed(t *testing.T) {
	t.Parallel()
	ch := Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceRequested,
		ModelPricing: []ChannelModelPricing{
			{Platform: "anthropic", Models: []string{"claude-sonnet-4-5"}},
		},
	}
	channelSvc := newTestChannelService(makeStandardRepo(ch, map[int64]string{10: "anthropic"}))
	svc := &GatewayService{channelService: channelSvc}

	gid := int64(10)
	require.False(t, svc.checkChannelPricingRestriction(context.Background(), &gid, "claude-sonnet-4-5"),
		"requested model IS in pricing → allowed")
}

func TestCheckChannelPricingRestriction_Upstream_SkipsPreCheck(t *testing.T) {
	t.Parallel()
	// upstream 模式：预检查始终跳过（返回 false），需逐账号检查
	ch := Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceUpstream,
		ModelPricing: []ChannelModelPricing{
			{Platform: "anthropic", Models: []string{"claude-opus-4-6"}},
		},
	}
	channelSvc := newTestChannelService(makeStandardRepo(ch, map[int64]string{10: "anthropic"}))
	svc := &GatewayService{channelService: channelSvc}

	gid := int64(10)
	require.False(t, svc.checkChannelPricingRestriction(context.Background(), &gid, "unknown-model"),
		"upstream mode should skip pre-check (per-account check needed)")
}

func TestCheckChannelPricingRestriction_RestrictModelsDisabled(t *testing.T) {
	t.Parallel()
	ch := Channel{
		ID:             1,
		Status:         StatusActive,
		GroupIDs:       []int64{10},
		RestrictModels: false, // 未开启模型限制
		ModelPricing: []ChannelModelPricing{
			{Platform: "anthropic", Models: []string{"claude-opus-4-6"}},
		},
	}
	channelSvc := newTestChannelService(makeStandardRepo(ch, map[int64]string{10: "anthropic"}))
	svc := &GatewayService{channelService: channelSvc}

	gid := int64(10)
	require.False(t, svc.checkChannelPricingRestriction(context.Background(), &gid, "any-model"),
		"RestrictModels=false → always allowed")
}

func TestCheckChannelPricingRestriction_NoChannel(t *testing.T) {
	t.Parallel()
	// 分组没有关联渠道
	repo := &mockChannelRepository{
		listAllFn: func(_ context.Context) ([]Channel, error) { return nil, nil },
	}
	channelSvc := newTestChannelService(repo)
	svc := &GatewayService{channelService: channelSvc}

	gid := int64(999)
	require.False(t, svc.checkChannelPricingRestriction(context.Background(), &gid, "any-model"),
		"no channel for group → allowed")
}

// --- isUpstreamModelRestrictedByChannel ---

func TestIsUpstreamModelRestrictedByChannel_Restricted(t *testing.T) {
	t.Parallel()
	ch := Channel{
		ID:             1,
		Status:         StatusActive,
		GroupIDs:       []int64{10},
		RestrictModels: true,
		ModelPricing: []ChannelModelPricing{
			{Platform: "anthropic", Models: []string{"claude-opus-4-6"}},
		},
	}
	channelSvc := newTestChannelService(makeStandardRepo(ch, map[int64]string{10: "anthropic"}))
	svc := &GatewayService{channelService: channelSvc}

	account := &Account{Platform: PlatformAntigravity}
	// claude-sonnet-4-6 在 DefaultAntigravityModelMapping 中，映射后仍为 claude-sonnet-4-6
	// 但定价列表只有 claude-opus-4-6
	require.True(t, svc.isUpstreamModelRestrictedByChannel(context.Background(), 10, account, "claude-sonnet-4-6"),
		"upstream model claude-sonnet-4-6 NOT in pricing → restricted")
}

func TestIsUpstreamModelRestrictedByChannel_Allowed(t *testing.T) {
	t.Parallel()
	ch := Channel{
		ID:             1,
		Status:         StatusActive,
		GroupIDs:       []int64{10},
		RestrictModels: true,
		ModelPricing: []ChannelModelPricing{
			{Platform: "anthropic", Models: []string{"claude-sonnet-4-6"}},
		},
	}
	channelSvc := newTestChannelService(makeStandardRepo(ch, map[int64]string{10: "anthropic"}))
	svc := &GatewayService{channelService: channelSvc}

	account := &Account{Platform: PlatformAntigravity}
	require.False(t, svc.isUpstreamModelRestrictedByChannel(context.Background(), 10, account, "claude-sonnet-4-6"),
		"upstream model claude-sonnet-4-6 IS in pricing → allowed")
}

func TestIsUpstreamModelRestrictedByChannel_UnsupportedModel(t *testing.T) {
	t.Parallel()
	ch := Channel{
		ID:             1,
		Status:         StatusActive,
		GroupIDs:       []int64{10},
		RestrictModels: true,
		ModelPricing: []ChannelModelPricing{
			{Platform: "anthropic", Models: []string{"claude-opus-4-6"}},
		},
	}
	channelSvc := newTestChannelService(makeStandardRepo(ch, map[int64]string{10: "anthropic"}))
	svc := &GatewayService{channelService: channelSvc}

	account := &Account{Platform: PlatformAntigravity}
	// totally-unknown-model 不在 DefaultAntigravityModelMapping 中 → 映射结果为空
	require.False(t, svc.isUpstreamModelRestrictedByChannel(context.Background(), 10, account, "totally-unknown-model"),
		"unmappable model → upstream model empty → not restricted (account filter handles this)")
}

// ---- merged from gateway_compat_reasoning_pricing_test.go ----
func TestGatewayAnthropicCompatReasoningPricingUsesForwardedEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	endpoints := []struct {
		name string
		path string
		call func(*GatewayService, context.Context, *gin.Context, *Account, []byte) (*ForwardResult, error)
	}{
		{"chat_completions", "/v1/chat/completions", func(s *GatewayService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
			return s.ForwardAsChatCompletions(ctx, c, account, body, nil)
		}},
		{"responses", "/v1/responses", func(s *GatewayService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
			return s.ForwardAsResponses(ctx, c, account, body, nil)
		}},
	}
	for _, endpoint := range endpoints {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name       string
				model      string
				effort     string
				thinking   string
				wantEffort string
				multiplier float64
			}{
				{"xhigh_converts_to_max", "claude-fable-5-1", "xhigh", "", "max", 3},
				{"native_max", "claude-fable-5-1", "max", "", "max", 3},
				{"missing_effort", "kimi-k3", "", "", "", 1},
				{"disabled_thinking", "kimi-k3", "", "disabled", "", 1},
				{"unforwarded_thinking", "kimi-k3", "", "enabled", "", 1},
			} {
				mode := "buffered"
				if stream {
					mode = "streaming"
				}
				t.Run(endpoint.name+"/"+mode+"/"+tc.name, func(t *testing.T) {
					request := map[string]any{"model": tc.model, "stream": stream}
					if endpoint.name == "chat_completions" {
						request["messages"] = []map[string]string{{"role": "user", "content": "hello"}}
						if tc.effort != "" {
							request["reasoning_effort"] = tc.effort
						}
					} else {
						request["input"] = "hello"
						if tc.effort != "" {
							request["reasoning"] = map[string]string{"effort": tc.effort}
						}
					}
					if tc.thinking != "" {
						request["thinking"] = map[string]string{"type": tc.thinking}
					}
					body, err := json.Marshal(request)
					require.NoError(t, err)
					upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:       io.NopCloser(strings.NewReader(namespaceToolAnthropicStream())),
					}}}
					svc := &GatewayService{
						cfg: &config.Config{}, httpUpstream: upstream,
						tlsFPProfileService: &TLSFingerprintProfileService{},
					}
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(string(body)))
					account := &Account{
						ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
						Credentials: map[string]any{"api_key": "test-key"},
					}
					result, err := endpoint.call(svc, context.Background(), c, account, body)
					require.NoError(t, err)
					require.Len(t, upstream.requestBodies, 1)
					require.Equal(t, tc.wantEffort, gjson.GetBytes(upstream.requestBodies[0], "output_config.effort").String())
					require.Equal(t, tc.wantEffort, optionalStringValue(result.ReasoningEffort))
					if tc.wantEffort == "" {
						require.Nil(t, result.ReasoningEffort)
						require.False(t, OpenAIBodyHasThinkingEnabled(upstream.requestBodies[0]))
					}

					billing := NewBillingService(&config.Config{}, nil)
					group := &Group{ID: 1, Platform: PlatformAnthropic, ModelPricing: []ChannelModelPricing{{
						Models: []string{tc.model}, BillingMode: BillingModeToken,
						InputPrice: testPtrFloat64(1e-6), OutputPrice: testPtrFloat64(2e-6),
						ReasoningEffortMultipliers: map[string]float64{"xhigh": 2, "max": 3, "high": 1.5},
					}}}
					cost, err := billing.CalculateTokenCostForRequest(TokenCostRequest{
						Ctx: context.Background(), Model: result.Model, Group: group,
						Tokens:          UsageTokens{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens},
						ReasoningEffort: optionalStringValue(result.ReasoningEffort), RateMultiplier: 1,
						Resolver: NewModelPricingResolver(nil, billing),
					})
					require.NoError(t, err)
					require.InDelta(t, 20e-6*tc.multiplier, cost.TotalCost, 1e-12)
				})
			}
		}
	}
}

// ---- merged from gateway_fallbacks_sanitize_test.go ----
// ============================================================================
// 背景
// ============================================================================
//
// Anthropic 上游对 body.fallbacks / body.fallback_credit_token 字段实施
// Pydantic extra='forbid' 校验：当且仅当 anthropic-beta header 含
// server-side-fallback-2026-07-01（fallback_credit_token 额外接受
// fallback-credit-2026-07-01 / fallback-credit-2026-06-01）时接受。
// 否则报：
//   "fallbacks: Extra inputs are not permitted"
//
// fallbacks 是 beta Messages API 的 server-side refusal fallback 字段；本仓
// 不写入该字段，全部来自客户端（Claude Code / SDK / OpenCode 等）透传。
// OAuth mimic 用 FullClaudeCodeMimicryBetas 覆盖客户端 beta（不含 fallback
// beta），因此必须在出口按最终 beta header 条件 strip，与 context_management
// 的对称约束同构。策略是"剥字段，不注入 beta"：fallback 会换模型、改计费，
// 不允许当默认打开。
//
// 本文件覆盖：
//   1) sanitizeAnthropicBodyForBetaTokens 对 fallbacks / fallback_credit_token
//      的条件 strip（以及与 context_management 的组合行为）
//   2) buildUpstreamRequest OAuth mimic / API-key passthrough 端到端
//   3) Bedrock 路径的对称 strip（PrepareBedrockRequestBodyWithTokens /
//      sanitizeBedrockCCFields）

// ============================================================================
// sanitizeAnthropicBodyForBetaTokens — fallbacks / fallback_credit_token
// ============================================================================

func TestSanitizeAnthropicBodyForBetaTokens_NoFallbackFieldsNoChange(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
}

func TestSanitizeAnthropicBodyForBetaTokens_FallbacksKeptWhenBetaPresent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","fallbacks":"default","messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body,
		"claude-code-20250219,oauth-2025-04-20,server-side-fallback-2026-07-01")
	require.False(t, changed, "客户端 header 已带 server-side-fallback beta → 字段保留（不过度删除）")
	require.True(t, gjson.GetBytes(out, "fallbacks").Exists())
	require.Equal(t, "default", gjson.GetBytes(out, "fallbacks").String())
}

func TestSanitizeAnthropicBodyForBetaTokens_FallbacksStrippedWhenBetaMissing(t *testing.T) {
	// 客户端透传的两种形态：字符串 "default" 与模型数组
	for name, fallbacks := range map[string]string{
		"string_default": `"fallbacks":"default"`,
		"model_array":    `"fallbacks":["claude-opus-4-6","claude-sonnet-4-6"]`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"model":"claude-haiku-4-5",` + fallbacks + `,"messages":[]}`)
			// 模拟 OAuth mimic / 默认 API-key beta：只有 oauth/interleaved，无 fallback beta
			out, changed := sanitizeAnthropicBodyForBetaTokens(body,
				"oauth-2025-04-20,interleaved-thinking-2025-05-14")
			require.True(t, changed)
			require.False(t, gjson.GetBytes(out, "fallbacks").Exists(),
				"header 不含 server-side-fallback beta 时必须 strip fallbacks，否则上游 400")
			require.True(t, gjson.GetBytes(out, "messages").Exists(), "strip 不得误伤其他字段")
		})
	}
}

func TestSanitizeAnthropicBodyForBetaTokens_FallbacksStrippedWhenHeaderEmpty(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","fallbacks":"default","messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "fallbacks").Exists())
}

func TestSanitizeAnthropicBodyForBetaTokens_FallbackCreditTokenStrippedWhenCreditBetaMissing(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","fallback_credit_token":"tok_123","messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20,interleaved-thinking-2025-05-14")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "fallback_credit_token").Exists(),
		"缺 credit/fallback beta 时必须 strip fallback_credit_token")
}

func TestSanitizeAnthropicBodyForBetaTokens_FallbackCreditTokenKeptWithAnyAcceptedBeta(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","fallback_credit_token":"tok_123","messages":[]}`)
	// 三个 beta token 任意一个在 header 中都必须保留字段
	for _, beta := range []string{
		claude.BetaServerSideFallback,
		claude.BetaFallbackCredit,
		claude.BetaFallbackCreditLegacy,
	} {
		out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20,"+beta)
		require.Falsef(t, changed, "header 含 %s 时 fallback_credit_token 必须保留", beta)
		require.Truef(t, gjson.GetBytes(out, "fallback_credit_token").Exists(),
			"header 含 %s 时 fallback_credit_token 必须保留", beta)
	}
}

// ★ 组合场景：只带 context-management beta → 剥 fallbacks，保留 context_management
// （守住"早退导致 fallbacks 漏洗"与"过度删除 context_management"两个方向的回归）
func TestSanitizeAnthropicBodyForBetaTokens_StripsFallbacksKeepsContextManagement(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"fallbacks":"default","messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "context-management-2025-06-27")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "fallbacks").Exists(),
		"header 只有 context-management beta → fallbacks 必须 strip")
	require.True(t, gjson.GetBytes(out, "context_management").Exists(),
		"context-management beta 在 header 中 → context_management 不得被误删")
}

func TestSanitizeAnthropicBodyForBetaTokens_KeepsBothWhenBothBetasPresent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"fallbacks":"default","fallback_credit_token":"tok_123","messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body,
		"context-management-2025-06-27,server-side-fallback-2026-07-01")
	require.False(t, changed, "两个 beta 都在 header 中 → 所有字段保留")
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
	require.True(t, gjson.GetBytes(out, "fallbacks").Exists())
	require.True(t, gjson.GetBytes(out, "fallback_credit_token").Exists())
}

func TestSanitizeAnthropicBodyForBetaTokens_EmptyBodyUnchanged(t *testing.T) {
	out, changed := sanitizeAnthropicBodyForBetaTokens([]byte{}, "server-side-fallback-2026-07-01")
	require.False(t, changed)
	require.Empty(t, out)

	out, changed = sanitizeAnthropicBodyForBetaTokens(nil, "server-side-fallback-2026-07-01")
	require.False(t, changed)
	require.Empty(t, out)
}

// ============================================================================
// buildUpstreamRequest 端到端
// 挡住未来某人忘调 sanitize / 将 sanitize 挪到 CCH 之后 等 regression。
// ============================================================================

// OAuth mimic：FullClaudeCodeMimicryBetas 不含 fallback beta → body.fallbacks
// 必须被 strip，且 outgoing anthropic-beta 不得注入 server-side-fallback beta
// （剥字段，不注入 beta）。
func TestBuildUpstreamRequest_OAuthMimicHaiku_StripsFallbacksEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	account := &Account{ID: 601, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-tok"},
		Status:      StatusActive,
		Schedulable: true,
	}
	// 客户端默认透传 "fallbacks":"default"（Claude Code / SDK / OpenCode 等）
	body := []byte(`{"model":"claude-haiku-4-5","fallbacks":"default","messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body,
		"oauth-tok", "oauth", "claude-haiku-4-5", false, true, // mimicClaudeCode=true
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	outBeta := getHeaderRaw(req.Header, "anthropic-beta")

	require.False(t, gjson.GetBytes(outBody, "fallbacks").Exists(),
		"OAuth mimic 端到端：mimic beta 集合不含 fallback beta → outgoing body 必须没有 fallbacks，"+
			"否则上游报 fallbacks: Extra inputs are not permitted")
	require.False(t, anthropicBetaTokensContains(outBeta, claude.BetaServerSideFallback),
		"修复策略是剥字段而非注入 beta：outgoing anthropic-beta 不得含 server-side-fallback beta")
	require.True(t, anthropicBetaTokensContains(outBeta, claude.BetaContextManagement),
		"mimic beta 集合本身不受影响")
}

// API-key passthrough + 客户端 header 未带 fallback beta → strip
func TestBuildUpstreamRequestAnthropicAPIKeyPassthrough_StripsFallbacksWhenClientHeaderMissingBeta(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	// 客户端仅带 oauth beta，不带 server-side-fallback-2026-07-01
	c.Request.Header.Set("Anthropic-Beta", "oauth-2025-04-20")

	body := []byte(`{"model":"claude-haiku-4-5","fallbacks":"default","messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(
		context.Background(), c, newAnthropicAPIKeyPassthroughAccountForBetaTest(), body, "token",
	)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(readUpstreamBodyForTest(t, req), "fallbacks").Exists(),
		"API-key passthrough + 客户端未带 fallback beta → strip body 字段")
}

// API-key passthrough + 客户端 header 带 fallback beta → 保留（不过度删除）
func TestBuildUpstreamRequestAnthropicAPIKeyPassthrough_PreservesFallbacksWhenClientHeaderHasBeta(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Anthropic-Beta", "oauth-2025-04-20,server-side-fallback-2026-07-01")

	// 模型数组形态：有 beta 时必须原样保留
	body := []byte(`{"model":"claude-opus-4-7","fallbacks":["claude-opus-4-6","claude-sonnet-4-6"],"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(
		context.Background(), c, newAnthropicAPIKeyPassthroughAccountForBetaTest(), body, "token",
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	require.True(t, gjson.GetBytes(outBody, "fallbacks").Exists(),
		"客户端 header 带 server-side-fallback beta → 字段保留（不过度删除）")
	fallbacks := gjson.GetBytes(outBody, "fallbacks").Array()
	require.Len(t, fallbacks, 2)
	require.Equal(t, "claude-opus-4-6", fallbacks[0].String())
	require.Equal(t, "claude-sonnet-4-6", fallbacks[1].String())
}

// ============================================================================
// Bedrock 对称 strip
// ============================================================================

// fallback beta token 不在 bedrockSupportedBetaTokens 白名单内（会被
// filterBedrockBetaTokens 过滤），因此条件 strip 实际总会剥除——这是预期。
func TestPrepareBedrockRequestBodyWithTokens_FallbacksRequireSupportedBeta(t *testing.T) {
	modelID := "us.anthropic.claude-opus-4-6-v1"

	t.Run("strips fallbacks when final tokens omit server-side-fallback beta", func(t *testing.T) {
		input := `{
			"messages":[{"role":"user","content":"hi"}],
			"max_tokens":100,
			"fallbacks":"default",
			"fallback_credit_token":"tok_123"
		}`
		betaTokens := []string{"context-1m-2025-08-07"}

		result, err := PrepareBedrockRequestBodyWithTokens([]byte(input), modelID, betaTokens, false)
		require.NoError(t, err)

		assert.False(t, gjson.GetBytes(result, "fallbacks").Exists())
		assert.False(t, gjson.GetBytes(result, "fallback_credit_token").Exists())
		assert.Equal(t, "hi", gjson.GetBytes(result, "messages.0.content").String())
		assert.Equal(t, int64(100), gjson.GetBytes(result, "max_tokens").Int())
	})

	t.Run("strips fallbacks even when client passes server-side-fallback token (not whitelisted)", func(t *testing.T) {
		input := `{"messages":[{"role":"user","content":"hi"}],"max_tokens":100,"fallbacks":"default"}`

		result, err := PrepareBedrockRequestBodyWithTokens(
			[]byte(input), modelID, []string{claude.BetaServerSideFallback}, false,
		)
		require.NoError(t, err)

		assert.False(t, gjson.GetBytes(result, "fallbacks").Exists(),
			"Bedrock 白名单不含 fallback beta → 条件 strip 总会剥除（预期）")
		for _, name := range bedrockAnthropicBetaNames(result) {
			assert.NotEqual(t, claude.BetaServerSideFallback, name,
				"fallback beta 不得进入 Bedrock anthropic_beta 白名单输出")
		}
	})

	t.Run("leaves body without fallback fields otherwise intact", func(t *testing.T) {
		input := `{"messages":[{"role":"user","content":"hi"}],"max_tokens":100}`

		result, err := PrepareBedrockRequestBodyWithTokens([]byte(input), modelID, nil, false)
		require.NoError(t, err)

		assert.False(t, gjson.GetBytes(result, "fallbacks").Exists())
		assert.False(t, gjson.GetBytes(result, "fallback_credit_token").Exists())
		assert.False(t, gjson.GetBytes(result, "context_management").Exists())
		assert.Equal(t, "hi", gjson.GetBytes(result, "messages.0.content").String())
	})
}

// sanitizeBedrockCCFields：fallbacks / fallback_credit_token 是 Anthropic 直连
// beta API 专有字段，Bedrock 无对应 beta → 无条件剥除。
func TestSanitizeBedrockCCFields_StripsFallbacksUnconditionally(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-6","context_management":{"edits":[]},"fallbacks":"default","fallback_credit_token":"tok_123","messages":[]}`)
	result := sanitizeBedrockCCFields(body)

	assert.False(t, gjson.GetBytes(result, "fallbacks").Exists())
	assert.False(t, gjson.GetBytes(result, "fallback_credit_token").Exists())
	assert.False(t, gjson.GetBytes(result, "context_management").Exists())
	assert.True(t, gjson.GetBytes(result, "messages").Exists())
}

// ---- merged from gateway_forward_as_chat_completions_test.go ----
func TestHandleCCBufferedFromAnthropic_ToolArgumentsAreValidJSON(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_tool","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","usage":{"input_tokens":10}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		``,
	}, "\n")))}

	_, err := (&GatewayService{}).handleCCBufferedFromAnthropic(resp, c, "gpt-5", "claude-sonnet-4.5", nil, time.Now())
	require.NoError(t, err)

	var body struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Choices, 1)
	require.Len(t, body.Choices[0].Message.ToolCalls, 1)
	args := body.Choices[0].Message.ToolCalls[0].Function.Arguments
	require.JSONEq(t, `{"city":"Paris"}`, args)
}

func TestExtractCCReasoningEffortFromBody(t *testing.T) {
	t.Parallel()

	t.Run("nested reasoning.effort", func(t *testing.T) {
		got := extractCCReasoningEffortFromBody([]byte(`{"reasoning":{"effort":"HIGH"}}`))
		require.NotNil(t, got)
		require.Equal(t, "high", *got)
	})

	t.Run("flat reasoning_effort", func(t *testing.T) {
		got := extractCCReasoningEffortFromBody([]byte(`{"reasoning_effort":"x-high"}`))
		require.NotNil(t, got)
		require.Equal(t, "xhigh", *got)
	})

	t.Run("DeepSeek max", func(t *testing.T) {
		got := extractCCReasoningEffortFromBody([]byte(`{"model":"deepseek-v4-flash","reasoning_effort":"Max"}`))
		require.NotNil(t, got)
		require.Equal(t, "max", *got)
	})

	t.Run("mapped Kimi alias max", func(t *testing.T) {
		got := extractCCReasoningEffortFromBody(
			[]byte(`{"model":"public-alias","reasoning_effort":"max"}`),
			"kimi-k3",
			"public-alias",
		)
		require.NotNil(t, got)
		require.Equal(t, "max", *got)
	})

	t.Run("legacy model max", func(t *testing.T) {
		got := extractCCReasoningEffortFromBody([]byte(`{"model":"gpt-5.5","reasoning_effort":"max"}`))
		require.NotNil(t, got)
		require.Equal(t, "xhigh", *got)
	})

	t.Run("missing effort", func(t *testing.T) {
		require.Nil(t, extractCCReasoningEffortFromBody([]byte(`{"model":"gpt-5"}`)))
	})
}

func TestHandleCCBufferedFromAnthropic_PreservesMessageStartCacheUsageAndReasoning(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	reasoningEffort := "high"
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_cc_buffered"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":12,"cache_read_input_tokens":9,"cache_creation_input_tokens":3}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hello"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleCCBufferedFromAnthropic(resp, c, "gpt-5", "claude-sonnet-4.5", &reasoningEffort, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
	require.Equal(t, 9, result.Usage.CacheReadInputTokens)
	require.Equal(t, 3, result.Usage.CacheCreationInputTokens)
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "high", *result.ReasoningEffort)
}

// Kimi 等 Anthropic 兼容上游返回 SSE 紧凑格式（冒号后无空格），CC 桥此前按
// "event: " / "data: " 严格匹配会丢弃全部事件，最终报 "Upstream stream ended
// without a response"（#4653 同根因；#4657 只修了 /v1/responses 桥）。
func TestHandleCCBufferedFromAnthropic_CompactSSEFormat(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_cc_buffered_compact"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event:message_start`,
			`data:{"type":"message_start","message":{"id":"msg_c1","type":"message","role":"assistant","content":[],"model":"k3","stop_reason":"","usage":{"input_tokens":15,"cache_read_input_tokens":5,"cache_creation_input_tokens":2}}}`,
			``,
			`event:content_block_start`,
			`data:{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"OK"}}`,
			``,
			`event:message_delta`,
			`data:{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleCCBufferedFromAnthropic(resp, c, "k3", "k3", nil, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 15, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Equal(t, 5, result.Usage.CacheReadInputTokens)
	require.Equal(t, 2, result.Usage.CacheCreationInputTokens)
	require.Contains(t, rec.Body.String(), `"OK"`, "紧凑格式事件必须被解析并产出响应内容")
}

func TestHandleCCStreamingFromAnthropic_CompactSSEFormat(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_cc_stream_compact"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event:message_start`,
			`data:{"type":"message_start","message":{"id":"msg_c2","type":"message","role":"assistant","content":[],"model":"k3","stop_reason":"","usage":{"input_tokens":21,"cache_read_input_tokens":6,"cache_creation_input_tokens":1}}}`,
			``,
			`event:content_block_start`,
			`data:{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"OK"}}`,
			``,
			`event:message_delta`,
			`data:{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
			``,
			`event:message_stop`,
			`data:{"type":"message_stop"}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleCCStreamingFromAnthropic(resp, c, "k3", "k3", nil, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 21, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, 6, result.Usage.CacheReadInputTokens)
	require.Equal(t, 1, result.Usage.CacheCreationInputTokens)
	require.Contains(t, rec.Body.String(), `[DONE]`)
}

func TestHandleCCStreamingFromAnthropic_PreservesMessageStartCacheUsageAndReasoning(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	reasoningEffort := "medium"
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_cc_stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":20,"cache_read_input_tokens":11,"cache_creation_input_tokens":4}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hello"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleCCStreamingFromAnthropic(resp, c, "gpt-5", "claude-sonnet-4.5", &reasoningEffort, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 20, result.Usage.InputTokens)
	require.Equal(t, 8, result.Usage.OutputTokens)
	require.Equal(t, 11, result.Usage.CacheReadInputTokens)
	require.Equal(t, 4, result.Usage.CacheCreationInputTokens)
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "medium", *result.ReasoningEffort)
	require.Contains(t, rec.Body.String(), `[DONE]`)
}

// ---- merged from gateway_image_reasoning_pricing_test.go ----
func TestRecordUsage_ImageReasoningPricing(t *testing.T) {
	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI} {
		for _, source := range []string{PricingSourceChannel, PricingSourceGroup} {
			for _, mode := range []BillingMode{BillingModeImage, BillingModePerRequest} {
				for _, independent := range []bool{false, true} {
					for _, effort := range []string{"high", "low", ""} {
						name := fmt.Sprintf("%s/%s/%s/independent=%t/effort=%s", platform, source, mode, independent, effort)
						t.Run(name, func(t *testing.T) {
							usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
							userRepo := &openAIRecordUsageUserRepoStub{}
							subRepo := &openAIRecordUsageSubRepoStub{}
							groupID := int64(77)
							model := "gemini-3-pro-image-preview"
							price := 0.25
							pricing := ChannelModelPricing{
								Models: []string{model}, BillingMode: mode, PerRequestPrice: &price,
								ReasoningEffortMultipliers: map[string]float64{"high": 2},
							}
							resolver := newOpenAIImageChannelPricingResolverForTest(t, groupID, model, price)
							cache := resolver.channelService.cache.Load().(*channelCache)
							cache.pricingByGroupModel[channelModelKey{groupID: groupID, model: model}] = &pricing
							group := &Group{
								ID: groupID, Platform: platform, Status: StatusActive, Hydrated: true,
								RateMultiplier: 0.5, ImageRateIndependent: independent, ImageRateMultiplier: 0.25,
							}
							if source == PricingSourceGroup {
								group.ModelPricing = []ChannelModelPricing{pricing}
								// A matching group card owns the multiplier; the channel must not stack on top.
								pricing.ReasoningEffortMultipliers = map[string]float64{"high": 3}
							}
							apiKey := &APIKey{ID: 10, GroupID: &groupID, Group: group}
							account := &Account{ID: 30, Platform: platform, Type: AccountTypeAPIKey}
							user := &User{ID: 20}
							var forwardedEffort *string
							if effort != "" {
								forwardedEffort = &effort
							}
							if platform == PlatformAnthropic {
								svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)
								svc.resolver = resolver
								require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
									Result: &ForwardResult{
										RequestID: "image_reasoning_pricing", Model: model, Duration: time.Second,
										ImageCount: 2, ImageSize: "1K", ReasoningEffort: forwardedEffort,
									},
									APIKey: apiKey, User: user, Account: account,
								}))
							} else {
								svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
								svc.resolver = resolver
								require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
									Result: &OpenAIForwardResult{
										RequestID: "image_reasoning_pricing", Model: model, Duration: time.Second,
										ImageCount: 2, ImageSize: "1K", ReasoningEffort: forwardedEffort,
									},
									APIKey: apiKey, User: user, Account: account,
								}))
							}
							wantTotal := 0.5
							if effort == "high" {
								wantTotal *= 2
							}
							wantRate := 0.5
							if independent {
								wantRate = 0.25
							}
							require.NotNil(t, usageRepo.lastLog)
							require.InDelta(t, wantTotal, usageRepo.lastLog.TotalCost, 1e-12)
							require.InDelta(t, wantTotal*wantRate, usageRepo.lastLog.ActualCost, 1e-12)
							require.InDelta(t, wantTotal*wantRate, userRepo.lastAmount, 1e-12)
							require.Equal(t, forwardedEffort, usageRepo.lastLog.ReasoningEffort)
							require.Equal(t, 2, usageRepo.lastLog.ImageCount)
							require.Equal(t, string(mode), *usageRepo.lastLog.BillingMode)
						})
					}
				}
			}
		}
	}
}

func TestCalculateRecordUsageCost_MediaReasoningPricing(t *testing.T) {
	for _, source := range []string{PricingSourceChannel, PricingSourceGroup} {
		for _, media := range []string{"gateway_audio", "openai_audio", "openai_video"} {
			t.Run(source+"/"+media, func(t *testing.T) {
				groupID := int64(77)
				model, effort := "media-model", "high"
				price := 0.25
				resolver := newOpenAIImageChannelPricingResolverForTest(t, groupID, model, price)
				cache := resolver.channelService.cache.Load().(*channelCache)
				pricing := cache.pricingByGroupModel[channelModelKey{groupID: groupID, model: model}]
				pricing.BillingMode = BillingModePerRequest
				pricing.ReasoningEffortMultipliers = map[string]float64{"high": 2}
				wantTotal := 1.0 // Two units at $0.25, with high=2.
				if media == "openai_video" {
					pricing.BillingMode = BillingModeVideo
					wantTotal = 5 // Two five-second videos at $0.25/second, with high=2.
				}
				apiKey := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, Hydrated: true}}
				if source == PricingSourceGroup {
					pricing.Models = []string{model}
					apiKey.Group.ModelPricing = []ChannelModelPricing{*pricing}
					pricing.ReasoningEffortMultipliers = map[string]float64{"high": 3}
				}
				var cost *CostBreakdown
				if media == "gateway_audio" {
					svc := &GatewayService{billingService: resolver.billingService, resolver: resolver}
					cost = svc.calculateRecordUsageCost(context.Background(), &ForwardResult{
						ReasoningEffort: &effort, AudioUsage: &AudioUsage{Mode: "tts", DurationOrUnits: 2},
					}, apiKey, model, 0.5, 0.5, time.Time{})
				} else {
					svc := &OpenAIGatewayService{billingService: resolver.billingService, resolver: resolver}
					result := &OpenAIForwardResult{ReasoningEffort: &effort}
					if media == "openai_audio" {
						result.AudioUsage = &AudioUsage{Mode: "tts", DurationOrUnits: 2}
					} else {
						result.VideoCount, result.VideoDurationSeconds = 2, 5
					}
					var err error
					cost, err = svc.calculateOpenAIRecordUsageCost(context.Background(), result, apiKey,
						[]string{model}, 0.5, 0.5, 0.5, 0.5, UsageTokens{}, "", nil, time.Time{})
					require.NoError(t, err)
				}
				require.NotNil(t, cost)
				require.InDelta(t, wantTotal, cost.TotalCost, 1e-12)
				require.InDelta(t, wantTotal*0.5, cost.ActualCost, 1e-12)
			})
		}
	}
}

// ---- merged from gateway_messages_cache_json_test.go ----
func TestAddMessageCacheBreakpoints_JSONStringEscaping(t *testing.T) {
	var controls strings.Builder
	for r := rune(0); r <= 0x9f; r++ {
		controls.WriteRune(r)
	}
	tests := []struct{ name, text string }{
		{"delete_character", "before\x7fafter"},
		{"bell_and_vertical_tab", "before\a\vafter"},
		{"nul_and_escape", "before\x00\x1bafter"},
		{"all_ascii_and_c1_controls", controls.String()},
		{"supplementary_nonprinting_rune", "before\U000e0067after"},
		{"unicode_and_json_metacharacters", "中文 😀 <>& \"quote\" \\slash\n\r\t\b\f"},
		{"literal_backslash_sequences", `literal \x7f \a \v \U000e0067`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Both the final message and the second-to-last user turn are promoted
			// from strings to text blocks by the cache rewrite.
			body, err := json.Marshal(map[string]any{
				"messages": []map[string]any{
					{"role": "user", "content": tt.text},
					{"role": "assistant", "content": "ack"},
					{"role": "user", "content": "next"},
					{"role": "assistant", "content": tt.text},
				},
			})
			require.NoError(t, err)
			out := addMessageCacheBreakpoints(body)
			// gjson accepts some malformed input, so validate with encoding/json
			// before inspecting the rewritten content and cache metadata.
			var decoded any
			require.NoError(t, json.Unmarshal(out, &decoded))
			for _, path := range []string{"messages.0.content", "messages.3.content"} {
				content := gjson.GetBytes(out, path)
				require.True(t, content.IsArray())
				require.Len(t, content.Array(), 1)
				block := content.Array()[0]
				require.Equal(t, "text", block.Get("type").String())
				require.Equal(t, tt.text, block.Get("text").String())
				require.Equal(t, "ephemeral", block.Get("cache_control.type").String())
				require.Equal(t, "5m", block.Get("cache_control.ttl").String())
			}
			require.JSONEq(t, string(out), string(addMessageCacheBreakpoints(out)))
		})
	}
}

// ---- merged from gateway_mid_conversation_output_config_test.go ----
// ============================================================================
// message-level output_config ↔ mid-conversation-output-config beta
// ============================================================================
//
// 背景：pi-ai（Harness 使用的 Anthropic provider）会为 opus5 生成形如
//
//	{"role":"system","content":[],"output_config":{"effort":"high"}}
//
// 的空控制消息，并在 anthropic-beta 中请求
// mid-conversation-output-config-2026-07-01。OAuth mimic 会用
// FullClaudeCodeMimicryBetas 覆盖客户端 beta；若该 beta 不在固定列表内，
// body 消息级 output_config 与最终 header 不再对称 → 上游 400：
//
//	"output_config: Extra inputs are not permitted"
//
// 修复策略（剥字段，不注入 beta）：
//   - header 缺该 token：仅为携带 message-level output_config 的消息剥此字段；
//     role=system 且 content 无正文（缺失 / null / 空 string / 空 array / 仅空
//     text 块）时整条删除；system 有正文保留正文与其余字段；user/assistant 只剥
//     字段，绝不整条删除。
//   - header 含该 token：完全保留。
//   - 无任何 message-level output_config：字节 no-op。
//   - 顶层 output_config / effort 不受该 beta 约束，本增量不触碰。
//
// 本文件只覆盖 Anthropic 直连路径的该增量；header 侧固定列表（mimic betas）
// 的改动由并行增量负责。

// ============================================================================
// sanitizeAnthropicBodyForBetaTokens — message-level output_config
// ============================================================================

// ★ 主场景：缺 beta 时，多条空控制 system 消息整条删除，且用户顺序 / 顶层 effort
// 不受影响；有正文 system 与 assistant/user 只剥字段。
func TestSanitizeAnthropicBodyForBetaTokens_MidConversationOutputConfig_MultiMessage(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-5",
		"output_config":{"effort":"high"},
		"messages":[
			{"role":"system","content":[],"output_config":{"effort":"high"}},
			{"role":"user","content":"hello"},
			{"role":"system","content":"You are helpful.","output_config":{"effort":"high"},"cache_control":{"type":"ephemeral"}},
			{"role":"assistant","content":[{"type":"text","text":"hi"}],"output_config":{"effort":"high"}},
			{"role":"system","content":[{"type":"text","text":""}],"output_config":{"effort":"high"}},
			{"role":"system","content":null,"output_config":{"effort":"high"}},
			{"role":"system","content":"","output_config":{"effort":"high"}},
			{"role":"system","output_config":{"effort":"high"}}
		]
	}`)

	// 模拟 OAuth mimic / 默认 API-key beta：无 mid-conversation-output-config beta
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "claude-code-20250219,oauth-2025-04-20")
	require.True(t, changed, "存在 message-level output_config 且缺 beta → 必须发生净化")

	msgs := gjson.GetBytes(out, "messages").Array()
	require.Len(t, msgs, 3, "空控制 system 整条删除；仅保留 user + 有正文 system + assistant")

	require.Equal(t, "user", gjson.GetBytes(out, "messages.0.role").String())
	require.Equal(t, "hello", gjson.GetBytes(out, "messages.0.content").String(), "用户消息顺序不得改变")

	require.Equal(t, "system", gjson.GetBytes(out, "messages.1.role").String())
	require.Equal(t, "You are helpful.", gjson.GetBytes(out, "messages.1.content").String())
	require.False(t, gjson.GetBytes(out, "messages.1.output_config").Exists(),
		"有正文 system 只剥 output_config，不整条删除")
	require.True(t, gjson.GetBytes(out, "messages.1.cache_control").Exists(),
		"system 其余字段必须保留")

	require.Equal(t, "assistant", gjson.GetBytes(out, "messages.2.role").String())
	require.Equal(t, "hi", gjson.GetBytes(out, "messages.2.content.0.text").String())
	require.False(t, gjson.GetBytes(out, "messages.2.output_config").Exists(),
		"user/assistant 只剥字段，绝不整条删除")

	// 顶层 output_config / effort 不属于该 beta 保护范围，必须原样保留
	require.Equal(t, "high", gjson.GetBytes(out, "output_config.effort").String())
	require.Equal(t, "claude-opus-5", gjson.GetBytes(out, "model").String())
}

// 表驱动：system 消息 content「无正文」的各种形态都必须整条删除。
func TestSanitizeAnthropicBodyForBetaTokens_MidConversationOutputConfig_EmptySystemVariants(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{"missing_content", `{"role":"system","output_config":{"effort":"high"}}`},
		{"null_content", `{"role":"system","content":null,"output_config":{"effort":"high"}}`},
		{"empty_string_content", `{"role":"system","content":"","output_config":{"effort":"high"}}`},
		{"empty_array_content", `{"role":"system","content":[],"output_config":{"effort":"high"}}`},
		{"only_empty_text_block", `{"role":"system","content":[{"type":"text","text":""}],"output_config":{"effort":"high"}}`},
		{"only_empty_text_blocks", `{"role":"system","content":[{"type":"text","text":""},{"type":"text","text":""}],"output_config":{"effort":"high"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"messages":[` + tc.msg + `,{"role":"user","content":"hi"}],"output_config":{"effort":"high"}}`)
			out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
			require.True(t, changed)

			msgs := gjson.GetBytes(out, "messages").Array()
			require.Len(t, msgs, 1, "无正文的 system 控制消息必须整条删除")
			require.Equal(t, "user", msgs[0].Get("role").String())
			require.Equal(t, "hi", msgs[0].Get("content").String())
			require.Equal(t, "high", gjson.GetBytes(out, "output_config.effort").String(),
				"顶层 effort 不得被连带修改")
		})
	}
}

// 表驱动：system 有正文（含未来非 text 内容块 / 未知块）时不得整条删除，
// 只剥 message-level output_config，正文原样保留。
func TestSanitizeAnthropicBodyForBetaTokens_MidConversationOutputConfig_SystemWithBodyKept(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"non_empty_string", `"You are helpful."`},
		{"text_block", `[{"type":"text","text":"hi"}]`},
		{"text_with_empty_sibling", `[{"type":"text","text":""},{"type":"text","text":"hi"}]`},
		{"future_non_text_block", `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]`},
		{"unknown_block", `[{"foo":"bar"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"messages":[{"role":"system","content":` + tc.content + `,"output_config":{"effort":"high"}}]}`)
			out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
			require.True(t, changed)

			msgs := gjson.GetBytes(out, "messages").Array()
			require.Len(t, msgs, 1, "有正文 system 不得整条删除")
			require.Equal(t, "system", msgs[0].Get("role").String())
			require.False(t, msgs[0].Get("output_config").Exists(),
				"有正文 system 必须剥掉 message-level output_config")
			require.Equal(t, tc.content, msgs[0].Get("content").Raw,
				"正文必须原样保留；非 text / 未知内容块不得被误当空而删除")
		})
	}
}

// header 含该 beta → 完全保留，字节 no-op（即使是空控制 system 消息）。
func TestSanitizeAnthropicBodyForBetaTokens_MidConversationOutputConfig_ByteNoopWhenBetaPresent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","output_config":{"effort":"high"},"messages":[{"role":"system","content":[],"output_config":{"effort":"high"}},{"role":"user","content":"hi"}]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body,
		"claude-code-20250219,oauth-2025-04-20,"+claude.BetaMidConversationOutputConfig)
	require.False(t, changed, "header 含 mid-conversation-output-config beta → 完全保留")
	require.True(t, bytes.Equal(body, out), "含 beta 时必须字节 no-op")
}

// 无 message-level output_config（仅有顶层 output_config，或完全没有）→ 字节 no-op。
func TestSanitizeAnthropicBodyForBetaTokens_MidConversationOutputConfig_NoFieldByteNoop(t *testing.T) {
	bodies := [][]byte{
		// 只有顶层 output_config（effort），消息无该字段
		[]byte(`{"model":"claude-opus-5","output_config":{"effort":"high"},"messages":[{"role":"system","content":[]},{"role":"user","content":"hi"}]}`),
		// 完全没有 output_config
		[]byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`),
	}
	for i, body := range bodies {
		out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
		require.Falsef(t, changed, "case %d: 无 message-level output_config → 不得改动", i)
		require.Truef(t, bytes.Equal(body, out), "case %d: 必须字节 no-op", i)
	}
}

// 幂等：净化后的 body 再跑一次必须是 no-op。
func TestSanitizeAnthropicBodyForBetaTokens_MidConversationOutputConfig_Idempotent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","output_config":{"effort":"high"},"messages":[` +
		`{"role":"system","content":[],"output_config":{"effort":"high"}},` +
		`{"role":"system","content":"sys","output_config":{"effort":"high"}},` +
		`{"role":"user","content":"hi"}]}`)

	first, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
	require.True(t, changed)

	second, changedAgain := sanitizeAnthropicBodyForBetaTokens(first, "oauth-2025-04-20")
	require.False(t, changedAgain, "二次净化必须为 no-op（幂等）")
	require.True(t, bytes.Equal(first, second))
}

// ============================================================================
// 真实 request 联动
// ============================================================================

// OAuth mimic 路径真实 request 联动，两 case 明确期望：
//   - 默认：mimic 固定列表带该 beta → outgoing header 含 token，空控制 system 消息保留；
//   - policy filter 命中（经 gin context 的 betaPolicyFilterSetKey 缓存注入该 token，
//     走真实 policy filter/dropSet 路径）→ outgoing header 无 token，空控制 system
//     消息整条删除。
//
// 两 case 都断言 user 文本、消息数、顶层 effort 原值；期望为显式常量，不引用
// FullClaudeCodeMimicryBetas（避免把实现列表当 expected）。
func TestBuildUpstreamRequestOAuthMimic_MidConversationOutputConfig(t *testing.T) {
	cases := []struct {
		name              string
		policyFilterDrops bool
		wantHeaderHasBeta bool
		wantMsgLen        int
		wantFieldOnFirst  bool
	}{
		{"default_mimic_keeps_beta", false, true, 2, true},
		{"policy_filter_drops_beta", true, false, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			if tc.policyFilterDrops {
				c.Set(betaPolicyFilterSetKey, map[string]struct{}{claude.BetaMidConversationOutputConfig: {}})
			}

			account := &Account{ID: 701, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
				Credentials: map[string]any{"access_token": "oauth-tok"},
				Status:      StatusActive,
				Schedulable: true,
			}
			body := []byte(`{"model":"claude-opus-5","output_config":{"effort":"high"},"messages":[` +
				`{"role":"system","content":[],"output_config":{"effort":"high"}},` +
				`{"role":"user","content":"hello"}]}`)

			svc := &GatewayService{cfg: &config.Config{}}
			req, _, err := svc.buildUpstreamRequest(
				context.Background(), c, account, body,
				"oauth-tok", "oauth", "claude-opus-5", false, true, // mimicClaudeCode=true
			)
			require.NoError(t, err)

			outBody := readUpstreamBodyForTest(t, req)
			outBeta := getHeaderRaw(req.Header, "anthropic-beta")
			require.Equalf(t, tc.wantHeaderHasBeta,
				anthropicBetaTokensContains(outBeta, claude.BetaMidConversationOutputConfig),
				"outgoing anthropic-beta 必须与 filter 结果一致（outgoing beta=%q）", outBeta)

			msgs := gjson.GetBytes(outBody, "messages").Array()
			require.Len(t, msgs, tc.wantMsgLen,
				"空控制 system 消息：header 带 beta 保留 / 缺 beta 整条删除")
			require.Equal(t, tc.wantFieldOnFirst, msgs[0].Get("output_config").Exists())

			var userContent string
			for _, m := range msgs {
				if m.Get("role").String() == "user" {
					userContent = m.Get("content").String()
				}
			}
			require.Equal(t, "hello", userContent, "用户消息文本必须始终保留")

			require.Equal(t, "high", gjson.GetBytes(outBody, "output_config.effort").String(),
				"顶层 effort 不受该 beta 约束")
		})
	}
}

// API-key passthrough 透传路径：客户端 header 带/不带该 beta 时，
// 出站 header 与 body 的 message-level output_config 必须同进同退。
func TestBuildUpstreamRequestAnthropicAPIKeyPassthrough_MidConversationOutputConfigConsistentWithClientHeader(t *testing.T) {
	cases := []struct {
		name       string
		clientBeta string
		wantField  bool
		wantMsgLen int
	}{
		{
			name:       "client_header_has_beta",
			clientBeta: "oauth-2025-04-20," + claude.BetaMidConversationOutputConfig,
			wantField:  true,
			wantMsgLen: 2,
		},
		{
			name:       "client_header_missing_beta",
			clientBeta: "oauth-2025-04-20",
			wantField:  false,
			wantMsgLen: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("Anthropic-Beta", tc.clientBeta)

			body := []byte(`{"model":"claude-opus-5","output_config":{"effort":"high"},"messages":[` +
				`{"role":"system","content":[],"output_config":{"effort":"high"}},` +
				`{"role":"user","content":"hello"}]}`)

			svc := &GatewayService{cfg: &config.Config{}}
			req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(
				context.Background(), c, newAnthropicAPIKeyPassthroughAccountForBetaTest(), body, "token",
			)
			require.NoError(t, err)

			outBody := readUpstreamBodyForTest(t, req)
			outBeta := getHeaderRaw(req.Header, "anthropic-beta")

			require.Equalf(t, tc.wantField, anthropicBetaTokensContains(outBeta, claude.BetaMidConversationOutputConfig),
				"出站 header 必须与客户端传入的 beta 一致（outgoing beta=%q）", outBeta)
			require.Equal(t, tc.wantField, gjson.GetBytes(outBody, "messages.0.output_config").Exists(),
				"header/body 必须一致")
			require.Len(t, gjson.GetBytes(outBody, "messages").Array(), tc.wantMsgLen,
				"缺 beta 时空控制 system 消息整条删除；带 beta 时保留")
		})
	}
}

// ---- merged from gateway_model_availability_test.go ----
func TestDiagnoseModelAvailabilityForPlatform_NoModel_AlwaysAvailable(t *testing.T) {
	repo := &mockAccountRepoForPlatform{accounts: nil, accountsByID: map[int64]*Account{}}
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "", PlatformOpenAI)

	require.True(t, diag.HasAccountsInPool, "empty model must return HasAccountsInPool=true so caller stays on 503")
	require.True(t, diag.HasModelSupport, "empty model must return HasModelSupport=true so caller stays on 503")
}

func TestDiagnoseModelAvailabilityForPlatform_EmptyPlatform_AlwaysAvailable(t *testing.T) {
	repo := &mockAccountRepoForPlatform{accounts: nil, accountsByID: map[int64]*Account{}}
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5", "")

	require.True(t, diag.HasAccountsInPool)
	require.True(t, diag.HasModelSupport, "empty platform must fall back to {true,true} so caller stays on 503")
}

func TestDiagnoseModelAvailabilityForPlatform_NilReceiver(t *testing.T) {
	var svc *GatewayService

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5", PlatformOpenAI)

	require.True(t, diag.HasAccountsInPool)
	require.True(t, diag.HasModelSupport)
}

func TestDiagnoseModelAvailabilityForPlatform_NoAccountsInPool(t *testing.T) {
	repo := &mockAccountRepoForPlatform{accounts: nil, accountsByID: map[int64]*Account{}}
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5", PlatformOpenAI)

	require.False(t, diag.HasAccountsInPool)
	require.False(t, diag.HasModelSupport, "no accounts means no support; caller stays on 503 (empty-pool branch)")
}

func TestDiagnoseModelAvailabilityForPlatform_ExplicitMappingMatches(t *testing.T) {
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:          1,
				Platform:    PlatformOpenAI,
				Status:      StatusActive,
				Schedulable: true,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"gpt-5.1-codex-mini": "gpt-5.1-codex-mini"},
				},
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5.1-codex-mini", PlatformOpenAI)

	require.True(t, diag.HasAccountsInPool)
	require.True(t, diag.HasModelSupport)
}

func TestDiagnoseModelAvailabilityForPlatform_EmptyMappingAllowsAll(t *testing.T) {
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true /* no ModelMapping = allow all */},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5.1-codex-mini", PlatformOpenAI)

	require.True(t, diag.HasModelSupport, "empty model_mapping must be treated as 'allow all' (Account.IsModelSupported semantics)")
}

func TestDiagnoseModelAvailabilityForPlatform_WildcardMappingMatches(t *testing.T) {
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:          1,
				Platform:    PlatformOpenAI,
				Status:      StatusActive,
				Schedulable: true,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"*": "gpt-5"},
				},
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5.1-codex-mini", PlatformOpenAI)

	require.True(t, diag.HasModelSupport, "wildcard mapping must classify the request as 'serviceable'")
}

func TestDiagnoseModelAvailabilityForPlatform_NoMatchingModel_ReturnsNotFoundSignal(t *testing.T) {
	groupID := int64(42)
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:          1,
				Platform:    PlatformOpenAI,
				Status:      StatusActive,
				Schedulable: true,
				AccountGroups: []AccountGroup{
					{GroupID: groupID},
				},
				Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5": "gpt-5"}},
			},
			{
				ID:          2,
				Platform:    PlatformOpenAI,
				Status:      StatusActive,
				Schedulable: true,
				AccountGroups: []AccountGroup{
					{GroupID: groupID},
				},
				Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5-mini": "gpt-5-mini"}},
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "gpt-5.1-codex-mini", PlatformOpenAI)

	require.True(t, diag.HasAccountsInPool, "group has OpenAI accounts")
	require.False(t, diag.HasModelSupport, "no account mapping admits the requested model — handler should return 404")
}

func TestDiagnoseModelAvailabilityForPlatform_RateLimitedSupportingAccountRemainsConfigured(t *testing.T) {
	groupID := int64(42)
	cooldownUntil := time.Now().Add(time.Hour)
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:                     1,
				Platform:               PlatformAnthropic,
				Status:                 StatusActive,
				Schedulable:            true,
				RateLimitResetAt:       &cooldownUntil,
				OverloadUntil:          &cooldownUntil,
				TempUnschedulableUntil: &cooldownUntil,
				AccountGroups:          []AccountGroup{{GroupID: groupID}},
				Credentials: map[string]any{
					"model_mapping": map[string]any{"claude-opus-4-8": "claude-opus-4-8"},
				},
			},
		},
		accountsByID: map[int64]*Account{},
	}
	require.False(t, repo.accounts[0].IsSchedulable(), "test account must be excluded from normal scheduling while cooling down")
	svc := &GatewayService{
		accountRepo:       repo,
		cfg:               testConfig(),
		schedulerSnapshot: &SchedulerSnapshotService{}, // diagnosis must bypass the transient-only snapshot
	}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "claude-opus-4-8", PlatformAnthropic)

	require.True(t, diag.HasAccountsInPool)
	require.True(t, diag.HasModelSupport, "a configured model remains supported while every matching account is temporarily cooling down")
}

func TestOpenAIDiagnoseModelAvailabilityForPlatform_RateLimitedSupportingAccountRemainsConfigured(t *testing.T) {
	groupID := int64(43)
	cooldownUntil := time.Now().Add(time.Hour)
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:                     2,
				Platform:               PlatformOpenAI,
				Status:                 StatusActive,
				Schedulable:            true,
				RateLimitResetAt:       &cooldownUntil,
				OverloadUntil:          &cooldownUntil,
				TempUnschedulableUntil: &cooldownUntil,
				AccountGroups:          []AccountGroup{{GroupID: groupID}},
				Credentials: map[string]any{
					"model_mapping": map[string]any{"claude-opus-4-8": "claude-opus-4-8"},
				},
			},
		},
		accountsByID: map[int64]*Account{},
	}
	require.False(t, repo.accounts[0].IsSchedulable(), "test account must be excluded from normal scheduling while cooling down")
	svc := &OpenAIGatewayService{
		accountRepo:       repo,
		cfg:               testConfig(),
		schedulerSnapshot: &SchedulerSnapshotService{}, // diagnosis must bypass the transient-only snapshot
	}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "claude-opus-4-8", PlatformOpenAI)

	require.True(t, diag.HasAccountsInPool)
	require.True(t, diag.HasModelSupport, "OpenAI-compatible diagnosis must keep transiently limited supporting accounts in the configured pool")
}

func TestDiagnoseModelAvailabilityForPlatform_WrongPlatformFiltersOut(t *testing.T) {
	// Group has only Anthropic accounts; user routes to OpenAI gateway.
	// Diagnosis must NOT see Anthropic accounts (listSchedulableAccounts filters
	// by platform), so HasAccountsInPool is false and the caller stays on 503.
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:          1,
				Platform:    PlatformAnthropic,
				Status:      StatusActive,
				Schedulable: true,
				Credentials: map[string]any{"model_mapping": map[string]any{"claude-sonnet-4-5": "claude-sonnet-4-5"}},
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "gpt-5", PlatformOpenAI)

	require.False(t, diag.HasAccountsInPool, "OpenAI route must not see Anthropic accounts in pool")
	require.False(t, diag.HasModelSupport)
}

// ---- merged from gateway_profit_control_v2_test.go ----
func gatewayProfitTestGroup(id int64, platform string) *Group {
	return &Group{
		ID:                   id,
		Name:                 "profit-" + platform,
		Platform:             platform,
		Status:               StatusActive,
		Hydrated:             true,
		RateMultiplier:       0.5,
		SubscriptionType:     SubscriptionTypeStandard,
		ProfitControlEnabled: true,
		ProfitMinMargin:      0,
		ProfitSafetyBuffer:   0,
	}
}

func gatewayProfitTestContext(group *Group) context.Context {
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)
	ctx, _ = WithGatewayTokenRequestPricing(ctx)
	return ctx
}

func gatewayProfitTestAccount(id int64, platform string, rate float64, groupID int64) Account {
	return Account{
		ID:             id,
		Name:           "account",
		Platform:       platform,
		Type:           AccountTypeAPIKey,
		Status:         StatusActive,
		Schedulable:    true,
		Concurrency:    2,
		Priority:       1,
		RateMultiplier: &rate,
		AccountGroups:  []AccountGroup{{AccountID: id, GroupID: groupID}},
		GroupIDs:       []int64{groupID},
	}
}

func TestGatewayProfitControlInstallsForFivePlatformsOnlyOnTokenRequests(t *testing.T) {
	for _, platform := range []string{
		PlatformOpenAI,
		PlatformAnthropic,
		PlatformGemini,
		PlatformGrok,
		PlatformAntigravity,
	} {
		t.Run(platform, func(t *testing.T) {
			group := gatewayProfitTestGroup(101, platform)
			groupID := group.ID
			svc := &GatewayService{}

			tokenCtx := svc.withGatewayProfitControlGate(gatewayProfitTestContext(group), &groupID)
			gate, _ := tokenCtx.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
			require.NotNil(t, gate)
			require.Equal(t, platform, gate.platform)
			require.InDelta(t, 0.5, gate.threshold, 1e-12)

			metadataCtx := context.WithValue(context.Background(), ctxkey.Group, group)
			metadataCtx = svc.withGatewayProfitControlGate(metadataCtx, &groupID)
			gate, _ = metadataCtx.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
			require.Nil(t, gate, "未显式标记为 token 请求的入口不得装门")
		})
	}
}

func TestGatewayProfitControlCompositeBillingUsesScheduledMemberConfig(t *testing.T) {
	billingGroup := &Group{
		ID:               201,
		Platform:         PlatformComposite,
		Status:           StatusActive,
		Hydrated:         true,
		RateMultiplier:   0.4,
		SubscriptionType: SubscriptionTypeStandard,
	}
	memberGroup := gatewayProfitTestGroup(202, PlatformAnthropic)
	memberGroup.RateMultiplier = 99
	memberGroup.ProfitMinMargin = 0.25

	ctx := context.WithValue(context.Background(), ctxkey.Group, billingGroup)
	ctx, pricingAt := WithGatewayTokenRequestPricing(ctx)
	svc := &GatewayService{
		schedulerSnapshot: NewSchedulerSnapshotService(
			nil,
			nil,
			nil,
			profitControlGroupRepo{group: memberGroup},
			nil,
		),
	}
	ctx = svc.withGatewayProfitControlGate(ctx, &memberGroup.ID)
	gate, _ := ctx.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
	require.NotNil(t, gate)
	require.Equal(t, memberGroup.ID, gate.groupID)
	require.Equal(t, PlatformAnthropic, gate.platform)
	require.Equal(t, pricingAt, gate.pricingAt)
	require.InDelta(t, 0.4*(1-0.25), gate.threshold, 1e-12, "D 必须取 composite 计费父分组，margin 取被调度成员分组")
}

func TestGatewayProfitControlGroupLoadFailureClearsForeignGate(t *testing.T) {
	billingGroup := &Group{
		ID:               211,
		Platform:         PlatformComposite,
		Status:           StatusActive,
		Hydrated:         true,
		RateMultiplier:   0.4,
		SubscriptionType: SubscriptionTypeStandard,
	}
	targetGroupID := int64(212)
	ctx := gatewayProfitTestContext(billingGroup)
	ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{
		groupID:   210,
		platform:  PlatformAnthropic,
		threshold: 0.1,
	})
	svc := &GatewayService{
		schedulerSnapshot: NewSchedulerSnapshotService(
			nil,
			nil,
			nil,
			profitControlFailingGroupRepo{},
			nil,
		),
	}

	ctx = svc.withGatewayProfitControlGate(ctx, &targetGroupID)
	gate, ok := ctx.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
	require.True(t, ok)
	require.Nil(t, gate, "加载新分组失败时必须清除其他分组遗留的门")

	account := gatewayProfitTestAccount(213, PlatformAnthropic, 0.8, targetGroupID)
	require.True(t, svc.isGatewayAccountProfitEligible(ctx, &account), "配置读取失败按既定语义 fail-open")
}

type profitControlFailingGroupRepo struct {
	GroupRepository
}

func (profitControlFailingGroupRepo) GetByIDLite(context.Context, int64) (*Group, error) {
	return nil, errors.New("group cache unavailable")
}

// 见 profitControlGroupRepo.GetByID：利润门必须走不带账号计数聚合的 lite 读取。
func (profitControlFailingGroupRepo) GetByID(context.Context, int64) (*Group, error) {
	panic("profit control gate must read groups via GetByIDLite (no account-count aggregation)")
}

func TestGatewayProfitControlLegacyMixedAndRoutedSelection(t *testing.T) {
	t.Run("legacy single-platform selection", func(t *testing.T) {
		group := gatewayProfitTestGroup(111, PlatformGrok)
		cheap := gatewayProfitTestAccount(1, PlatformGrok, 0.2, group.ID)
		expensive := gatewayProfitTestAccount(2, PlatformGrok, 0.8, group.ID)
		repo := &mockAccountRepoForPlatform{
			accounts:     []Account{expensive, cheap},
			accountsByID: map[int64]*Account{cheap.ID: &cheap, expensive.ID: &expensive},
		}
		svc := &GatewayService{
			accountRepo: repo,
			cache:       &mockGatewayCacheForPlatform{},
			cfg:         testConfig(),
		}

		selected, err := svc.SelectAccountForModelWithExclusions(
			gatewayProfitTestContext(group), &group.ID, "", "", nil,
		)
		require.NoError(t, err)
		require.Equal(t, cheap.ID, selected.ID)

		_, err = svc.SelectAccountForModelWithExclusions(
			gatewayProfitTestContext(group), &group.ID, "", "", map[int64]struct{}{cheap.ID: {}},
		)
		require.Error(t, err)
		require.ErrorIs(t, err, ErrNoAvailableAccounts)
	})

	t.Run("mixed routing filters the routed account", func(t *testing.T) {
		group := gatewayProfitTestGroup(112, PlatformAnthropic)
		group.ModelRoutingEnabled = true
		group.ModelRouting = map[string][]int64{"claude-test": {2, 1}}
		cheap := gatewayProfitTestAccount(1, PlatformAntigravity, 0.2, group.ID)
		cheap.Extra = map[string]any{"mixed_scheduling": true}
		cheap.Credentials = map[string]any{"model_mapping": map[string]any{"claude-test": "claude-test"}}
		expensive := gatewayProfitTestAccount(2, PlatformAnthropic, 0.8, group.ID)
		repo := &mockAccountRepoForPlatform{
			accounts:     []Account{expensive, cheap},
			accountsByID: map[int64]*Account{cheap.ID: &cheap, expensive.ID: &expensive},
		}
		svc := &GatewayService{
			accountRepo: repo,
			cache:       &mockGatewayCacheForPlatform{},
			cfg:         testConfig(),
		}

		selected, err := svc.SelectAccountForModelWithExclusions(
			gatewayProfitTestContext(group), &group.ID, "", "claude-test", nil,
		)
		require.NoError(t, err)
		require.Equal(t, cheap.ID, selected.ID)
	})
}

func TestGatewayProfitControlLoadAwareSelectionAndFailover(t *testing.T) {
	group := gatewayProfitTestGroup(121, PlatformGrok)
	cheap := gatewayProfitTestAccount(1, PlatformGrok, 0.2, group.ID)
	expensive := gatewayProfitTestAccount(2, PlatformGrok, 0.8, group.ID)
	repo := &mockAccountRepoForPlatform{
		accounts:     []Account{expensive, cheap},
		accountsByID: map[int64]*Account{cheap.ID: &cheap, expensive.ID: &expensive},
	}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	svc := &GatewayService{
		accountRepo:        repo,
		cache:              &mockGatewayCacheForPlatform{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	result, err := svc.SelectAccountWithLoadAwareness(
		gatewayProfitTestContext(group), &group.ID, "", "", nil, "", 0,
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, cheap.ID, result.Account.ID)
	if result.ReleaseFunc != nil {
		result.ReleaseFunc()
	}

	result, err = svc.SelectAccountWithLoadAwareness(
		gatewayProfitTestContext(group),
		&group.ID,
		"",
		"",
		map[int64]struct{}{cheap.ID: {}},
		"",
		0,
	)
	require.Nil(t, result)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
}

func TestGatewayProfitControlStickyVetoKeepsBindingUntilRateRecovers(t *testing.T) {
	group := gatewayProfitTestGroup(131, PlatformAnthropic)
	expensive := gatewayProfitTestAccount(1, PlatformAnthropic, 0.8, group.ID)
	cheap := gatewayProfitTestAccount(2, PlatformAnthropic, 0.2, group.ID)
	repo := &mockAccountRepoForPlatform{
		accounts:     []Account{expensive, cheap},
		accountsByID: map[int64]*Account{expensive.ID: &expensive, cheap.ID: &cheap},
	}
	cache := &mockGatewayCacheForPlatform{
		sessionBindings: map[string]int64{"sticky-profit": expensive.ID},
	}
	svc := &GatewayService{
		accountRepo: repo,
		cache:       cache,
		cfg:         testConfig(),
	}
	ctx := gatewayProfitTestContext(group)

	selected, err := svc.SelectAccountForModelWithExclusions(ctx, &group.ID, "sticky-profit", "", nil)
	require.NoError(t, err)
	require.Equal(t, cheap.ID, selected.ID)
	require.Equal(t, expensive.ID, cache.sessionBindings["sticky-profit"], "候选过滤不得覆盖旧粘性绑定")

	require.NoError(t, svc.BindStickySessionAfterProfitAdmission(
		svc.withGatewayProfitControlGate(ctx, &group.ID),
		&group.ID,
		"sticky-profit",
		cheap.ID,
	))
	require.Equal(t, expensive.ID, cache.sessionBindings["sticky-profit"], "终检通过的 fallback 账号也不得覆盖旧绑定")
	require.Zero(t, cache.deletedSessions["sticky-profit"])

	recovered := expensive
	recoveredRate := 0.2
	recovered.RateMultiplier = &recoveredRate
	repo.accounts[0] = recovered
	repo.accountsByID[recovered.ID] = &repo.accounts[0]

	selected, err = svc.SelectAccountForModelWithExclusions(ctx, &group.ID, "sticky-profit", "", nil)
	require.NoError(t, err)
	require.Equal(t, recovered.ID, selected.ID, "倍率恢复后应重新命中原粘性账号")
	require.Zero(t, cache.deletedSessions["sticky-profit"])
}

type gatewayProfitSnapshotCache struct {
	SchedulerCache
	account *Account
	err     error
}

func (c *gatewayProfitSnapshotCache) GetAccount(context.Context, int64) (*Account, error) {
	return c.account, c.err
}

type gatewayProfitAccountRepo struct {
	AccountRepository
	account *Account
	err     error
}

func (r gatewayProfitAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, r.err
}

func TestGatewayProfitControlTerminalRefreshUsesReplacementObject(t *testing.T) {
	selected := gatewayProfitTestAccount(141, PlatformGemini, 0.2, 1)
	replacement := selected
	expensiveRate := 0.8
	replacement.RateMultiplier = &expensiveRate

	snapshot := NewSchedulerSnapshotService(
		&gatewayProfitSnapshotCache{account: &replacement},
		nil,
		gatewayProfitAccountRepo{},
		nil,
		nil,
	)
	ctx := context.WithValue(context.Background(), openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{
		groupID:   1,
		platform:  PlatformGemini,
		threshold: 0.5,
	})

	latest, vetoed, reason := profitControlVetoLatest(ctx, &selected, snapshot)
	require.Same(t, &replacement, latest)
	require.True(t, vetoed)
	require.Equal(t, openAIProfitFilterReasonThreshold, reason)
	require.InDelta(t, 0.2, *selected.RateMultiplier, 1e-12, "测试必须替换缓存对象，不能原地修改旧指针")
}

func TestGatewayProfitControlTerminalRefreshFallsBackFromCacheToDatabase(t *testing.T) {
	selected := gatewayProfitTestAccount(145, PlatformAnthropic, 0.2, 1)
	replacement := selected
	expensiveRate := 0.8
	replacement.RateMultiplier = &expensiveRate

	snapshot := NewSchedulerSnapshotService(
		&gatewayProfitSnapshotCache{err: errors.New("cache unavailable")},
		nil,
		gatewayProfitAccountRepo{account: &replacement},
		nil,
		nil,
	)
	ctx := context.WithValue(context.Background(), openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{
		groupID:   1,
		platform:  PlatformAnthropic,
		threshold: 0.5,
	})

	latest, vetoed, reason := profitControlVetoLatest(ctx, &selected, snapshot)
	require.Same(t, &replacement, latest)
	require.True(t, vetoed, "缓存读取失败时必须继续从数据库重读，不能直接使用选号旧对象")
	require.Equal(t, openAIProfitFilterReasonThreshold, reason)
}

func TestGatewayProfitControlTerminalRefreshFailureFallsBackToSelectedObject(t *testing.T) {
	selected := gatewayProfitTestAccount(151, PlatformAntigravity, 0.2, 1)
	snapshot := NewSchedulerSnapshotService(
		&gatewayProfitSnapshotCache{err: errors.New("cache unavailable")},
		nil,
		gatewayProfitAccountRepo{err: errors.New("database unavailable")},
		nil,
		nil,
	)
	ctx := context.WithValue(context.Background(), openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{
		groupID:   1,
		platform:  PlatformAntigravity,
		threshold: 0.5,
	})

	latest, vetoed, reason := profitControlVetoLatest(ctx, &selected, snapshot)
	require.Same(t, &selected, latest)
	require.False(t, vetoed)
	require.Empty(t, reason)
}

// 选号结果携带门：门安装在调度栈局部 ctx 上，handler 必须经
// ContextWithSelectionProfitGate 重放后终检与准入后绑定才可见（评审修复回归）。
func TestGatewayProfitControlSelectionCarriesGateToHandlerContext(t *testing.T) {
	group := gatewayProfitTestGroup(1, PlatformAnthropic)
	svc := &GatewayService{}
	expensive := gatewayProfitTestAccount(161, PlatformAnthropic, 0.9, group.ID)

	gateCtx := svc.withGatewayProfitControlGate(gatewayProfitTestContext(group), &group.ID)
	selection, err := svc.newSelectionResult(gateCtx, &expensive, true, nil, nil)
	require.NoError(t, err)
	require.True(t, selection.ProfitGateActive(), "选号结果必须携带调度栈内生效的门")

	// 修复前的缺陷形态：handler 原始 ctx 不含门，终检退化为空操作。
	_, vetoed, _ := svc.GatewayProfitControlVetoLatest(context.Background(), &expensive)
	require.False(t, vetoed, "对照组：不重放门时终检确实看不到门")

	handlerCtx := ContextWithSelectionProfitGate(context.Background(), selection)
	latest, vetoed, reason := svc.GatewayProfitControlVetoLatest(handlerCtx, &expensive)
	require.True(t, vetoed, "重放门后终检必须真实生效")
	require.Equal(t, openAIProfitFilterReasonThreshold, reason)
	require.NotNil(t, latest)

	// 无门选号不携带门，重放为无操作。
	plain, err := svc.newSelectionResult(context.Background(), &expensive, true, nil, nil)
	require.NoError(t, err)
	require.False(t, plain.ProfitGateActive())
	require.Equal(t, context.Background(), ContextWithSelectionProfitGate(context.Background(), plain))
}

// 生图意图不关门（H1/H2 回归锚点）：/v1/responses 混合请求即使带生图声明，
// token 定价上下文照常装配，共享门照常安装并否决越线账号。
func TestGatewayProfitControlImageIntentDoesNotDisableGate(t *testing.T) {
	group := gatewayProfitTestGroup(2, PlatformAnthropic)
	svc := &GatewayService{}
	expensive := gatewayProfitTestAccount(162, PlatformAnthropic, 0.9, group.ID)

	ctx := gatewayProfitTestContext(group)
	ctx = WithOpenAIImageGenerationIntent(ctx)
	gateCtx := svc.withGatewayProfitControlGate(ctx, &group.ID)
	require.False(t, svc.isGatewayAccountProfitEligible(gateCtx, &expensive),
		"请求体里的生图声明（含被动 image_gen namespace）不得关闭利润门")
}

// 无门时准入后绑定回退官方 eager 语义；门下读失败保守不写（评审 M-Bind 回归）。
func TestGatewayProfitControlAfterAdmissionBindSemantics(t *testing.T) {
	groupID := int64(3)
	expensiveID := int64(171)
	cheapID := int64(172)

	t.Run("eager without gate", func(t *testing.T) {
		cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"s": expensiveID}}
		svc := &GatewayService{cache: cache}
		require.NoError(t, svc.BindStickySessionAfterProfitAdmission(context.Background(), &groupID, "s", cheapID))
		require.Equal(t, cheapID, cache.sessionBindings["s"], "无门时保持既有 eager 绑定行为")
	})

	t.Run("gated read failure is conservative", func(t *testing.T) {
		// mock 的 miss 返回非 sentinel 错误，等价于 Redis 读失败：门下保守不写。
		cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{}}
		svc := &GatewayService{cache: cache}
		gate := &openAIProfitControlGate{groupID: groupID, platform: PlatformAnthropic, threshold: 0.5}
		gateCtx := context.WithValue(context.Background(), openAIProfitControlGateCtxKey{}, gate)
		require.NoError(t, svc.BindStickySessionAfterProfitAdmission(gateCtx, &groupID, "absent", cheapID))
		require.NotContains(t, cache.sessionBindings, "absent")
	})

	t.Run("gated sentinel miss binds", func(t *testing.T) {
		cache := &sentinelMissGatewayCache{mockGatewayCacheForPlatform: &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{}}}
		svc := &GatewayService{cache: cache}
		gate := &openAIProfitControlGate{groupID: groupID, platform: PlatformAnthropic, threshold: 0.5}
		gateCtx := context.WithValue(context.Background(), openAIProfitControlGateCtxKey{}, gate)
		require.NoError(t, svc.BindStickySessionAfterProfitAdmission(gateCtx, &groupID, "fresh", cheapID))
		require.Equal(t, cheapID, cache.sessionBindings["fresh"], "门下无既有绑定（sentinel miss）应建立粘性")
	})
}

// sentinelMissGatewayCache 让 miss 返回与真实仓库一致的 ErrStickySessionNotFound。
type sentinelMissGatewayCache struct {
	*mockGatewayCacheForPlatform
}

func (c *sentinelMissGatewayCache) GetSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	if id, ok := c.sessionBindings[sessionHash]; ok {
		return id, nil
	}
	return 0, ErrStickySessionNotFound
}

// ---- merged from gateway_reasoning_pricing_test.go ----
func TestRecordUsage_ReasoningPricingUsesForwardedEffort(t *testing.T) {
	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI} {
		t.Run(platform, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			subRepo := &openAIRecordUsageSubRepoStub{}
			groupID := int64(77)
			inputPrice, outputPrice := 0.001, 0.002
			requested, forwarded := "max", "high"
			model := "gpt-5.4"
			if platform == PlatformAnthropic {
				model = "claude-fable-5-1"
			}
			apiKey := &APIKey{
				ID: 10, GroupID: &groupID,
				Group: &Group{
					ID: groupID, Platform: platform, Status: StatusActive, Hydrated: true, RateMultiplier: 0.5,
					ModelPricing: []ChannelModelPricing{{
						Models: []string{model}, BillingMode: BillingModeToken,
						InputPrice: &inputPrice, OutputPrice: &outputPrice,
						ReasoningEffortMultipliers: map[string]float64{"high": 1.5, "max": 3},
					}},
				},
			}
			account := &Account{ID: 30, Platform: platform, Type: AccountTypeAPIKey}
			user := &User{ID: 20}
			if platform == PlatformAnthropic {
				svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)
				svc.resolver = NewModelPricingResolver(nil, svc.billingService)
				require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
					Result: &ForwardResult{
						RequestID: "reasoning_pricing", Model: model, Duration: time.Second,
						Usage:           ClaudeUsage{InputTokens: 100, OutputTokens: 50},
						ReasoningEffort: &forwarded, RequestedReasoningEffort: &requested,
					},
					APIKey: apiKey, User: user, Account: account,
				}))
			} else {
				svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
				svc.resolver = NewModelPricingResolver(nil, svc.billingService)
				require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
					Result: &OpenAIForwardResult{
						RequestID: "reasoning_pricing", Model: model, Duration: time.Second,
						Usage:           OpenAIUsage{InputTokens: 100, OutputTokens: 50},
						ReasoningEffort: &forwarded, RequestedReasoningEffort: &requested,
					},
					APIKey: apiKey, User: user, Account: account,
				}))
			}
			require.NotNil(t, usageRepo.lastLog)
			require.InDelta(t, 0.3, usageRepo.lastLog.TotalCost, 1e-12)
			require.InDelta(t, 0.15, usageRepo.lastLog.ActualCost, 1e-12)
			require.Equal(t, forwarded, *usageRepo.lastLog.ReasoningEffort)
			require.Equal(t, requested, *usageRepo.lastLog.RequestedReasoningEffort)
			require.InDelta(t, 0.15, userRepo.lastAmount, 1e-12)
		})
	}
}

// ---- merged from gateway_service_antigravity_whitelist_test.go ----
func TestGatewayService_isModelSupportedByAccount_AntigravityModelMapping(t *testing.T) {
	svc := &GatewayService{}

	// 使用 model_mapping 作为白名单（通配符匹配）
	account := &Account{
		Platform: PlatformAntigravity,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-*":   "claude-sonnet-4-5",
				"gemini-3-*": "gemini-3-flash",
			},
		},
	}

	// claude-* 通配符匹配
	require.True(t, svc.isModelSupportedByAccount(account, "claude-sonnet-4-5"))
	require.True(t, svc.isModelSupportedByAccount(account, "claude-haiku-4-5"))
	require.True(t, svc.isModelSupportedByAccount(account, "claude-opus-4-6"))

	// gemini-3-* 通配符匹配
	require.True(t, svc.isModelSupportedByAccount(account, "gemini-3-flash"))
	require.True(t, svc.isModelSupportedByAccount(account, "gemini-3-pro-high"))

	// gemini-2.5-* 不匹配（不在 model_mapping 中）
	require.False(t, svc.isModelSupportedByAccount(account, "gemini-2.5-flash"))
	require.False(t, svc.isModelSupportedByAccount(account, "gemini-2.5-pro"))

	// 其他平台模型不支持
	require.False(t, svc.isModelSupportedByAccount(account, "gpt-4"))

	// 空模型允许
	require.True(t, svc.isModelSupportedByAccount(account, ""))
}

func TestGatewayService_isModelSupportedByAccount_AntigravityNoMapping(t *testing.T) {
	svc := &GatewayService{}

	// 未配置 model_mapping 时，使用默认映射（domain.DefaultAntigravityModelMapping）
	// 只有默认映射中的模型才被支持
	account := &Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{},
	}

	// 默认映射中的模型应该被支持
	require.True(t, svc.isModelSupportedByAccount(account, "claude-sonnet-4-5"))
	require.True(t, svc.isModelSupportedByAccount(account, "gemini-3-flash"))
	require.True(t, svc.isModelSupportedByAccount(account, "gemini-2.5-pro"))
	require.True(t, svc.isModelSupportedByAccount(account, "claude-haiku-4-5"))

	// 不在默认映射中的模型不被支持
	require.False(t, svc.isModelSupportedByAccount(account, "claude-3-5-sonnet-20241022"))
	require.False(t, svc.isModelSupportedByAccount(account, "claude-unknown-model"))

	// 非 claude-/gemini- 前缀仍然不支持
	require.False(t, svc.isModelSupportedByAccount(account, "gpt-4"))
}

// TestGatewayService_isModelSupportedByAccountWithContext_ThinkingMode 测试 thinking 模式下的模型支持检查
// 验证调度时使用映射后的最终模型名（包括 thinking 后缀）来检查 model_mapping 支持
func TestGatewayService_isModelSupportedByAccountWithContext_ThinkingMode(t *testing.T) {
	svc := &GatewayService{}

	tests := []struct {
		name            string
		modelMapping    map[string]any
		requestedModel  string
		thinkingEnabled bool
		expected        bool
	}{
		// 场景 1: 只配置 claude-sonnet-4-5-thinking，请求 claude-sonnet-4-5 + thinking=true
		// mapAntigravityModel 找不到 claude-sonnet-4-5 的映射 → 返回 false
		{
			name: "thinking_enabled_no_base_mapping_returns_false",
			modelMapping: map[string]any{
				"claude-sonnet-4-5-thinking": "claude-sonnet-4-5-thinking",
			},
			requestedModel:  "claude-sonnet-4-5",
			thinkingEnabled: true,
			expected:        false,
		},
		// 场景 2: 只配置 claude-sonnet-4-5-thinking，请求 claude-sonnet-4-5 + thinking=false
		// mapAntigravityModel 找不到 claude-sonnet-4-5 的映射 → 返回 false
		{
			name: "thinking_disabled_no_base_mapping_returns_false",
			modelMapping: map[string]any{
				"claude-sonnet-4-5-thinking": "claude-sonnet-4-5-thinking",
			},
			requestedModel:  "claude-sonnet-4-5",
			thinkingEnabled: false,
			expected:        false,
		},
		// 场景 3: 配置 claude-sonnet-4-5（非 thinking），请求 claude-sonnet-4-5 + thinking=true
		// 最终模型名 = claude-sonnet-4-5-thinking，不在 mapping 中，应该不匹配
		{
			name: "thinking_enabled_no_match_non_thinking_mapping",
			modelMapping: map[string]any{
				"claude-sonnet-4-5": "claude-sonnet-4-5",
			},
			requestedModel:  "claude-sonnet-4-5",
			thinkingEnabled: true,
			expected:        false,
		},
		// 场景 4: 配置两种模型，请求 claude-sonnet-4-5 + thinking=true，应该匹配 thinking 版本
		{
			name: "both_models_thinking_enabled_matches_thinking",
			modelMapping: map[string]any{
				"claude-sonnet-4-5":          "claude-sonnet-4-5",
				"claude-sonnet-4-5-thinking": "claude-sonnet-4-5-thinking",
			},
			requestedModel:  "claude-sonnet-4-5",
			thinkingEnabled: true,
			expected:        true,
		},
		// 场景 5: 配置两种模型，请求 claude-sonnet-4-5 + thinking=false，应该匹配非 thinking 版本
		{
			name: "both_models_thinking_disabled_matches_non_thinking",
			modelMapping: map[string]any{
				"claude-sonnet-4-5":          "claude-sonnet-4-5",
				"claude-sonnet-4-5-thinking": "claude-sonnet-4-5-thinking",
			},
			requestedModel:  "claude-sonnet-4-5",
			thinkingEnabled: false,
			expected:        true,
		},
		// 场景 6: 通配符 claude-* 应该同时匹配 thinking 和非 thinking
		{
			name: "wildcard_matches_thinking",
			modelMapping: map[string]any{
				"claude-*": "claude-sonnet-4-5",
			},
			requestedModel:  "claude-sonnet-4-5",
			thinkingEnabled: true,
			expected:        true, // claude-sonnet-4-5-thinking 匹配 claude-*
		},
		// 场景 7: 只配置 thinking 变体但没有基础模型映射 → 返回 false
		// mapAntigravityModel 找不到 claude-opus-4-6 的映射
		{
			name: "opus_thinking_no_base_mapping_returns_false",
			modelMapping: map[string]any{
				"claude-opus-4-6-thinking": "claude-opus-4-6-thinking",
			},
			requestedModel:  "claude-opus-4-6",
			thinkingEnabled: true,
			expected:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{
				Platform: PlatformAntigravity,
				Credentials: map[string]any{
					"model_mapping": tt.modelMapping,
				},
			}

			ctx := context.WithValue(context.Background(), ctxkey.ThinkingEnabled, tt.thinkingEnabled)
			result := svc.isModelSupportedByAccountWithContext(ctx, account, tt.requestedModel)

			require.Equal(t, tt.expected, result,
				"isModelSupportedByAccountWithContext(ctx[thinking=%v], account, %q) = %v, want %v",
				tt.thinkingEnabled, tt.requestedModel, result, tt.expected)
		})
	}
}

// TestGatewayService_isModelSupportedByAccount_CustomMappingNotInDefault 测试自定义模型映射中
// 不在 DefaultAntigravityModelMapping 中的模型能通过调度
func TestGatewayService_isModelSupportedByAccount_CustomMappingNotInDefault(t *testing.T) {
	svc := &GatewayService{}

	// 自定义映射中包含不在默认映射中的模型
	account := &Account{
		Platform: PlatformAntigravity,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"my-custom-model":   "actual-upstream-model",
				"gpt-4o":            "some-upstream-model",
				"llama-3-70b":       "llama-3-70b-upstream",
				"claude-sonnet-4-5": "claude-sonnet-4-5",
			},
		},
	}

	// 自定义模型应该通过（不在 DefaultAntigravityModelMapping 中也可以）
	require.True(t, svc.isModelSupportedByAccount(account, "my-custom-model"))
	require.True(t, svc.isModelSupportedByAccount(account, "gpt-4o"))
	require.True(t, svc.isModelSupportedByAccount(account, "llama-3-70b"))
	require.True(t, svc.isModelSupportedByAccount(account, "claude-sonnet-4-5"))

	// 不在自定义映射中的模型不通过
	require.False(t, svc.isModelSupportedByAccount(account, "gpt-3.5-turbo"))
	require.False(t, svc.isModelSupportedByAccount(account, "unknown-model"))

	// 空模型允许
	require.True(t, svc.isModelSupportedByAccount(account, ""))
}

// TestGatewayService_isModelSupportedByAccountWithContext_CustomMappingThinking
// 测试自定义映射 + thinking 模式的交互
func TestGatewayService_isModelSupportedByAccountWithContext_CustomMappingThinking(t *testing.T) {
	svc := &GatewayService{}

	// 自定义映射同时配置基础模型和 thinking 变体
	account := &Account{
		Platform: PlatformAntigravity,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-sonnet-4-5":          "claude-sonnet-4-5",
				"claude-sonnet-4-5-thinking": "claude-sonnet-4-5-thinking",
				"my-custom-model":            "upstream-model",
			},
		},
	}

	// thinking=true: claude-sonnet-4-5 → mapped=claude-sonnet-4-5 → +thinking → check IsModelSupported(claude-sonnet-4-5-thinking)=true
	ctx := context.WithValue(context.Background(), ctxkey.ThinkingEnabled, true)
	require.True(t, svc.isModelSupportedByAccountWithContext(ctx, account, "claude-sonnet-4-5"))

	// thinking=false: claude-sonnet-4-5 → mapped=claude-sonnet-4-5 → check IsModelSupported(claude-sonnet-4-5)=true
	ctx = context.WithValue(context.Background(), ctxkey.ThinkingEnabled, false)
	require.True(t, svc.isModelSupportedByAccountWithContext(ctx, account, "claude-sonnet-4-5"))

	// 自定义模型（非 claude）不受 thinking 后缀影响，mapped 成功即通过
	ctx = context.WithValue(context.Background(), ctxkey.ThinkingEnabled, true)
	require.True(t, svc.isModelSupportedByAccountWithContext(ctx, account, "my-custom-model"))
}

// ---- merged from gateway_service_subscription_billing_test.go ----
// TestBuildUsageBillingCommand_SubscriptionAppliesRateMultiplier locks in the fix
// that subscription-mode billing honours the group (and any user-specific) rate
// multiplier — i.e. cmd.SubscriptionCost tracks ActualCost (= TotalCost *
// RateMultiplier), not raw TotalCost.
func TestBuildUsageBillingCommand_SubscriptionAppliesRateMultiplier(t *testing.T) {
	t.Parallel()

	groupID := int64(7)
	subID := int64(42)

	tests := []struct {
		name           string
		totalCost      float64
		actualCost     float64
		isSubscription bool
		wantSub        float64
		wantBalance    float64
	}{
		{
			name:           "subscription with 2x multiplier consumes 2x quota",
			totalCost:      1.0,
			actualCost:     2.0,
			isSubscription: true,
			wantSub:        2.0,
			wantBalance:    0,
		},
		{
			name:           "subscription with 0.5x multiplier consumes 0.5x quota",
			totalCost:      1.0,
			actualCost:     0.5,
			isSubscription: true,
			wantSub:        0.5,
			wantBalance:    0,
		},
		{
			name:           "free subscription (multiplier 0) consumes no quota",
			totalCost:      1.0,
			actualCost:     0,
			isSubscription: true,
			wantSub:        0,
			wantBalance:    0,
		},
		{
			name:           "balance billing keeps using ActualCost (regression)",
			totalCost:      1.0,
			actualCost:     2.0,
			isSubscription: false,
			wantSub:        0,
			wantBalance:    2.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &postUsageBillingParams{
				Cost:               &CostBreakdown{TotalCost: tt.totalCost, ActualCost: tt.actualCost},
				User:               &User{ID: 1},
				APIKey:             &APIKey{ID: 2, GroupID: &groupID},
				Account:            &Account{ID: 3},
				Subscription:       &UserSubscription{ID: subID},
				IsSubscriptionBill: tt.isSubscription,
			}

			cmd := buildUsageBillingCommand("req-1", nil, p)
			if cmd == nil {
				t.Fatal("buildUsageBillingCommand returned nil")
			}
			if cmd.SubscriptionCost != tt.wantSub {
				t.Errorf("SubscriptionCost = %v, want %v", cmd.SubscriptionCost, tt.wantSub)
			}
			if cmd.BalanceCost != tt.wantBalance {
				t.Errorf("BalanceCost = %v, want %v", cmd.BalanceCost, tt.wantBalance)
			}
		})
	}
}

// ---- merged from gateway_simple_mode_record_usage_test.go ----
func TestSimpleModeRecordUsageWindowOptIn(t *testing.T) {
	for _, openAI := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("openai=%v/enabled=%v/stream=%v", openAI, enabled, stream), func(t *testing.T) {
					logs := &openAIRecordUsageLogRepoStub{inserted: true}
					billing := &openAIRecordUsageBillingRepoStub{}
					users := &openAIRecordUsageUserRepoStub{}
					subs := &openAIRecordUsageSubRepoStub{}
					key := &APIKey{ID: 1, Quota: 100, RateLimit5h: 30, Group: &Group{RateMultiplier: 1}}
					user := &User{ID: 2, Balance: 0}
					account := &Account{ID: 3, Type: AccountTypeAPIKey}
					if openAI {
						svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, users, subs, nil)
						svc.cfg.RunMode = config.RunModeSimple
						svc.cfg.SimpleModeKeyRateLimitEnabled = enabled
						err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
							Result: &OpenAIForwardResult{RequestID: "simple-request", Model: "gpt-5.1", Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 20}, Stream: stream},
							APIKey: key, User: user, Account: account,
						})
						require.NoError(t, err)
					} else {
						svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logs, billing, users, subs)
						svc.cfg.RunMode = config.RunModeSimple
						svc.cfg.SimpleModeKeyRateLimitEnabled = enabled
						err := svc.RecordUsage(context.Background(), &RecordUsageInput{
							Result: &ForwardResult{RequestID: "simple-request", Model: "claude-sonnet-4", Usage: ClaudeUsage{InputTokens: 100, OutputTokens: 20}, Stream: stream},
							APIKey: key, User: user, Account: account,
						})
						require.NoError(t, err)
					}
					require.Equal(t, 1, logs.calls)
					require.Positive(t, logs.lastLog.ActualCost)
					require.Zero(t, users.deductCalls)
					require.Zero(t, subs.incrementCalls)
					if !enabled {
						require.Zero(t, billing.calls)
						return
					}
					require.Equal(t, 1, billing.calls)
					require.InDelta(t, logs.lastLog.ActualCost, billing.lastCmd.APIKeyRateLimitCost, 1e-12)
					require.Zero(t, billing.lastCmd.BalanceCost)
					require.Zero(t, billing.lastCmd.SubscriptionCost)
					require.Zero(t, billing.lastCmd.APIKeyQuotaCost)
					require.Zero(t, billing.lastCmd.AccountQuotaCost)
				})
			}
		}
	}
}

// ---- merged from gateway_soonest_reset_test.go ----
func accWithWindowEnd(id int64, end *time.Time) accountWithLoad {
	return accountWithLoad{
		account: &Account{
			ID:               id,
			Schedulable:      true,
			Status:           StatusActive,
			SessionWindowEnd: end,
		},
		loadInfo: &AccountLoadInfo{AccountID: id},
	}
}

func TestFilterBySoonestReset_PicksSoonestFutureWindow(t *testing.T) {
	now := time.Now()
	soon := now.Add(1 * time.Hour)
	later := now.Add(24 * time.Hour)
	accounts := []accountWithLoad{
		accWithWindowEnd(1, testTimePtr(later)),
		accWithWindowEnd(2, testTimePtr(soon)),
		accWithWindowEnd(3, testTimePtr(later)),
	}
	got := filterBySoonestReset(accounts)
	require.Len(t, got, 1)
	require.Equal(t, int64(2), got[0].account.ID, "重置时间最早的账号被选中")
}

func TestFilterBySoonestReset_IgnoresNilAndExpiredWindows(t *testing.T) {
	now := time.Now()
	expired := now.Add(-1 * time.Hour)
	active := now.Add(2 * time.Hour)
	accounts := []accountWithLoad{
		accWithWindowEnd(1, nil),                  // 无活跃窗口
		accWithWindowEnd(2, testTimePtr(expired)), // 已过期，视为无活跃窗口
		accWithWindowEnd(3, testTimePtr(active)),  // 唯一活跃窗口
	}
	got := filterBySoonestReset(accounts)
	require.Len(t, got, 1)
	require.Equal(t, int64(3), got[0].account.ID, "仅保留拥有未来重置时间的账号")
}

func TestFilterBySoonestReset_NoActiveWindowReturnsAll(t *testing.T) {
	now := time.Now()
	expired := now.Add(-30 * time.Minute)
	accounts := []accountWithLoad{
		accWithWindowEnd(1, nil),
		accWithWindowEnd(2, testTimePtr(expired)),
	}
	got := filterBySoonestReset(accounts)
	require.Len(t, got, 2, "没有任何账号拥有活跃窗口时，返回原集合不做过滤")
}

func TestFilterBySoonestReset_TiedSoonestKeepsAll(t *testing.T) {
	now := time.Now()
	end := now.Add(90 * time.Minute)
	accounts := []accountWithLoad{
		accWithWindowEnd(1, testTimePtr(end)),
		accWithWindowEnd(2, testTimePtr(end)),
		accWithWindowEnd(3, testTimePtr(now.Add(5*time.Hour))),
	}
	got := filterBySoonestReset(accounts)
	require.Len(t, got, 2, "并列最早重置的账号都保留，交由后续 LRU 决定")
	ids := map[int64]bool{got[0].account.ID: true, got[1].account.ID: true}
	require.True(t, ids[1] && ids[2])
}

func TestFilterBySoonestReset_SingleOrEmptyUnchanged(t *testing.T) {
	require.Empty(t, filterBySoonestReset(nil))
	single := []accountWithLoad{accWithWindowEnd(1, nil)}
	require.Len(t, filterBySoonestReset(single), 1)
}

// ---- merged from gateway_structured_outputs_beta_test.go ----
func TestStructuredOutputsBetaOAuthMimic(t *testing.T) {
	const beta = "structured-outputs-2025-11-13"
	for _, tc := range []struct {
		name   string
		header string
		drop   map[string]struct{}
		want   bool
	}{
		{"explicit", beta, nil, true},
		{"mixed and duplicate", "custom-beta, " + beta + "," + beta, nil, true},
		{"absent", "custom-beta", nil, false},
		{"similar token", beta + "-other", nil, false},
		{"filtered", beta, map[string]struct{}{beta: {}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestGatewayServiceForBeta(false)
			header := http.Header{}
			header.Set("anthropic-beta", tc.header)
			body := []byte(`{"output_format":{"type":"json_schema","schema":{"type":"object"}}}`)
			got, set := s.computeFinalAnthropicBeta("oauth", true, "claude-sonnet-5", header, body, tc.drop)
			require.True(t, set)
			require.Equal(t, tc.want, containsBetaToken(got, beta))
			require.False(t, containsBetaToken(got, "custom-beta"))
			require.True(t, containsBetaToken(got, "oauth-2025-04-20"))
			if tc.want {
				require.Equal(t, 1, strings.Count(got, beta))
			}
		})
	}
}

func TestBuildUpstreamRequestStructuredOutputsBeta(t *testing.T) {
	const beta = "structured-outputs-2025-11-13"
	for _, filtered := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserved", true: "policy filtered"}[filtered], func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("anthropic-beta", beta+",custom-beta")
			if filtered {
				c.Set(betaPolicyFilterSetKey, map[string]struct{}{beta: {}})
			}
			account := &Account{ID: 701, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
				Credentials: map[string]any{"access_token": "test-token"}, Status: StatusActive, Schedulable: true}
			body := []byte(`{"model":"claude-sonnet-5","max_tokens":1024,"output_format":{"type":"json_schema","schema":{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}},"messages":[{"role":"user","content":"Return JSON"}]}`)
			svc := &GatewayService{cfg: &config.Config{}}
			req, _, err := svc.buildUpstreamRequest(context.Background(), c, account, body,
				"test-token", "oauth", "claude-sonnet-5", false, true)
			require.NoError(t, err)
			out := readUpstreamBodyForTest(t, req)
			require.JSONEq(t, gjson.GetBytes(body, "output_format").Raw, gjson.GetBytes(out, "output_format").Raw)
			header := getHeaderRaw(req.Header, "anthropic-beta")
			require.Equal(t, !filtered, containsBetaToken(header, beta))
			require.False(t, containsBetaToken(header, "custom-beta"))
		})
	}
}

// ---- merged from gateway_thinking_budget_test.go ----
const basetenBudgetError = "Error from provider (Console Go): Upstream request failed: [invalid_request_error] must be greater than 1024 to reserve tokens for a final answer when Baseten reasoning is enabled"

func TestThinkingBudgetConstraintErrors(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    bool
	}{
		{basetenBudgetError, true},
		{"MUST BE GREATER THAN 1024 TO RESERVE TOKENS FOR A FINAL ANSWER WHEN BASETEN REASONING IS ENABLED", true},
		{"thinking.budget_tokens must be >= 1024", true},
		{"thinking budget tokens must be greater than or equal to 1024", true},
		{"thinking.budget_tokens: Input should be greater than 1024", true},
		{"reasoning quota exceeded: 1024 tokens", false},
		{"must be greater than 1024", false},
		{"reserve tokens for a final answer when Baseten reasoning is enabled", false},
		{"must be greater than 2048 to reserve tokens for a final answer when Baseten reasoning is enabled", false},
		{"", false},
	} {
		t.Run(tc.message, func(t *testing.T) {
			require.Equal(t, tc.want, isThinkingBudgetConstraintError(tc.message))
		})
	}
}

func TestRectifyThinkingBudgetFinalAnswerReserve(t *testing.T) {
	for _, tc := range []struct {
		maxTokens int
		want      int64
		changed   bool
	}{
		{1024, 64000, true},
		{32000, 64000, true},
		{32001, 32001, false},
		{40000, 40000, false},
		{64000, 64000, false},
		{100000, 100000, false},
	} {
		t.Run(fmt.Sprint(tc.maxTokens), func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"example","max_tokens":%d,"thinking":{"type":"enabled","budget_tokens":32000},"messages":[{"role":"user","content":"hi"}]}`, tc.maxTokens))
			require.True(t, isThinkingBudgetConstraintError(basetenBudgetError))
			got, changed := RectifyThinkingBudget(body)
			require.Equal(t, tc.changed, changed)
			require.Equal(t, tc.want, gjson.GetBytes(got, "max_tokens").Int())
			require.Equal(t, int64(32000), gjson.GetBytes(got, "thinking.budget_tokens").Int())
			require.Equal(t, "hi", gjson.GetBytes(got, "messages.0.content").String())
		})
	}
	adaptive := []byte(`{"max_tokens":1024,"thinking":{"type":"adaptive"}}`)
	got, changed := RectifyThinkingBudget(adaptive)
	require.False(t, changed)
	require.Equal(t, adaptive, got)
	legacy := []byte(`{"max_tokens":32001,"thinking":{"type":"enabled","budget_tokens":32000}}`)
	got, changed = RectifyThinkingBudget(legacy)
	require.False(t, changed)
	require.Equal(t, legacy, got)
}

// ---- merged from gateway_upstream_transport_error_test.go ----
type transportTempUnschedRepoStub struct {
	AccountRepository
	calls      int
	lastID     int64
	lastUntil  time.Time
	lastReason string
}

func (r *transportTempUnschedRepoStub) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.calls++
	r.lastID = id
	r.lastUntil = until
	r.lastReason = reason
	return nil
}

func newTransportErrorTestGin(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c
}

// TestHandleUpstreamTransportError_TransientFailsOverWithoutEviction pins the
// contract for transient transport blips (EOF / connection reset): the request
// fails over to another account, the current account stays schedulable, and
// nothing is written to the response (the handler owns it).
func TestHandleUpstreamTransportError_TransientFailsOverWithoutEviction(t *testing.T) {
	repo := &transportTempUnschedRepoStub{}
	s := &GatewayService{accountRepo: repo}
	c := newTransportErrorTestGin(t)
	account := &Account{ID: 149, Name: "acc", Platform: PlatformAnthropic}

	err := s.handleUpstreamTransportError(context.Background(), c, account,
		errors.New(`Post "http://upstream/v1/messages?beta=true": EOF`), OpsUpstreamErrorEvent{})

	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("expected *UpstreamFailoverError, got %T: %v", err, err)
	}
	if failoverErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("StatusCode = %d, want 502", failoverErr.StatusCode)
	}
	if string(failoverErr.ResponseBody) != string(gatewayTransportFailoverBody) {
		t.Fatalf("ResponseBody = %s, want legacy 502 body", failoverErr.ResponseBody)
	}
	if !failoverErr.ShouldRetryNextAccount() {
		t.Fatal("transient transport error must allow retrying the next account")
	}
	if repo.calls != 0 {
		t.Fatalf("SetTempUnschedulable called %d times for a transient error, want 0", repo.calls)
	}
	if c.Writer.Written() {
		t.Fatal("handler owns the response; service must not write on transport failover")
	}
}

// TestHandleUpstreamTransportError_AttributesEventTimeProxy pins that the
// shared helper stamps proxy attribution from the same account snapshot the
// transport used, so every Anthropic/Bedrock caller inherits it.
func TestHandleUpstreamTransportError_AttributesEventTimeProxy(t *testing.T) {
	proxyID := int64(10060)
	tests := []struct {
		name     string
		account  *Account
		wantID   *int64
		wantName string
	}{
		{
			name:     "managed proxy",
			account:  &Account{ID: 149, Name: "acc", Platform: PlatformAnthropic, ProxyID: &proxyID, Proxy: &Proxy{ID: proxyID, Name: "wldsg82-ipv6-10060"}},
			wantID:   &proxyID,
			wantName: "wldsg82-ipv6-10060",
		},
		{
			name:     "direct",
			account:  &Account{ID: 149, Name: "acc", Platform: PlatformAnthropic},
			wantName: opsProxyNameDirect,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &GatewayService{accountRepo: &transportTempUnschedRepoStub{}}
			c := newTransportErrorTestGin(t)
			_ = s.handleUpstreamTransportError(context.Background(), c, tt.account,
				errors.New("EOF"), OpsUpstreamErrorEvent{UpstreamURL: "https://api.anthropic.com/v1/messages", Passthrough: true})
			raw, ok := c.Get(OpsUpstreamErrorsKey)
			if !ok {
				t.Fatal("no upstream events recorded")
			}
			events := raw.([]*OpsUpstreamErrorEvent)
			if len(events) != 1 {
				t.Fatalf("events = %d, want 1", len(events))
			}
			ev := events[0]
			if tt.wantID == nil {
				if ev.ProxyID != nil {
					t.Fatalf("proxy_id = %d, want null", *ev.ProxyID)
				}
			} else if ev.ProxyID == nil || *ev.ProxyID != *tt.wantID {
				t.Fatalf("proxy_id = %v, want %d", ev.ProxyID, *tt.wantID)
			}
			if ev.ProxyName != tt.wantName {
				t.Fatalf("proxy_name = %q, want %q", ev.ProxyName, tt.wantName)
			}
			if !ev.Passthrough || ev.UpstreamURL == "" || ev.Kind != "request_error" {
				t.Fatalf("caller-supplied fields lost: %+v", ev)
			}
		})
	}
}

// TestHandleUpstreamTransportError_PersistentEvictsAccount pins the contract
// for durable faults (dead endpoint / DNS / proxy credentials): fail over AND
// temporarily unschedule the account for the transport cooldown.
func TestHandleUpstreamTransportError_PersistentEvictsAccount(t *testing.T) {
	repo := &transportTempUnschedRepoStub{}
	s := &GatewayService{accountRepo: repo}
	c := newTransportErrorTestGin(t)
	account := &Account{ID: 149, Name: "acc", Platform: PlatformAnthropic}

	before := time.Now()
	err := s.handleUpstreamTransportError(context.Background(), c, account,
		errors.New(`dial tcp 1.2.3.4:443: connect: connection refused`), OpsUpstreamErrorEvent{})

	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("expected *UpstreamFailoverError, got %T: %v", err, err)
	}
	if repo.calls != 1 {
		t.Fatalf("SetTempUnschedulable called %d times for a persistent error, want 1", repo.calls)
	}
	if repo.lastID != account.ID {
		t.Fatalf("unscheduled account = %d, want %d", repo.lastID, account.ID)
	}
	wantUntil := before.Add(gatewayTransportErrorTempUnschedDuration)
	if repo.lastUntil.Before(wantUntil.Add(-time.Minute)) || repo.lastUntil.After(wantUntil.Add(time.Minute)) {
		t.Fatalf("until = %v, want ~%v", repo.lastUntil, wantUntil)
	}
	if !strings.HasPrefix(repo.lastReason, "upstream transport error (proxy/network): ") {
		t.Fatalf("reason = %q, want transport-error prefix", repo.lastReason)
	}
}

// TestHandleUpstreamTransportError_ClientCanceledNoFailover pins that a
// canceled client neither fails over nor evicts: the upstream never had a
// chance to exhibit a fault.
func TestHandleUpstreamTransportError_ClientCanceledNoFailover(t *testing.T) {
	repo := &transportTempUnschedRepoStub{}
	s := &GatewayService{accountRepo: repo}
	c := newTransportErrorTestGin(t)
	account := &Account{ID: 149, Name: "acc", Platform: PlatformAnthropic}

	inErr := context.Canceled
	err := s.handleUpstreamTransportError(context.Background(), c, account, inErr, OpsUpstreamErrorEvent{})

	var failoverErr *UpstreamFailoverError
	if errors.As(err, &failoverErr) {
		t.Fatal("canceled client must not fail over to another account")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled passthrough", err)
	}
	if repo.calls != 0 {
		t.Fatalf("SetTempUnschedulable called %d times on client cancel, want 0", repo.calls)
	}
}

// TestHandleUpstreamTransportError_UpstreamDeadlineStillFailsOver pins that an
// upstream-side timeout (request context still alive) is treated as a
// transient fault: fail over, no eviction.
func TestHandleUpstreamTransportError_UpstreamDeadlineStillFailsOver(t *testing.T) {
	repo := &transportTempUnschedRepoStub{}
	s := &GatewayService{accountRepo: repo}
	c := newTransportErrorTestGin(t)
	account := &Account{ID: 149, Name: "acc", Platform: PlatformAnthropic}

	err := s.handleUpstreamTransportError(context.Background(), c, account,
		context.DeadlineExceeded, OpsUpstreamErrorEvent{})

	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("upstream deadline with live request context must fail over, got %T: %v", err, err)
	}
	if repo.calls != 0 {
		t.Fatalf("SetTempUnschedulable called %d times for upstream deadline, want 0", repo.calls)
	}
}

// ---- merged from gateway_usage_billing_fallback_test.go ----
// composite 分组的公开别名经 BillingModelSource 来源覆盖成为计费模型后有两类错计：
// 任意别名（如 team/best）查无价静默落 $0；含家族词的别名（如 all/claude）被价格表
// 家族模糊匹配错计（Opus 流量按 Sonnet 兜底价）。compositeBillableModel 要求别名必须
// 有显式渠道定价才可参与计费，否则回退实际转发的具体模型。
func TestCompositeBillableModel(t *testing.T) {
	svc := &GatewayService{billingService: NewBillingService(&config.Config{}, nil)}
	apiKey := &APIKey{}
	ctx := context.Background()

	// 别名无渠道定价（含家族词也一样）→ 回退具体模型
	require.Equal(t, "claude-opus-4-7",
		svc.compositeBillableModel(ctx, apiKey, "all/claude", "claude-opus-4-7"))
	require.Equal(t, "claude-sonnet-4",
		svc.compositeBillableModel(ctx, apiKey, "team/best", "claude-sonnet-4"))

	// 未发生来源覆盖（计费模型已是具体模型）→ 原样返回
	require.Equal(t, "claude-sonnet-4",
		svc.compositeBillableModel(ctx, apiKey, "claude-sonnet-4", "claude-sonnet-4"))

	// 具体模型缺失 → 保持原值（走后续通用兜底/既有路径）
	require.Equal(t, "all/claude",
		svc.compositeBillableModel(ctx, apiKey, "all/claude", ""))
}

// billableModelWithFallback 是通用安全网：选定计费模型查不到任何价格时回退到
// 实际转发的具体模型；已定价流量（含家族兜底可解析的名字）不受影响。
func TestBillableModelWithFallback(t *testing.T) {
	svc := &GatewayService{billingService: NewBillingService(&config.Config{}, nil)}
	apiKey := &APIKey{}
	ctx := context.Background()

	// 完全无价的别名 → 回退到具体转发模型（claude-sonnet-4 有内置回退价格）
	require.Equal(t, "claude-sonnet-4",
		svc.billableModelWithFallback(ctx, apiKey, "team/best", "", "claude-sonnet-4"))

	// 已定价模型不回退，候选被忽略
	require.Equal(t, "claude-sonnet-4",
		svc.billableModelWithFallback(ctx, apiKey, "claude-sonnet-4", "claude-opus-4"))

	// 所有候选都无价 → 保持原值，走既有 warn + 零成本路径
	require.Equal(t, "team/best",
		svc.billableModelWithFallback(ctx, apiKey, "team/best", "another/alias", ""))

	// 空计费模型 + 有价候选 → 取候选
	require.Equal(t, "claude-sonnet-4",
		svc.billableModelWithFallback(ctx, apiKey, "", "claude-sonnet-4"))
}

func TestHasResolvableTokenPricing(t *testing.T) {
	svc := &GatewayService{billingService: NewBillingService(&config.Config{}, nil)}
	apiKey := &APIKey{}
	ctx := context.Background()

	require.True(t, svc.hasResolvableTokenPricing(ctx, "claude-sonnet-4", apiKey))
	// 注意：含家族词的名字（all/claude）会被价格表家族兜底解析为"有价"，
	// 这正是 compositeBillableModel 必须先于通用兜底拦截别名的原因。
	require.True(t, svc.hasResolvableTokenPricing(ctx, "all/claude", apiKey))
	require.False(t, svc.hasResolvableTokenPricing(ctx, "team/best", apiKey))
	require.False(t, svc.hasResolvableTokenPricing(ctx, "", apiKey))

	// billingService 缺失时 fail-closed（不误判有价）
	empty := &GatewayService{}
	require.False(t, empty.hasResolvableTokenPricing(ctx, "claude-sonnet-4", apiKey))
}

// ---- merged from gateway_usage_billing_request_id_test.go ----
func TestResolveUsageBillingRequestID_ForcedWebSearchBeatsClientID(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-shared-id")
	got := resolveUsageBillingRequestID(ctx, "web_search:uuid-1")
	require.Equal(t, "web_search:uuid-1", got)
}

func TestResolveUsageBillingRequestID_ClientWinsOverPlainUpstream(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-shared-id")
	got := resolveUsageBillingRequestID(ctx, "resp_abc")
	require.Equal(t, "client:client-shared-id", got)
}

func TestIsForcedUsageBillingRequestID(t *testing.T) {
	t.Parallel()
	require.True(t, isForcedUsageBillingRequestID("web_search:x"))
	require.True(t, isForcedUsageBillingRequestID("grok-video:task-1"))
	require.True(t, isForcedUsageBillingRequestID("grok_audio:up-1"))
	require.True(t, isForcedUsageBillingRequestID("grok_realtime:sess-1"))
	require.False(t, isForcedUsageBillingRequestID("resp_abc"))
}

func TestStableGrokAudioBillingRequestID(t *testing.T) {
	t.Parallel()
	require.Equal(t, "grok_audio:up-1", StableGrokAudioBillingRequestID("up-1"))
	require.Equal(t, "grok_audio:up-1", StableGrokAudioBillingRequestID("grok_audio:up-1"))
	got := StableGrokAudioBillingRequestID("")
	require.True(t, strings.HasPrefix(got, "grok_audio:"))
	require.Greater(t, len(got), len("grok_audio:"))
}

func TestStableGrokRealtimeBillingRequestID(t *testing.T) {
	t.Parallel()
	require.Equal(t, "grok_realtime:s1", StableGrokRealtimeBillingRequestID("s1"))
	require.Equal(t, "grok_realtime:s1", StableGrokRealtimeBillingRequestID("grok_realtime:s1"))
	got := StableGrokRealtimeBillingRequestID("")
	require.True(t, strings.HasPrefix(got, "grok_realtime:"))
}

func TestResolveUsageBillingRequestID_ForcedGrokAudioBeatsClientID(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-shared-id")
	got := resolveUsageBillingRequestID(ctx, StableGrokAudioBillingRequestID("up-9"))
	require.Equal(t, "grok_audio:up-9", got)
}

// ---- merged from gateway_waiting_queue_test.go ----
// TestDecrementWaitCount_NilCache 确保 nil cache 不会 panic
func TestDecrementWaitCount_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}
	// 不应 panic
	svc.DecrementWaitCount(context.Background(), 1)
}

// TestDecrementWaitCount_CacheError 确保 cache 错误不会传播
func TestDecrementWaitCount_CacheError(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{}
	svc := NewConcurrencyService(cache)
	// DecrementWaitCount 使用 background context，错误只记录日志不传播
	svc.DecrementWaitCount(context.Background(), 1)
}

// TestDecrementAccountWaitCount_NilCache 确保 nil cache 不会 panic
func TestDecrementAccountWaitCount_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}
	svc.DecrementAccountWaitCount(context.Background(), 1)
}

// TestDecrementAccountWaitCount_CacheError 确保 cache 错误不会传播
func TestDecrementAccountWaitCount_CacheError(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{}
	svc := NewConcurrencyService(cache)
	svc.DecrementAccountWaitCount(context.Background(), 1)
}

// TestWaitingQueueFlow_IncrementThenDecrement 测试完整的等待队列增减流程
func TestWaitingQueueFlow_IncrementThenDecrement(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: true}
	svc := NewConcurrencyService(cache)

	// 进入等待队列
	allowed, err := svc.IncrementWaitCount(context.Background(), 1, 25)
	require.NoError(t, err)
	require.True(t, allowed)

	// 离开等待队列（不应 panic）
	svc.DecrementWaitCount(context.Background(), 1)
}

// TestWaitingQueueFlow_AccountLevel 测试账号级等待队列流程
func TestWaitingQueueFlow_AccountLevel(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: true}
	svc := NewConcurrencyService(cache)

	// 进入账号等待队列
	allowed, err := svc.IncrementAccountWaitCount(context.Background(), 42, 10)
	require.NoError(t, err)
	require.True(t, allowed)

	// 离开账号等待队列
	svc.DecrementAccountWaitCount(context.Background(), 42)
}

// TestWaitingQueueFull_Returns429Signal 测试等待队列满时返回 false
func TestWaitingQueueFull_Returns429Signal(t *testing.T) {
	// waitAllowed=false 模拟队列已满
	cache := &stubConcurrencyCacheForTest{waitAllowed: false}
	svc := NewConcurrencyService(cache)

	// 用户级等待队列满
	allowed, err := svc.IncrementWaitCount(context.Background(), 1, 25)
	require.NoError(t, err)
	require.False(t, allowed, "等待队列满时应返回 false（调用方根据此返回 429）")

	// 账号级等待队列满
	allowed, err = svc.IncrementAccountWaitCount(context.Background(), 1, 10)
	require.NoError(t, err)
	require.False(t, allowed, "账号等待队列满时应返回 false")
}

// TestWaitingQueue_FailOpen_OnCacheError 测试 Redis 故障时 fail-open
func TestWaitingQueue_FailOpen_OnCacheError(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitErr: errors.New("redis connection refused")}
	svc := NewConcurrencyService(cache)

	// 用户级：Redis 错误时允许通过
	allowed, err := svc.IncrementWaitCount(context.Background(), 1, 25)
	require.NoError(t, err, "Redis 错误不应向调用方传播")
	require.True(t, allowed, "Redis 故障时应 fail-open 放行")

	// 账号级：同样 fail-open
	allowed, err = svc.IncrementAccountWaitCount(context.Background(), 1, 10)
	require.NoError(t, err, "Redis 错误不应向调用方传播")
	require.True(t, allowed, "Redis 故障时应 fail-open 放行")
}

// TestCalculateMaxWait_Scenarios 测试最大等待队列大小计算
func TestCalculateMaxWait_Scenarios(t *testing.T) {
	tests := []struct {
		concurrency int
		expected    int
	}{
		{5, 25},    // 5 + 20
		{10, 30},   // 10 + 20
		{1, 21},    // 1 + 20
		{0, 21},    // min(1) + 20
		{-1, 21},   // min(1) + 20
		{-10, 21},  // min(1) + 20
		{100, 120}, // 100 + 20
	}
	for _, tt := range tests {
		result := CalculateMaxWait(tt.concurrency)
		require.Equal(t, tt.expected, result, "CalculateMaxWait(%d)", tt.concurrency)
	}
}

// ---- merged from gateway_websearch_block_filter_test.go ----
// emulatedWebSearchBody is a follow-up /v1/messages request whose history
// contains an assistant turn synthesized by the web-search emulation
// (server_tool_use + web_search_tool_result with the local srvtoolu_ws_ ID
// prefix, followed by the text summary).
const emulatedWebSearchBody = `{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[` +
	`{"role":"user","content":[{"type":"text","text":"search the weather"}]},` +
	`{"role":"assistant","content":[` +
	`{"type":"server_tool_use","id":"srvtoolu_ws_0123456789abcdef","name":"web_search","input":{"query":"weather"}},` +
	`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_ws_0123456789abcdef","content":[{"type":"web_search_result","url":"https://example.com","title":"Weather"}]},` +
	`{"type":"text","text":"Here are the search results for \"weather\":"}]},` +
	`{"role":"user","content":[{"type":"text","text":"thanks, continue"}]}]}`

// genuineWebSearchBody carries real Anthropic web-search blocks (upstream IDs
// do NOT have the local srvtoolu_ws_ prefix).
const genuineWebSearchBody = `{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[` +
	`{"role":"user","content":[{"type":"text","text":"search"}]},` +
	`{"role":"assistant","content":[` +
	`{"type":"server_tool_use","id":"srvtoolu_01ABCDEF","name":"web_search","input":{"query":"weather"}},` +
	`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01ABCDEF","content":[{"type":"web_search_result","url":"https://example.com","title":"Weather"}]},` +
	`{"type":"text","text":"summary with citations"}]}]}`

func collectContentTypes(t *testing.T, body []byte) []string {
	t.Helper()
	var types []string
	for _, msg := range gjson.GetBytes(body, "messages").Array() {
		for _, block := range msg.Get("content").Array() {
			types = append(types, block.Get("type").String())
		}
	}
	return types
}

func TestFilterWebSearchHistoryBlocks_StripsEmulatedBlocksForAnthropicStrict(t *testing.T) {
	out := FilterWebSearchHistoryBlocks([]byte(emulatedWebSearchBody), "claude-sonnet-4-6")

	require.Equal(t, []string{"text", "text", "text"}, collectContentTypes(t, out))
	// The emulated text summary must survive so the search context is preserved.
	require.Contains(t, string(out), "Here are the search results")
	require.NotContains(t, string(out), "srvtoolu_ws_")
	require.True(t, gjson.ValidBytes(out))
}

func TestFilterWebSearchHistoryBlocks_KeepsGenuineBlocksForAnthropicStrict(t *testing.T) {
	body := []byte(genuineWebSearchBody)
	out := FilterWebSearchHistoryBlocks(body, "claude-sonnet-4-6")

	require.Equal(t, string(body), string(out))
}

func TestFilterWebSearchHistoryBlocks_StripsAllBlocksForPassbackRequired(t *testing.T) {
	// GLM only accepts text/thinking/image/tool_use/tool_result and rejects
	// server_tool_use with 400, so genuine blocks must be stripped as well.
	out := FilterWebSearchHistoryBlocks([]byte(genuineWebSearchBody), "glm-4.7")

	require.Equal(t, []string{"text", "text"}, collectContentTypes(t, out))
	require.NotContains(t, string(out), "server_tool_use")
	require.NotContains(t, string(out), "web_search_tool_result")
	require.Contains(t, string(out), "summary with citations")
}

func TestFilterWebSearchHistoryBlocks_StripsEmulatedBlocksForUnknownModel(t *testing.T) {
	out := FilterWebSearchHistoryBlocks([]byte(emulatedWebSearchBody), "totally-unknown-model")

	require.Equal(t, []string{"text", "text", "text"}, collectContentTypes(t, out))
	require.NotContains(t, string(out), "srvtoolu_ws_")
}

func TestFilterWebSearchHistoryBlocks_KeepsGenuineBlocksForUnknownModel(t *testing.T) {
	body := []byte(genuineWebSearchBody)
	out := FilterWebSearchHistoryBlocks(body, "totally-unknown-model")

	require.Equal(t, string(body), string(out))
}

func TestFilterWebSearchHistoryBlocks_NoWebSearchBlocksFastPath(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	out := FilterWebSearchHistoryBlocks(body, "claude-sonnet-4-6")

	require.Equal(t, string(body), string(out))
}

func TestFilterWebSearchHistoryBlocks_EmptiedMessageGetsPlaceholder(t *testing.T) {
	body := []byte(`{"model":"glm-4.7","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"search"}]},` +
		`{"role":"assistant","content":[` +
		`{"type":"server_tool_use","id":"srvtoolu_01X","name":"web_search","input":{"query":"q"}},` +
		`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01X","content":[]}]}]}`)

	out := FilterWebSearchHistoryBlocks(body, "glm-4.7")

	msgs := gjson.GetBytes(out, "messages").Array()
	require.Len(t, msgs, 2)
	assistant := msgs[1]
	require.Equal(t, "assistant", assistant.Get("role").String())
	content := assistant.Get("content").Array()
	require.Len(t, content, 1)
	require.Equal(t, "text", content[0].Get("type").String())
	require.Equal(t, "(assistant content removed)", content[0].Get("text").String())
}

func TestFilterWebSearchHistoryBlocks_StringContentUntouched(t *testing.T) {
	// A string mentioning the pattern inside a text value must not trigger a rewrite.
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[` +
		`{"role":"user","content":"please explain \"server_tool_use\" blocks"}]}`)

	out := FilterWebSearchHistoryBlocks(body, "claude-sonnet-4-6")

	require.Equal(t, string(body), string(out))
}

func TestFilterWebSearchHistoryBlocks_InvalidMessagesUnchanged(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":"server_tool_use"}`)
	out := FilterWebSearchHistoryBlocks(body, "claude-sonnet-4-6")

	require.Equal(t, string(body), string(out))
}

func TestFilterWebSearchHistoryBlocks_PreservesOtherToolBlocks(t *testing.T) {
	body := []byte(`{"model":"glm-4.7","messages":[` +
		`{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"toolu_01A","name":"get_weather","input":{}},` +
		`{"type":"server_tool_use","id":"srvtoolu_ws_abc","name":"web_search","input":{"query":"q"}},` +
		`{"type":"text","text":"result"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01A","content":"sunny"}]}]}`)

	out := FilterWebSearchHistoryBlocks(body, "glm-4.7")

	require.Equal(t, []string{"tool_use", "text", "tool_result"}, collectContentTypes(t, out))
}

// ---- merged from gateway_websearch_emulation_test.go ----
// --- isOnlyWebSearchToolInBody ---

func TestIsOnlyWebSearchToolInBody_WebSearchType(t *testing.T) {
	require.True(t, isOnlyWebSearchToolInBody([]byte(`{"tools":[{"type":"web_search"}]}`)))
}

func TestIsOnlyWebSearchToolInBody_WebSearch2025Type(t *testing.T) {
	require.True(t, isOnlyWebSearchToolInBody([]byte(`{"tools":[{"type":"web_search_20250305"}]}`)))
}

func TestIsOnlyWebSearchToolInBody_GoogleSearchType(t *testing.T) {
	require.True(t, isOnlyWebSearchToolInBody([]byte(`{"tools":[{"type":"google_search"}]}`)))
}

func TestIsOnlyWebSearchToolInBody_NameWebSearch(t *testing.T) {
	require.True(t, isOnlyWebSearchToolInBody([]byte(`{"tools":[{"name":"web_search"}]}`)))
}

func TestIsOnlyWebSearchToolInBody_NameWebSearch2025(t *testing.T) {
	require.True(t, isOnlyWebSearchToolInBody([]byte(`{"tools":[{"name":"web_search_20250305"}]}`)))
}

func TestIsOnlyWebSearchToolInBody_NameGoogleSearch(t *testing.T) {
	require.True(t, isOnlyWebSearchToolInBody([]byte(`{"tools":[{"name":"google_search"}]}`)))
}

func TestIsOnlyWebSearchToolInBody_MultipleTools(t *testing.T) {
	require.False(t, isOnlyWebSearchToolInBody(
		[]byte(`{"tools":[{"type":"web_search"},{"type":"text_editor"}]}`)))
}

func TestIsOnlyWebSearchToolInBody_NoTools(t *testing.T) {
	require.False(t, isOnlyWebSearchToolInBody([]byte(`{"model":"claude-3"}`)))
}

func TestIsOnlyWebSearchToolInBody_EmptyToolsArray(t *testing.T) {
	require.False(t, isOnlyWebSearchToolInBody([]byte(`{"tools":[]}`)))
}

func TestIsOnlyWebSearchToolInBody_NonWebSearchTool(t *testing.T) {
	require.False(t, isOnlyWebSearchToolInBody([]byte(`{"tools":[{"type":"text_editor"}]}`)))
}

func TestIsOnlyWebSearchToolInBody_ToolsNotArray(t *testing.T) {
	require.False(t, isOnlyWebSearchToolInBody([]byte(`{"tools":"web_search"}`)))
}

// --- extractSearchQueryFromBody ---

func TestExtractSearchQueryFromBody_StringContent(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"what is golang"}]}`
	require.Equal(t, "what is golang", extractSearchQueryFromBody([]byte(body)))
}

func TestExtractSearchQueryFromBody_ArrayContent(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"search this"}]}]}`
	require.Equal(t, "search this", extractSearchQueryFromBody([]byte(body)))
}

func TestExtractSearchQueryFromBody_MultipleMessages(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"ok"},{"role":"user","content":"second"}]}`
	require.Equal(t, "second", extractSearchQueryFromBody([]byte(body)))
}

func TestExtractSearchQueryFromBody_LastMessageNotUser(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":"a"}]}`
	require.Equal(t, "", extractSearchQueryFromBody([]byte(body)))
}

func TestExtractSearchQueryFromBody_EmptyMessages(t *testing.T) {
	require.Equal(t, "", extractSearchQueryFromBody([]byte(`{"messages":[]}`)))
}

func TestExtractSearchQueryFromBody_NoMessages(t *testing.T) {
	require.Equal(t, "", extractSearchQueryFromBody([]byte(`{"model":"claude-3"}`)))
}

func TestExtractSearchQueryFromBody_ArrayContentSkipsEmptyText(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"image"},{"type":"text","text":""},{"type":"text","text":"real query"}]}]}`
	require.Equal(t, "real query", extractSearchQueryFromBody([]byte(body)))
}

func TestExtractSearchQueryFromBody_ArrayContentNoTextBlock(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"image","source":{}}]}]}`
	require.Equal(t, "", extractSearchQueryFromBody([]byte(body)))
}

// --- buildSearchResultBlocks ---

func TestBuildSearchResultBlocks_WithResults(t *testing.T) {
	results := []websearch.SearchResult{
		{URL: "https://a.com", Title: "A", Snippet: "snippet a", PageAge: "2 days"},
		{URL: "https://b.com", Title: "B", Snippet: "snippet b"},
	}
	blocks := buildSearchResultBlocks(results)
	require.Len(t, blocks, 2)
	require.Equal(t, "web_search_result", blocks[0]["type"])
	require.Equal(t, "https://a.com", blocks[0]["url"])
	require.Equal(t, "snippet a", blocks[0]["page_content"])
	require.Equal(t, "2 days", blocks[0]["page_age"])
	// Second result has no PageAge
	require.Equal(t, "https://b.com", blocks[1]["url"])
	_, hasPageAge := blocks[1]["page_age"]
	require.False(t, hasPageAge)
}

func TestBuildSearchResultBlocks_Empty(t *testing.T) {
	blocks := buildSearchResultBlocks(nil)
	require.Empty(t, blocks)
}

func TestBuildSearchResultBlocks_SnippetEmpty(t *testing.T) {
	blocks := buildSearchResultBlocks([]websearch.SearchResult{{URL: "https://x.com", Title: "X", Snippet: ""}})
	_, hasContent := blocks[0]["page_content"]
	require.False(t, hasContent)
}

// --- buildTextSummary ---

func TestBuildTextSummary_WithResults(t *testing.T) {
	results := []websearch.SearchResult{
		{URL: "https://a.com", Title: "A", Snippet: "desc a"},
	}
	summary := buildTextSummary("test query", results)
	require.Contains(t, summary, "test query")
	require.Contains(t, summary, "1. **A**")
	require.Contains(t, summary, "https://a.com")
}

func TestBuildTextSummary_NoResults(t *testing.T) {
	summary := buildTextSummary("test", nil)
	require.Contains(t, summary, "No search results found for: test")
}

// --- shouldEmulateWebSearch ---

// webSearchToolBody is a valid request body with exactly one web_search tool.
var webSearchToolBody = []byte(`{"tools":[{"type":"web_search"}],"messages":[{"role":"user","content":"test"}]}`)

// nonWebSearchToolBody is a request body without web_search tool.
var nonWebSearchToolBody = []byte(`{"tools":[{"type":"text_editor"}],"messages":[{"role":"user","content":"test"}]}`)

// newAnthropicAPIKeyAccount creates a test Account with the given web search emulation mode.
func newAnthropicAPIKeyAccount(mode string) *Account {
	return &Account{
		ID:       1,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Extra:    map[string]any{featureKeyWebSearchEmulation: mode},
	}
}

// setGlobalWebSearchConfig stores a config in the global cache used by SettingService.IsWebSearchEmulationEnabled.
func setGlobalWebSearchConfig(cfg *WebSearchEmulationConfig) {
	webSearchEmulationCache.Store(&cachedWebSearchEmulationConfig{
		config:    cfg,
		expiresAt: time.Now().Add(10 * time.Minute).UnixNano(),
	})
}

// clearGlobalWebSearchConfig resets the global cache to force re-read.
func clearGlobalWebSearchConfig() {
	webSearchEmulationCache.Store((*cachedWebSearchEmulationConfig)(nil))
}

// newSettingServiceForWebSearchTest creates a SettingService with a mock repo pre-loaded with config.
func newSettingServiceForWebSearchTest(enabled bool) *SettingService {
	repo := newMockSettingRepo()
	cfg := &WebSearchEmulationConfig{
		Enabled:   enabled,
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: "sk-test"}},
	}
	data, _ := json.Marshal(cfg)
	repo.data[SettingKeyWebSearchEmulationConfig] = string(data)
	return NewSettingService(repo, &config.Config{})
}

// newChannelServiceWithCache creates a ChannelService with a pre-built cache containing the channel.
func newChannelServiceWithCache(groupID int64, ch *Channel) *ChannelService {
	svc := &ChannelService{}
	cache := &channelCache{
		channelByGroupID: map[int64]*Channel{groupID: ch},
		byID:             map[int64]*Channel{ch.ID: ch},
		groupPlatform:    map[int64]string{},
		loadedAt:         time.Now(),
	}
	svc.cache.Store(cache)
	return svc
}

func TestShouldEmulateWebSearch_NilManager(t *testing.T) {
	SetWebSearchManager(nil)
	defer SetWebSearchManager(nil)

	settingSvc := newSettingServiceForWebSearchTest(true)
	setGlobalWebSearchConfig(&WebSearchEmulationConfig{
		Enabled:   true,
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: "k"}},
	})
	defer clearGlobalWebSearchConfig()

	svc := &GatewayService{settingService: settingSvc}
	account := newAnthropicAPIKeyAccount(WebSearchModeEnabled)
	require.False(t, svc.shouldEmulateWebSearch(context.Background(), account, nil, webSearchToolBody))
}

func TestShouldEmulateWebSearch_NotOnlyWebSearchTool(t *testing.T) {
	mgr := websearch.NewManager([]websearch.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil)
	SetWebSearchManager(mgr)
	defer SetWebSearchManager(nil)

	settingSvc := newSettingServiceForWebSearchTest(true)
	setGlobalWebSearchConfig(&WebSearchEmulationConfig{
		Enabled:   true,
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: "k"}},
	})
	defer clearGlobalWebSearchConfig()

	svc := &GatewayService{settingService: settingSvc}
	account := newAnthropicAPIKeyAccount(WebSearchModeEnabled)
	require.False(t, svc.shouldEmulateWebSearch(context.Background(), account, nil, nonWebSearchToolBody))
}

func TestShouldEmulateWebSearch_GlobalDisabled(t *testing.T) {
	mgr := websearch.NewManager([]websearch.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil)
	SetWebSearchManager(mgr)
	defer SetWebSearchManager(nil)

	// Global config disabled
	setGlobalWebSearchConfig(&WebSearchEmulationConfig{
		Enabled:   false,
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: "k"}},
	})
	defer clearGlobalWebSearchConfig()

	settingSvc := newSettingServiceForWebSearchTest(false)
	svc := &GatewayService{settingService: settingSvc}
	account := newAnthropicAPIKeyAccount(WebSearchModeEnabled)
	require.False(t, svc.shouldEmulateWebSearch(context.Background(), account, nil, webSearchToolBody))
}

func TestShouldEmulateWebSearch_AccountDisabled(t *testing.T) {
	mgr := websearch.NewManager([]websearch.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil)
	SetWebSearchManager(mgr)
	defer SetWebSearchManager(nil)

	setGlobalWebSearchConfig(&WebSearchEmulationConfig{
		Enabled:   true,
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: "k"}},
	})
	defer clearGlobalWebSearchConfig()

	settingSvc := newSettingServiceForWebSearchTest(true)
	svc := &GatewayService{settingService: settingSvc}
	account := newAnthropicAPIKeyAccount(WebSearchModeDisabled)
	require.False(t, svc.shouldEmulateWebSearch(context.Background(), account, nil, webSearchToolBody))
}

func TestShouldEmulateWebSearch_AccountEnabled(t *testing.T) {
	mgr := websearch.NewManager([]websearch.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil)
	SetWebSearchManager(mgr)
	defer SetWebSearchManager(nil)

	setGlobalWebSearchConfig(&WebSearchEmulationConfig{
		Enabled:   true,
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: "k"}},
	})
	defer clearGlobalWebSearchConfig()

	settingSvc := newSettingServiceForWebSearchTest(true)
	svc := &GatewayService{settingService: settingSvc}
	account := newAnthropicAPIKeyAccount(WebSearchModeEnabled)
	require.True(t, svc.shouldEmulateWebSearch(context.Background(), account, nil, webSearchToolBody))
}

func TestShouldEmulateWebSearch_DefaultMode_ChannelEnabled(t *testing.T) {
	mgr := websearch.NewManager([]websearch.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil)
	SetWebSearchManager(mgr)
	defer SetWebSearchManager(nil)

	setGlobalWebSearchConfig(&WebSearchEmulationConfig{
		Enabled:   true,
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: "k"}},
	})
	defer clearGlobalWebSearchConfig()

	settingSvc := newSettingServiceForWebSearchTest(true)
	ch := &Channel{
		ID:     10,
		Status: StatusActive,
		FeaturesConfig: map[string]any{
			featureKeyWebSearchEmulation: map[string]any{PlatformAnthropic: true},
		},
	}
	channelSvc := newChannelServiceWithCache(42, ch)
	svc := &GatewayService{settingService: settingSvc, channelService: channelSvc}

	account := newAnthropicAPIKeyAccount(WebSearchModeDefault)
	groupID := int64(42)
	require.True(t, svc.shouldEmulateWebSearch(context.Background(), account, &groupID, webSearchToolBody))
}

func TestShouldEmulateWebSearch_DefaultMode_ChannelDisabled(t *testing.T) {
	mgr := websearch.NewManager([]websearch.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil)
	SetWebSearchManager(mgr)
	defer SetWebSearchManager(nil)

	setGlobalWebSearchConfig(&WebSearchEmulationConfig{
		Enabled:   true,
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: "k"}},
	})
	defer clearGlobalWebSearchConfig()

	settingSvc := newSettingServiceForWebSearchTest(true)
	ch := &Channel{
		ID:     10,
		Status: StatusActive,
		FeaturesConfig: map[string]any{
			featureKeyWebSearchEmulation: map[string]any{PlatformAnthropic: false},
		},
	}
	channelSvc := newChannelServiceWithCache(42, ch)
	svc := &GatewayService{settingService: settingSvc, channelService: channelSvc}

	account := newAnthropicAPIKeyAccount(WebSearchModeDefault)
	groupID := int64(42)
	require.False(t, svc.shouldEmulateWebSearch(context.Background(), account, &groupID, webSearchToolBody))
}

func TestShouldEmulateWebSearch_DefaultMode_NilGroupID(t *testing.T) {
	mgr := websearch.NewManager([]websearch.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil)
	SetWebSearchManager(mgr)
	defer SetWebSearchManager(nil)

	setGlobalWebSearchConfig(&WebSearchEmulationConfig{
		Enabled:   true,
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: "k"}},
	})
	defer clearGlobalWebSearchConfig()

	settingSvc := newSettingServiceForWebSearchTest(true)
	svc := &GatewayService{settingService: settingSvc}
	account := newAnthropicAPIKeyAccount(WebSearchModeDefault)
	// nil groupID + default mode → falls through to channel check → returns false
	require.False(t, svc.shouldEmulateWebSearch(context.Background(), account, nil, webSearchToolBody))
}

func TestShouldEmulateWebSearch_DefaultMode_NilChannelService(t *testing.T) {
	mgr := websearch.NewManager([]websearch.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil)
	SetWebSearchManager(mgr)
	defer SetWebSearchManager(nil)

	setGlobalWebSearchConfig(&WebSearchEmulationConfig{
		Enabled:   true,
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: "k"}},
	})
	defer clearGlobalWebSearchConfig()

	settingSvc := newSettingServiceForWebSearchTest(true)
	svc := &GatewayService{settingService: settingSvc, channelService: nil}
	account := newAnthropicAPIKeyAccount(WebSearchModeDefault)
	groupID := int64(42)
	// nil channelService + default mode → returns false
	require.False(t, svc.shouldEmulateWebSearch(context.Background(), account, &groupID, webSearchToolBody))
}
