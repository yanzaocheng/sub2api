package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicfp"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---- merged from gateway_accept_encoding_test.go ----
func TestGatewayService_AcceptEncodingOnWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, protocol := range []struct {
		name  string
		major int
	}{
		{name: "HTTP1", major: 1},
		{name: "HTTP2", major: 2},
	} {
		t.Run(protocol.name, func(t *testing.T) {
			type receivedRequest struct {
				encodings []string
				proto     int
			}
			received := make(chan receivedRequest, 1)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- receivedRequest{encodings: r.Header.Values("Accept-Encoding"), proto: r.ProtoMajor}
				w.WriteHeader(http.StatusNoContent)
			}))
			server.EnableHTTP2 = protocol.major == 2
			server.StartTLS()
			defer server.Close()
			client := server.Client()
			client.Timeout = 5 * time.Second
			upstreamURL, err := url.Parse(server.URL)
			require.NoError(t, err)

			for _, tc := range []struct {
				name     string
				encoding string
				want     string
			}{
				{name: "gzip", encoding: "gzip", want: "gzip"},
				{name: "multiple encodings", encoding: "gzip, deflate, br", want: "gzip, deflate, br"},
				{name: "identity", encoding: "identity", want: "identity"},
				{name: "omitted", want: "gzip"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
					if tc.encoding != "" {
						c.Request.Header.Set("Accept-Encoding", tc.encoding)
					}
					svc := &GatewayService{cfg: &config.Config{}}
					account := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}
					req, _, err := svc.buildUpstreamRequest(context.Background(), c, account,
						[]byte(`{"model":"claude-sonnet-4-6","messages":[]}`), "test-token", "oauth", "claude-sonnet-4-6", false, false)
					require.NoError(t, err)
					req.URL.Scheme = upstreamURL.Scheme
					req.URL.Host = upstreamURL.Host
					req.Host = ""

					resp, err := client.Do(req)
					require.NoError(t, err)
					_, err = io.Copy(io.Discard, resp.Body)
					require.NoError(t, err)
					require.NoError(t, resp.Body.Close())
					got := <-received
					require.Equal(t, protocol.major, got.proto)
					require.Equal(t, []string{tc.want}, got.encodings)
				})
			}
		})
	}
}

// ---- merged from gateway_anthropic_apikey_passthrough_benchmark_test.go ----
func BenchmarkGatewayService_ParseSSEUsage_MessageStart(b *testing.B) {
	svc := &GatewayService{}
	data := `{"type":"message_start","message":{"usage":{"input_tokens":123,"cache_creation_input_tokens":45,"cache_read_input_tokens":6,"cached_tokens":6,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":25}}}}`
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		usage := &ClaudeUsage{}
		svc.parseSSEUsage(data, usage)
	}
}

func BenchmarkGatewayService_ParseSSEUsagePassthrough_MessageStart(b *testing.B) {
	data := `{"type":"message_start","message":{"usage":{"input_tokens":123,"cache_creation_input_tokens":45,"cache_read_input_tokens":6,"cached_tokens":6,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":25}}}}`
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		usage := &ClaudeUsage{}
		parseSSEUsagePassthrough(data, usage)
	}
}

func BenchmarkGatewayService_ParseSSEUsage_MessageDelta(b *testing.B) {
	svc := &GatewayService{}
	data := `{"type":"message_delta","usage":{"output_tokens":456,"cache_creation_input_tokens":30,"cache_read_input_tokens":7,"cached_tokens":7,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}`
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		usage := &ClaudeUsage{}
		svc.parseSSEUsage(data, usage)
	}
}

func BenchmarkGatewayService_ParseSSEUsagePassthrough_MessageDelta(b *testing.B) {
	data := `{"type":"message_delta","usage":{"output_tokens":456,"cache_creation_input_tokens":30,"cache_read_input_tokens":7,"cached_tokens":7,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}`
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		usage := &ClaudeUsage{}
		parseSSEUsagePassthrough(data, usage)
	}
}

func BenchmarkParseClaudeUsageFromResponseBody(b *testing.B) {
	body := []byte(`{"id":"msg_123","type":"message","usage":{"input_tokens":123,"output_tokens":456,"cache_creation_input_tokens":45,"cache_read_input_tokens":6,"cached_tokens":6,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":25}}}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseClaudeUsageFromResponseBody(body)
	}
}

// ---- merged from gateway_anthropic_vertex_beta_filter_test.go ----
func newVertexBetaTestContext(t *testing.T, anthropicBeta string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if anthropicBeta != "" {
		c.Request.Header.Set("Anthropic-Beta", anthropicBeta)
	}
	return c
}

func newVertexServiceAccount(id int64) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformAnthropic,
		Type:     AccountTypeServiceAccount,
		Credentials: map[string]any{
			"project_id": "vertex-proj",
			"location":   "us-east5",
		},
	}
}

// 复刻线上 400：近期 Claude Code CLI 透传的整份 anthropic-beta header 里含 Vertex
// 不接受的 token（advisor-tool / prompt-caching-scope / redact-thinking /
// thinking-token-count）。Vertex builder 必须剥掉它们，否则上游 HTTP 400（issue #3358）。
// 本用例在 Commit 1 之前 FAIL、之后 PASS。
func TestVertexBetaFilter_StripsUnsupportedClaudeCodeTokens(t *testing.T) {
	c := newVertexBetaTestContext(t,
		"claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,"+
			"advisor-tool-2026-03-01,prompt-caching-scope-2026-01-05,"+
			"redact-thinking-2026-02-12,thinking-token-count-2026-05-13,"+
			"context-management-2025-06-27")

	body := []byte(`{"model":"claude-opus-4-7","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`)

	svc := &GatewayService{}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, newVertexServiceAccount(401), body,
		"vertex-token", "service_account", "claude-opus-4-7@20260417", false, false,
	)
	require.NoError(t, err)

	outBeta := getHeaderRaw(req.Header, "anthropic-beta")

	// Vertex 拒绝的 token 必须全部剥掉。
	for _, bad := range []string{
		"advisor-tool-2026-03-01",
		"prompt-caching-scope-2026-01-05",
		"redact-thinking-2026-02-12",
		"thinking-token-count-2026-05-13",
		// 客户端身份 beta：Vertex service_account 不需要，亦不在白名单。
		"claude-code-20250219",
		"oauth-2025-04-20",
	} {
		require.False(t, anthropicBetaTokensContains(outBeta, bad),
			"token %q 必须被剥离；实际 outgoing beta=%q", bad, outBeta)
	}

	// 白名单内的 token 必须保留。
	for _, keep := range []string{
		"interleaved-thinking-2025-05-14",
		"context-management-2025-06-27",
	} {
		require.True(t, anthropicBetaTokensContains(outBeta, keep),
			"token %q 应保留；实际 outgoing beta=%q", keep, outBeta)
	}
}

// 全部 token 都不受 Vertex 支持时，outgoing header 不应下发 anthropic-beta。
func TestVertexBetaFilter_DropsHeaderWhenAllUnsupported(t *testing.T) {
	c := newVertexBetaTestContext(t,
		"prompt-caching-scope-2026-01-05,redact-thinking-2026-02-12")

	body := []byte(`{"model":"claude-opus-4-7","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`)

	svc := &GatewayService{}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, newVertexServiceAccount(402), body,
		"vertex-token", "service_account", "claude-opus-4-7@20260417", false, false,
	)
	require.NoError(t, err)

	require.Empty(t, getHeaderRaw(req.Header, "anthropic-beta"),
		"所有 token 被剥离后不应残留 anthropic-beta header")
}

// 能力维度 sanitize 以「最终 beta」为准：客户端只带不支持的 prompt-caching-scope（会被剥光），
// body 又带 context_management → 因最终 header 不含 context-management beta，body 字段必须 strip。
// 证明 sanitize 不再以原始 client 值为准（修复前用 clientBeta，会错误保留 context_management）。
func TestVertexBetaFilter_BodySanitizeKeysOnFinalBeta(t *testing.T) {
	c := newVertexBetaTestContext(t, "prompt-caching-scope-2026-01-05")

	body := []byte(`{"model":"claude-opus-4-7","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[{"role":"user","content":"hi"}]}`)

	svc := &GatewayService{}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, newVertexServiceAccount(403), body,
		"vertex-token", "service_account", "claude-opus-4-7@20260417", false, false,
	)
	require.NoError(t, err)

	got := readRequestBodyForTest(t, req)
	require.False(t, gjson.GetBytes(got, "context_management").Exists(),
		"最终 beta 不含 context-management 时 body.context_management 必须被 strip")
	require.Empty(t, getHeaderRaw(req.Header, "anthropic-beta"))
}

// BetaPolicy block 规则在 Vertex 路径同样生效：管理员 block 某 token，客户端带它 → 直接报错。
func TestVertexBetaFilter_BlocksViaBetaPolicy(t *testing.T) {
	settings := &BetaPolicySettings{
		Rules: []BetaPolicyRule{
			{
				BetaToken:    "context-management-2025-06-27",
				Action:       BetaPolicyActionBlock,
				Scope:        BetaPolicyScopeAll,
				ErrorMessage: "context management is blocked",
			},
		},
	}
	raw, err := json.Marshal(settings)
	require.NoError(t, err)

	svc := &GatewayService{
		settingService: NewSettingService(
			&betaPolicySettingRepoStub{values: map[string]string{
				SettingKeyBetaPolicySettings: string(raw),
			}},
			&config.Config{},
		),
	}

	c := newVertexBetaTestContext(t,
		"interleaved-thinking-2025-05-14,context-management-2025-06-27")
	body := []byte(`{"model":"claude-opus-4-7","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`)

	_, _, err = svc.buildUpstreamRequest(
		context.Background(), c, newVertexServiceAccount(404), body,
		"vertex-token", "service_account", "claude-opus-4-7@20260417", false, false,
	)
	require.Error(t, err)
	var blocked *BetaBlockedError
	require.True(t, errors.As(err, &blocked), "expected *BetaBlockedError, got %T", err)
	require.Equal(t, "context management is blocked", err.Error())
}

// filterVertexBetaTokens 单元测试：白名单过滤 + drop 集合 + 去重 + 空输入。
func TestFilterVertexBetaTokens(t *testing.T) {
	t.Run("whitelist filters unsupported", func(t *testing.T) {
		out := filterVertexBetaTokens(
			"interleaved-thinking-2025-05-14,prompt-caching-scope-2026-01-05,context-management-2025-06-27",
			nil,
		)
		require.Equal(t, "interleaved-thinking-2025-05-14,context-management-2025-06-27", out)
	})

	t.Run("drop set strips before whitelist", func(t *testing.T) {
		out := filterVertexBetaTokens(
			"interleaved-thinking-2025-05-14,context-management-2025-06-27",
			map[string]struct{}{"context-management-2025-06-27": {}},
		)
		require.Equal(t, "interleaved-thinking-2025-05-14", out)
	})

	t.Run("dedupe", func(t *testing.T) {
		out := filterVertexBetaTokens(
			"context-1m-2025-08-07,context-1m-2025-08-07",
			nil,
		)
		require.Equal(t, "context-1m-2025-08-07", out)
	})

	t.Run("empty input", func(t *testing.T) {
		require.Empty(t, filterVertexBetaTokens("", nil))
		require.Empty(t, filterVertexBetaTokens("prompt-caching-scope-2026-01-05", nil))
	})
}

// ---- merged from gateway_anthropic_vertex_service_account_test.go ----
func TestGatewayService_BuildAnthropicVertexServiceAccountRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Authorization", "Bearer inbound-token")
	c.Request.Header.Set("X-Api-Key", "inbound-api-key")
	c.Request.Header.Set("Anthropic-Version", "2023-06-01")
	c.Request.Header.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")

	account := &Account{
		ID:       301,
		Platform: PlatformAnthropic,
		Type:     AccountTypeServiceAccount,
		Credentials: map[string]any{
			"project_id": "vertex-proj",
			"location":   "us-east5",
		},
	}
	body := []byte(`{"model":"claude-sonnet-4-5","stream":false,"max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)

	svc := &GatewayService{}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(),
		c,
		account,
		body,
		"vertex-token",
		"service_account",
		"claude-sonnet-4-5@20250929",
		false,
		false,
	)
	require.NoError(t, err)
	require.Equal(t, "https://us-east5-aiplatform.googleapis.com/v1/projects/vertex-proj/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-5@20250929:rawPredict", req.URL.String())
	require.Equal(t, "Bearer vertex-token", getHeaderRaw(req.Header, "authorization"))
	require.Empty(t, getHeaderRaw(req.Header, "x-api-key"))
	require.Empty(t, getHeaderRaw(req.Header, "anthropic-version"))
	require.Equal(t, "interleaved-thinking-2025-05-14", getHeaderRaw(req.Header, "anthropic-beta"))

	got := readRequestBodyForTest(t, req)
	require.Equal(t, "", gjson.GetBytes(got, "model").String())
	require.Equal(t, vertexAnthropicVersion, gjson.GetBytes(got, "anthropic_version").String())
	require.Equal(t, "hello", gjson.GetBytes(got, "messages.0.content").String())
}

func readRequestBodyForTest(t *testing.T, req *http.Request) []byte {
	t.Helper()
	require.NotNil(t, req.Body)
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	return body
}

// Vertex 路径回归保护：同样需要
// body↔beta header 能力维度对称。客户端 header 不带 context-management beta
// 但 body 带 context_management 字段 → Vertex builder 必须 strip 字段，与 Anthropic
// 直连 / Bedrock 路径保持一致。
func TestGatewayService_BuildAnthropicVertexServiceAccount_StripsContextManagementWhenBetaMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	// 客户端 header 只带 interleaved-thinking，不带 context-management-2025-06-27
	c.Request.Header.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")

	account := &Account{
		ID: 302, Platform: PlatformAnthropic, Type: AccountTypeServiceAccount,
		Credentials: map[string]any{"project_id": "vertex-proj", "location": "us-east5"},
	}
	// body 带了 context_management 字段（客户端透传 / normalize 补齐 / mimicry 注入等场景都可能导致）
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[{"role":"user","content":"hi"}]}`)

	svc := &GatewayService{}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body,
		"vertex-token", "service_account", "claude-haiku-4-5@20251001", false, false,
	)
	require.NoError(t, err)

	got := readRequestBodyForTest(t, req)
	require.False(t, gjson.GetBytes(got, "context_management").Exists(),
		"Vertex 路径下客户端 header 缺 context-management beta 时，必须 strip body 同名字段")
	// header 对称断言：覆盖未来某人在 Vertex builder 里加入与 sanitize 不一致的 header 处理。
	outBeta := getHeaderRaw(req.Header, "anthropic-beta")
	require.False(t, anthropicBetaTokensContains(outBeta, "context-management-2025-06-27"),
		"与 body 对称：outgoing anthropic-beta header 也不含 context-management beta")
}

// Vertex 路径反面：客户端 header 含 context-management beta 时保留字段。
func TestGatewayService_BuildAnthropicVertexServiceAccount_PreservesContextManagementWhenBetaPresent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14,context-management-2025-06-27")

	account := &Account{
		ID: 303, Platform: PlatformAnthropic, Type: AccountTypeServiceAccount,
		Credentials: map[string]any{"project_id": "vertex-proj", "location": "us-east5"},
	}
	body := []byte(`{"model":"claude-sonnet-4-6","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)

	svc := &GatewayService{}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body,
		"vertex-token", "service_account", "claude-sonnet-4-6@20260218", false, false,
	)
	require.NoError(t, err)

	got := readRequestBodyForTest(t, req)
	require.True(t, gjson.GetBytes(got, "context_management").Exists(),
		"Vertex + 客户端 header 包含 context-management beta 时字段必须保留")
	outBeta := getHeaderRaw(req.Header, "anthropic-beta")
	require.True(t, anthropicBetaTokensContains(outBeta, "context-management-2025-06-27"),
		"与 body 对称：outgoing anthropic-beta header 同步含 context-management beta")
}

// ---- merged from gateway_billing_header_test.go ----
func TestSyncBillingHeaderVersion(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		userAgent string
		wantSub   string // substring expected in result
		unchanged bool   // expect body to remain the same
	}{
		{
			name:      "replaces cc_version and recomputes message-derived suffix",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81.df2; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],"messages":[]}`,
			userAgent: "claude-cli/2.1.22 (external, cli)",
			wantSub:   "cc_version=2.1.22." + computeClaudeCodeFingerprint([]byte(`{"messages":[]}`), "2.1.22"),
		},
		{
			name:      "no billing header in system",
			body:      `{"system":[{"type":"text","text":"You are Claude Code."}],"messages":[]}`,
			userAgent: "claude-cli/2.1.22",
			unchanged: true,
		},
		{
			name:      "no system field",
			body:      `{"messages":[]}`,
			userAgent: "claude-cli/2.1.22",
			unchanged: true,
		},
		{
			name:      "user-agent without version",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`,
			userAgent: "Mozilla/5.0",
			unchanged: true,
		},
		{
			name:      "empty user-agent",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`,
			userAgent: "",
			unchanged: true,
		},
		{
			name:      "version already matches",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.22; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`,
			userAgent: "claude-cli/2.1.22",
			unchanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := syncBillingHeaderVersion([]byte(tt.body), tt.userAgent)
			if tt.unchanged {
				assert.Equal(t, tt.body, string(result), "body should remain unchanged")
			} else {
				assert.Contains(t, string(result), tt.wantSub)
				// Ensure old semver is gone
				assert.NotContains(t, string(result), "cc_version=2.1.81")
			}
		})
	}
}

func TestSyncBillingHeaderVersion_RecomputesSuffixAndIsIdempotent(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81.df2; cc_entrypoint=cli;"}],"messages":[{"role":"user","content":"hello world"}]}`)
	version := "2.1.22"
	result := syncBillingHeaderVersion(body, "claude-cli/"+version)
	require.Contains(t, gjson.GetBytes(result, "system.0.text").String(),
		"cc_version="+version+"."+computeClaudeCodeFingerprint(body, version)+";")
	require.Equal(t, string(result), string(syncBillingHeaderVersion(result, "claude-cli/"+version)))
	require.JSONEq(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(result, "messages").Raw)
}

func TestBuildOAuthRequest_BillingMatchesWireUserAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"messages", "count_tokens"} {
		for _, tc := range []struct {
			name      string
			mimic     bool
			identity  bool
			disableFP bool
		}{
			{name: "mimic_overrides_cached_version", mimic: true, identity: true},
			{name: "mimic_without_identity", mimic: true},
			{name: "mimic_with_fingerprint_disabled", mimic: true, identity: true, disableFP: true},
			{name: "passthrough_uses_cached_version", identity: true},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				resetGatewayForwardingSettingsCacheForTest(t)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				body := []byte(`{"model":"claude-haiku-4-5","system":[{"type":"text","text":""}],"messages":[{"role":"user","content":"hello world"}]}`)
				billing, err := buildBillingAttributionText(body, "2.1.81")
				require.NoError(t, err)
				body, err = sjson.SetBytes(body, "system.0.text", billing)
				require.NoError(t, err)

				cfg := &config.Config{}
				svc := &GatewayService{cfg: cfg}
				cachedUA := "claude-cli/2.9.0 (external, cli)"
				if tc.identity {
					svc.identityService = NewIdentityService(&stubIdentityCache{fingerprint: &Fingerprint{
						UserAgent: cachedUA, ClientID: "test-client", UpdatedAt: time.Now().Unix(),
					}})
				}
				if tc.disableFP {
					svc.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
						SettingKeyEnableFingerprintUnification: "false",
					}}, cfg)
				}
				account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
				var req *http.Request
				var wireBody []byte
				if endpoint == "messages" {
					req, wireBody, err = svc.buildUpstreamRequest(context.Background(), c, account,
						body, "test-token", "oauth", "claude-haiku-4-5", false, tc.mimic)
				} else {
					req, wireBody, err = svc.buildCountTokensRequest(context.Background(), c, account,
						body, "test-token", "oauth", "claude-haiku-4-5", tc.mimic)
				}
				require.NoError(t, err)
				defer func() { require.NoError(t, req.Body.Close()) }()
				wantUA := cachedUA
				if tc.mimic {
					wantUA = claude.DefaultHeaders()["User-Agent"]
				}
				require.Equal(t, wantUA, getHeaderRaw(req.Header, "User-Agent"))
				version := ExtractCLIVersion(wantUA)
				require.Contains(t, gjson.GetBytes(wireBody, "system.0.text").String(),
					"cc_version="+version+"."+computeClaudeCodeFingerprint(wireBody, version)+";")
				actualBody, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.Equal(t, wireBody, actualBody)
			})
		}
	}
}

// ---- merged from gateway_body_order_test.go ----
type gatewayTTLSettingRepo struct {
	data map[string]string
}

func (r *gatewayTTLSettingRepo) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (r *gatewayTTLSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if r == nil {
		return "", ErrSettingNotFound
	}
	v, ok := r.data[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}

func (r *gatewayTTLSettingRepo) Set(_ context.Context, key, value string) error {
	if r == nil {
		return errors.New("setting repo is nil")
	}
	if r.data == nil {
		r.data = map[string]string{}
	}
	r.data[key] = value
	return nil
}

func (r *gatewayTTLSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string)
	if r == nil {
		return result, nil
	}
	for _, key := range keys {
		if v, ok := r.data[key]; ok {
			result[key] = v
		}
	}
	return result, nil
}

func (r *gatewayTTLSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	if r == nil {
		return errors.New("setting repo is nil")
	}
	if r.data == nil {
		r.data = map[string]string{}
	}
	for key, value := range settings {
		r.data[key] = value
	}
	return nil
}

func (r *gatewayTTLSettingRepo) GetAll(context.Context) (map[string]string, error) {
	result := make(map[string]string)
	if r == nil {
		return result, nil
	}
	for key, value := range r.data {
		result[key] = value
	}
	return result, nil
}

func (r *gatewayTTLSettingRepo) Delete(_ context.Context, key string) error {
	if r != nil {
		delete(r.data, key)
	}
	return nil
}

func assertJSONTokenOrder(t *testing.T, body string, tokens ...string) {
	t.Helper()

	last := -1
	for _, token := range tokens {
		pos := strings.Index(body, token)
		require.NotEqualf(t, -1, pos, "missing token %s in body %s", token, body)
		require.Greaterf(t, pos, last, "token %s should appear after previous tokens in body %s", token, body)
		last = pos
	}
}

func TestReplaceModelInBody_PreservesTopLevelFieldOrder(t *testing.T) {
	svc := &GatewayService{}
	body := []byte(`{"alpha":1,"model":"claude-3-5-sonnet-latest","messages":[],"omega":2}`)

	result := svc.replaceModelInBody(body, "claude-3-5-sonnet-20241022")
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"model"`, `"messages"`, `"omega"`)
	require.Contains(t, resultStr, `"model":"claude-3-5-sonnet-20241022"`)
}

func TestNormalizeClaudeOAuthRequestBody_PreservesTopLevelFieldOrder(t *testing.T) {
	body := []byte(`{"alpha":1,"model":"claude-3-5-sonnet-latest","temperature":0.2,"system":"You are OpenCode, the best coding agent on the planet.","messages":[],"tool_choice":{"type":"auto"},"omega":2}`)

	result, modelID := normalizeClaudeOAuthRequestBody(body, "claude-3-5-sonnet-latest", claudeOAuthNormalizeOptions{
		injectMetadata: true,
		metadataUserID: "user-1",
	})
	resultStr := string(result)

	require.Equal(t, claude.NormalizeModelID("claude-3-5-sonnet-latest"), modelID)
	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"model"`, `"temperature"`, `"system"`, `"messages"`, `"omega"`, `"tools"`, `"metadata"`, `"max_tokens"`)
	require.Contains(t, resultStr, `"temperature":0.2`)
	require.NotContains(t, resultStr, `"tool_choice"`)
	require.Contains(t, resultStr, `"system":"`+claudeCodeSystemPrompt+`"`)
	require.Contains(t, resultStr, `"tools":[]`)
	require.Contains(t, resultStr, `"metadata":{"user_id":"user-1"}`)
	require.Contains(t, resultStr, `"max_tokens":128000`)
}

func TestInjectClaudeCodePrompt_PreservesFieldOrder(t *testing.T) {
	body := []byte(`{"alpha":1,"system":[{"id":"block-1","type":"text","text":"Custom"}],"messages":[],"omega":2}`)

	result := injectClaudeCodePrompt(body, []any{
		map[string]any{"id": "block-1", "type": "text", "text": "Custom"},
	})
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"system"`, `"messages"`, `"omega"`)
	require.Contains(t, resultStr, `{"id":"block-1","type":"text","text":"`+claudeCodeSystemPrompt+`\n\nCustom"}`)
}

func TestEnforceCacheControlLimit_PreservesTopLevelFieldOrder(t *testing.T) {
	body := []byte(`{"alpha":1,"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"s2","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"m1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"m2","cache_control":{"type":"ephemeral"}},{"type":"text","text":"m3","cache_control":{"type":"ephemeral"}}]}],"omega":2}`)

	result := enforceCacheControlLimit(body)
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"system"`, `"messages"`, `"omega"`)
	require.Equal(t, 4, strings.Count(resultStr, `"cache_control"`))
}

func TestEnforceCacheControlLimit_CountsToolsAndPreservesMessageAnchorsFirst(t *testing.T) {
	body := []byte(`{"alpha":1,"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"m1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"m2","cache_control":{"type":"ephemeral"}},{"type":"text","text":"m3","cache_control":{"type":"ephemeral"}}]}],"tools":[{"name":"a","input_schema":{},"cache_control":{"type":"ephemeral"}}],"omega":2}`)

	result := enforceCacheControlLimit(body)
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"system"`, `"messages"`, `"tools"`, `"omega"`)
	require.Equal(t, 4, strings.Count(resultStr, `"cache_control"`))
	require.True(t, gjson.GetBytes(result, "system.0.cache_control").Exists())
	require.True(t, gjson.GetBytes(result, "messages.0.content.0.cache_control").Exists())
	require.True(t, gjson.GetBytes(result, "messages.0.content.1.cache_control").Exists())
	require.True(t, gjson.GetBytes(result, "messages.0.content.2.cache_control").Exists())
	require.False(t, gjson.GetBytes(result, "tools.0.cache_control").Exists())
}

func TestInjectAnthropicCacheControlTTL1h_OnlyUpdatesExistingEphemeralCacheControl(t *testing.T) {
	body := []byte(`{"alpha":1,"cache_control":{"type":"ephemeral"},"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral","ttl":"5m"}},{"type":"text","text":"plain"}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}},{"type":"text","text":"non","cache_control":{"type":"persistent","ttl":"5m"}}]}],"tools":[{"name":"a","input_schema":{},"cache_control":{"type":"ephemeral"}}],"omega":2}`)

	result := injectAnthropicCacheControlTTL1h(body)
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"cache_control"`, `"system"`, `"messages"`, `"tools"`, `"omega"`)
	require.Equal(t, "1h", gjson.GetBytes(result, "cache_control.ttl").String())
	require.Equal(t, "1h", gjson.GetBytes(result, "system.0.cache_control.ttl").String())
	require.False(t, gjson.GetBytes(result, "system.1.cache_control").Exists())
	require.Equal(t, "1h", gjson.GetBytes(result, "messages.0.content.0.cache_control.ttl").String())
	require.Equal(t, "5m", gjson.GetBytes(result, "messages.0.content.1.cache_control.ttl").String())
	require.Equal(t, "1h", gjson.GetBytes(result, "tools.0.cache_control.ttl").String())
}

func TestGatewayCacheTTLGlobalSetting_TargetResolution(t *testing.T) {
	repo := &gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableAnthropicCacheTTL1hInjection: "true",
	}}
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	svc := &GatewayService{
		settingService: NewSettingService(repo, &config.Config{}),
	}
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	target, ok := svc.resolveCacheTTLUsageOverrideTarget(context.Background(), account)
	require.True(t, ok)
	require.Equal(t, cacheTTLTarget5m, target)

	account.Extra = map[string]any{
		"cache_ttl_override_enabled": true,
		"cache_ttl_override_target":  "1h",
	}
	target, ok = svc.resolveCacheTTLUsageOverrideTarget(context.Background(), account)
	require.True(t, ok)
	require.Equal(t, cacheTTLTarget1h, target)
}

func TestGatewayCacheTTLGlobalSetting_RequestInjectionScope(t *testing.T) {
	repo := &gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableAnthropicCacheTTL1hInjection: "true",
	}}
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	svc := &GatewayService{
		settingService: NewSettingService(repo, &config.Config{}),
	}

	require.True(t, svc.shouldInjectAnthropicCacheTTL1h(context.Background(), &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}))
	require.True(t, svc.shouldInjectAnthropicCacheTTL1h(context.Background(), &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken}))
	require.False(t, svc.shouldInjectAnthropicCacheTTL1h(context.Background(), &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}))
	require.False(t, svc.shouldInjectAnthropicCacheTTL1h(context.Background(), &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}))

	repo.data[SettingKeyEnableAnthropicCacheTTL1hInjection] = "false"
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	require.False(t, svc.shouldInjectAnthropicCacheTTL1h(context.Background(), &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}))
}

// ---- merged from gateway_cached_tokens_test.go ----
// ---------- reconcileCachedTokens 单元测试 ----------

func TestReconcileCachedTokens_NilUsage(t *testing.T) {
	assert.False(t, reconcileCachedTokens(nil))
}

func TestReconcileCachedTokens_AlreadyHasCacheRead(t *testing.T) {
	// 已有标准字段，不应覆盖
	usage := map[string]any{
		"cache_read_input_tokens": float64(100),
		"cached_tokens":           float64(50),
	}
	assert.False(t, reconcileCachedTokens(usage))
	assert.Equal(t, float64(100), usage["cache_read_input_tokens"])
}

func TestReconcileCachedTokens_KimiStyle(t *testing.T) {
	// Kimi 风格：cache_read_input_tokens=0，cached_tokens>0
	usage := map[string]any{
		"input_tokens":                float64(23),
		"cache_creation_input_tokens": float64(0),
		"cache_read_input_tokens":     float64(0),
		"cached_tokens":               float64(23),
	}
	assert.True(t, reconcileCachedTokens(usage))
	assert.Equal(t, float64(23), usage["cache_read_input_tokens"])
}

func TestReconcileCachedTokens_NoCachedTokens(t *testing.T) {
	// 无 cached_tokens 字段（原生 Claude）
	usage := map[string]any{
		"input_tokens":                float64(100),
		"cache_read_input_tokens":     float64(0),
		"cache_creation_input_tokens": float64(0),
	}
	assert.False(t, reconcileCachedTokens(usage))
	assert.Equal(t, float64(0), usage["cache_read_input_tokens"])
}

func TestReconcileCachedTokens_CachedTokensZero(t *testing.T) {
	// cached_tokens 为 0，不应覆盖
	usage := map[string]any{
		"cache_read_input_tokens": float64(0),
		"cached_tokens":           float64(0),
	}
	assert.False(t, reconcileCachedTokens(usage))
	assert.Equal(t, float64(0), usage["cache_read_input_tokens"])
}

func TestReconcileCachedTokens_MissingCacheReadField(t *testing.T) {
	// cache_read_input_tokens 字段完全不存在，cached_tokens > 0
	usage := map[string]any{
		"cached_tokens": float64(42),
	}
	assert.True(t, reconcileCachedTokens(usage))
	assert.Equal(t, float64(42), usage["cache_read_input_tokens"])
}

// ---------- 流式 message_start 事件 reconcile 测试 ----------

func TestStreamingReconcile_MessageStart(t *testing.T) {
	// 模拟 Kimi 返回的 message_start SSE 事件
	eventJSON := `{
		"type": "message_start",
		"message": {
			"id": "msg_123",
			"type": "message",
			"role": "assistant",
			"model": "kimi",
			"usage": {
				"input_tokens": 23,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens": 0,
				"cached_tokens": 23
			}
		}
	}`

	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(eventJSON), &event))

	eventType, _ := event["type"].(string)
	require.Equal(t, "message_start", eventType)

	// 模拟 processSSEEvent 中的 reconcile 逻辑
	if msg, ok := event["message"].(map[string]any); ok {
		if u, ok := msg["usage"].(map[string]any); ok {
			reconcileCachedTokens(u)
		}
	}

	// 验证 cache_read_input_tokens 已被填充
	msg, ok := event["message"].(map[string]any)
	require.True(t, ok)
	usage, ok := msg["usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(23), usage["cache_read_input_tokens"])

	// 验证重新序列化后 JSON 也包含正确值
	data, err := json.Marshal(event)
	require.NoError(t, err)
	assert.Equal(t, int64(23), gjson.GetBytes(data, "message.usage.cache_read_input_tokens").Int())
}

func TestStreamingReconcile_MessageStart_NativeClaude(t *testing.T) {
	// 原生 Claude 不返回 cached_tokens，reconcile 不应改变任何值
	eventJSON := `{
		"type": "message_start",
		"message": {
			"usage": {
				"input_tokens": 100,
				"cache_creation_input_tokens": 50,
				"cache_read_input_tokens": 30
			}
		}
	}`

	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(eventJSON), &event))

	if msg, ok := event["message"].(map[string]any); ok {
		if u, ok := msg["usage"].(map[string]any); ok {
			reconcileCachedTokens(u)
		}
	}

	msg, ok := event["message"].(map[string]any)
	require.True(t, ok)
	usage, ok := msg["usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(30), usage["cache_read_input_tokens"])
}

// ---------- 流式 message_delta 事件 reconcile 测试 ----------

func TestStreamingReconcile_MessageDelta(t *testing.T) {
	// 模拟 Kimi 返回的 message_delta SSE 事件
	eventJSON := `{
		"type": "message_delta",
		"usage": {
			"output_tokens": 7,
			"cache_read_input_tokens": 0,
			"cached_tokens": 15
		}
	}`

	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(eventJSON), &event))

	eventType, _ := event["type"].(string)
	require.Equal(t, "message_delta", eventType)

	// 模拟 processSSEEvent 中的 reconcile 逻辑
	usage, ok := event["usage"].(map[string]any)
	require.True(t, ok)
	reconcileCachedTokens(usage)
	assert.Equal(t, float64(15), usage["cache_read_input_tokens"])
}

func TestStreamingReconcile_MessageDelta_NativeClaude(t *testing.T) {
	// 原生 Claude 的 message_delta 通常没有 cached_tokens
	eventJSON := `{
		"type": "message_delta",
		"usage": {
			"output_tokens": 50
		}
	}`

	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(eventJSON), &event))

	usage, ok := event["usage"].(map[string]any)
	require.True(t, ok)
	reconcileCachedTokens(usage)
	_, hasCacheRead := usage["cache_read_input_tokens"]
	assert.False(t, hasCacheRead, "不应为原生 Claude 响应注入 cache_read_input_tokens")
}

// ---------- 非流式响应 reconcile 测试 ----------

func TestNonStreamingReconcile_KimiResponse(t *testing.T) {
	// 模拟 Kimi 非流式响应
	body := []byte(`{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "hello"}],
		"model": "kimi",
		"usage": {
			"input_tokens": 23,
			"output_tokens": 7,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens": 0,
			"cached_tokens": 23,
			"prompt_tokens": 23,
			"completion_tokens": 7
		}
	}`)

	// 模拟 handleNonStreamingResponse 中的逻辑
	var response struct {
		Usage ClaudeUsage `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(body, &response))

	// reconcile
	if response.Usage.CacheReadInputTokens == 0 {
		cachedTokens := gjson.GetBytes(body, "usage.cached_tokens").Int()
		if cachedTokens > 0 {
			response.Usage.CacheReadInputTokens = int(cachedTokens)
			if newBody, err := sjson.SetBytes(body, "usage.cache_read_input_tokens", cachedTokens); err == nil {
				body = newBody
			}
		}
	}

	// 验证内部 usage（计费用）
	assert.Equal(t, 23, response.Usage.CacheReadInputTokens)
	assert.Equal(t, 23, response.Usage.InputTokens)
	assert.Equal(t, 7, response.Usage.OutputTokens)

	// 验证返回给客户端的 JSON body
	assert.Equal(t, int64(23), gjson.GetBytes(body, "usage.cache_read_input_tokens").Int())
}

func TestNonStreamingReconcile_NativeClaude(t *testing.T) {
	// 原生 Claude 响应：cache_read_input_tokens 已有值
	body := []byte(`{
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cache_creation_input_tokens": 20,
			"cache_read_input_tokens": 30
		}
	}`)

	var response struct {
		Usage ClaudeUsage `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(body, &response))

	// CacheReadInputTokens == 30，条件不成立，整个 reconcile 分支不会执行
	assert.NotZero(t, response.Usage.CacheReadInputTokens)
	assert.Equal(t, 30, response.Usage.CacheReadInputTokens)
}

func TestNonStreamingReconcile_NoCachedTokens(t *testing.T) {
	// 没有 cached_tokens 字段
	body := []byte(`{
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens": 0
		}
	}`)

	var response struct {
		Usage ClaudeUsage `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(body, &response))

	if response.Usage.CacheReadInputTokens == 0 {
		cachedTokens := gjson.GetBytes(body, "usage.cached_tokens").Int()
		if cachedTokens > 0 {
			response.Usage.CacheReadInputTokens = int(cachedTokens)
			if newBody, err := sjson.SetBytes(body, "usage.cache_read_input_tokens", cachedTokens); err == nil {
				body = newBody
			}
		}
	}

	// cache_read_input_tokens 应保持为 0
	assert.Equal(t, 0, response.Usage.CacheReadInputTokens)
	assert.Equal(t, int64(0), gjson.GetBytes(body, "usage.cache_read_input_tokens").Int())
}

// ---- merged from gateway_cli_version_runtime_test.go ----
// withCLIVersionResolverForTest 注入解析器并在用例结束时还原（置 nil），避免用例间污染。
func withCLIVersionResolverForTest(t *testing.T, resolver func() string) {
	t.Helper()
	claude.SetCLIVersionResolver(resolver)
	t.Cleanup(func() { claude.SetCLIVersionResolver(nil) })
}

// 一致性铁律（d）：模拟一次 OAuth + mimic 的请求构建（messages 与 count_tokens 两条
// 出站路径），断言出站 User-Agent 头里的版本号与请求体 x-anthropic-billing-header
// 中 cc_version 的版本号相同，且都等于运行期注入的版本号。头/体不一致会被 Anthropic
// 判为非正版客户端。
func TestBuildOAuthMimicRequest_RuntimeVersionConsistentBetweenHeaderAndBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const upgraded = "9.9.9"
	withCLIVersionResolverForTest(t, func() string { return upgraded })

	for _, endpoint := range []string{"messages", "count_tokens"} {
		t.Run(endpoint, func(t *testing.T) {
			resetGatewayForwardingSettingsCacheForTest(t)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := []byte(`{"model":"claude-haiku-4-5","system":[{"type":"text","text":""}],"messages":[{"role":"user","content":"hello world"}]}`)
			billing, err := buildBillingAttributionText(body, "2.1.81")
			require.NoError(t, err)
			body, err = sjson.SetBytes(body, "system.0.text", billing)
			require.NoError(t, err)

			svc := &GatewayService{cfg: &config.Config{}}
			account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

			var req *http.Request
			var wireBody []byte
			if endpoint == "messages" {
				req, wireBody, err = svc.buildUpstreamRequest(context.Background(), c, account,
					body, "test-token", "oauth", "claude-haiku-4-5", false, true)
			} else {
				req, wireBody, err = svc.buildCountTokensRequest(context.Background(), c, account,
					body, "test-token", "oauth", "claude-haiku-4-5", true)
			}
			require.NoError(t, err)

			wantUA := "claude-cli/" + upgraded + " (external, cli)"
			outboundUA := getHeaderRaw(req.Header, "User-Agent")
			require.Equal(t, wantUA, outboundUA)

			billingText := gjson.GetBytes(wireBody, "system.0.text").String()
			require.Contains(t, billingText, "x-anthropic-billing-header")
			require.Contains(t, billingText, "cc_version="+upgraded+"."+computeClaudeCodeFingerprint(wireBody, upgraded)+";")
			require.NotContains(t, billingText, "cc_version=2.1.81")

			// 头/体版本号必须来自同一字符串。
			headerVersion := ExtractCLIVersion(outboundUA)
			require.Equal(t, upgraded, headerVersion)
			require.Contains(t, billingText, "cc_version="+headerVersion+".")
		})
	}
}

// e. 注入更高版本后，floorClaudeCLIUserAgentVersion 把存量低版本指纹抬升到新版本；
//
//	等于或高于新版本的指纹保持不动（只升不降）。
func TestFloorClaudeCLIUserAgentVersion_UsesRuntimeVersion(t *testing.T) {
	const upgraded = "9.9.9"
	withCLIVersionResolverForTest(t, func() string { return upgraded })

	floored, changed := floorClaudeCLIUserAgentVersion("claude-cli/2.1.100 (external, cli)")
	require.True(t, changed)
	require.Equal(t, "claude-cli/"+upgraded+" (external, cli)", floored)

	// 等于运行期版本：不动。
	same, changed := floorClaudeCLIUserAgentVersion("claude-cli/" + upgraded + " (external, cli)")
	require.False(t, changed)
	require.Equal(t, "claude-cli/"+upgraded+" (external, cli)", same)

	// 高于运行期版本：不动。
	greater, changed := floorClaudeCLIUserAgentVersion("claude-cli/99.0.0 (external, cli)")
	require.False(t, changed)
	require.Equal(t, "claude-cli/99.0.0 (external, cli)", greater)

	// resolver 返回非法值时回退内置基线，floor 语义仍然成立。
	withCLIVersionResolverForTest(t, func() string { return "abc" })
	baselineFloored, changed := floorClaudeCLIUserAgentVersion("claude-cli/1.0.0 (external, cli)")
	require.True(t, changed)
	require.Equal(t, "claude-cli/"+claude.CLIVersion()+" (external, cli)", baselineFloored)
}

// 注入更高版本后，isAcceptableFingerprintUserAgent 以运行期版本为基准，允许客户端
// 上报的新版本。
func TestIsAcceptableFingerprintUserAgent_UsesRuntimeVersion(t *testing.T) {
	const upgraded = "9.9.9"
	withCLIVersionResolverForTest(t, func() string { return upgraded })
	require.True(t, isAcceptableFingerprintUserAgent("claude-cli/"+upgraded+" (external, cli)"))
	// 允许的最大主版本超前量是 +2（9.9.9 → 11.x）。
	require.True(t, isAcceptableFingerprintUserAgent("claude-cli/11.0.0 (external, cli)"))
	// 超前 +3 会被拒（与静态基线下的既有语义一致，只是基准换成运行期版本）。
	require.False(t, isAcceptableFingerprintUserAgent("claude-cli/12.0.0 (external, cli)"))
}

// defaultFingerprint 现取运行期版本，不再在 init 固化。
func TestDefaultFingerprintUsesRuntimeVersion(t *testing.T) {
	const upgraded = "9.9.9"
	withCLIVersionResolverForTest(t, func() string { return upgraded })
	require.Equal(t, "claude-cli/"+upgraded+" (external, cli)", defaultFingerprint().UserAgent)
}

// ---- merged from gateway_dateline_normalization_test.go ----
// TestGatewayClientDatelineNormalization_Scope covers the account/switch matrix
// for the shouldNormalizeClientDateline gate: Anthropic OAuth/SetupToken pass
// only when the switch is on; API-Key and non-Anthropic platforms are excluded
// unconditionally.
func TestGatewayClientDatelineNormalization_Scope(t *testing.T) {
	repo := &gatewayTTLSettingRepo{data: map[string]string{}}
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	svc := &GatewayService{
		settingService: NewSettingService(repo, &config.Config{}),
	}
	ctx := context.Background()

	// Default (missing key): fallback in parseSettings/cache loader is true.
	require.True(t, svc.shouldNormalizeClientDateline(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}))
	require.True(t, svc.shouldNormalizeClientDateline(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken}))
	require.False(t, svc.shouldNormalizeClientDateline(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}))
	require.False(t, svc.shouldNormalizeClientDateline(ctx, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}))

	// Switch off: no account qualifies.
	repo.data[SettingKeyEnableClientDatelineNormalization] = "false"
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	require.False(t, svc.shouldNormalizeClientDateline(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}))
	require.False(t, svc.shouldNormalizeClientDateline(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken}))

	// Switch back on: OAuth qualifies again.
	repo.data[SettingKeyEnableClientDatelineNormalization] = "true"
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	require.True(t, svc.shouldNormalizeClientDateline(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}))
}

// TestGatewayClientDatelineNormalization_HelperNoRewrite exercises the code
// path used by Forward: the helper must return ok=false when the switch is
// off, when the account is API-Key, when the account is nil, and when the
// body carries no fingerprinted dateline. It must return ok=true and a
// rewritten body when both the switch is on and the account is Anthropic
// OAuth/SetupToken and a rewrite actually happened.
func TestGatewayClientDatelineNormalization_HelperNoRewrite(t *testing.T) {
	repo := &gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableClientDatelineNormalization: "true",
	}}
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	svc := &GatewayService{
		settingService: NewSettingService(repo, &config.Config{}),
	}
	ctx := context.Background()

	dirty := []byte(`{"messages":[{"role":"user","content":"<system-reminder>\nToday’s date is 2026/07/01.\n</system-reminder>"}]}`)
	clean := []byte(`{"messages":[{"role":"user","content":"just hello"}]}`)

	// API-Key account: never rewrites, even with dirty payload.
	next, ok := svc.normalizeClientDatelineIfEnabled(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, dirty)
	require.False(t, ok)
	require.Nil(t, next)

	// Nil account: safe no-op.
	next, ok = svc.normalizeClientDatelineIfEnabled(ctx, nil, dirty)
	require.False(t, ok)
	require.Nil(t, next)

	// OAuth account + clean body: no changes, ok=false.
	next, ok = svc.normalizeClientDatelineIfEnabled(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, clean)
	require.False(t, ok)
	require.Nil(t, next)

	// OAuth account + dirty body: rewritten, ok=true.
	next, ok = svc.normalizeClientDatelineIfEnabled(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, dirty)
	require.True(t, ok)
	require.NotNil(t, next)
	require.Contains(t, string(next), "Today's date is 2026-07-01.")
	require.NotContains(t, string(next), "2026/07/01")
	require.NotContains(t, string(next), "Today’s date is")

	// SetupToken account + dirty body: rewritten, ok=true.
	next, ok = svc.normalizeClientDatelineIfEnabled(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken}, dirty)
	require.True(t, ok)
	require.Contains(t, string(next), "Today's date is 2026-07-01.")

	// Switch off: even OAuth account is not rewritten.
	repo.data[SettingKeyEnableClientDatelineNormalization] = "false"
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	next, ok = svc.normalizeClientDatelineIfEnabled(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, dirty)
	require.False(t, ok)
	require.Nil(t, next)
}

// TestGatewayClientDatelineNormalization_LeavesUserProseUntouched double-checks
// that the pure normalizer never touches content outside <system-reminder>
// blocks. This is an integration guard between the switch-gated helper and
// the pkg/anthropicfp scope contract, tripped by anyone who broadens scope.
func TestGatewayClientDatelineNormalization_LeavesUserProseUntouched(t *testing.T) {
	repo := &gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableClientDatelineNormalization: "true",
	}}
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	svc := &GatewayService{
		settingService: NewSettingService(repo, &config.Config{}),
	}
	ctx := context.Background()

	// User prose that happens to include a fingerprint-looking sentence
	// (outside <system-reminder>) must be preserved byte-for-byte.
	body := []byte(`{"messages":[{"role":"user","content":"I wrote: Today’s date is 2026/07/01. What do you think?"}]}`)
	next, ok := svc.normalizeClientDatelineIfEnabled(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, body)
	require.False(t, ok, "must not rewrite user prose outside <system-reminder>")
	require.Nil(t, next)

	// Direct pure-fn check for redundancy.
	out, hits, changed := anthropicfp.NormalizeDateline(body)
	require.False(t, changed)
	require.Empty(t, hits)
	require.Equal(t, body, out)
}

// ---- merged from gateway_debug_env_test.go ----
func TestParseDebugEnvBool(t *testing.T) {
	t.Run("empty is false", func(t *testing.T) {
		if parseDebugEnvBool("") {
			t.Fatalf("expected false for empty string")
		}
	})

	t.Run("true-like values", func(t *testing.T) {
		for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
			t.Run(value, func(t *testing.T) {
				if !parseDebugEnvBool(value) {
					t.Fatalf("expected true for %q", value)
				}
			})
		}
	})

	t.Run("false-like values", func(t *testing.T) {
		for _, value := range []string{"0", "false", "off", "debug"} {
			t.Run(value, func(t *testing.T) {
				if parseDebugEnvBool(value) {
					t.Fatalf("expected false for %q", value)
				}
			})
		}
	})
}

// ---- merged from gateway_non_streaming_response_test.go ----
type nonJSONTempUnschedAccountRepo struct {
	AccountRepository
	tempUnschedCalls    int
	tempReason          string
	modelRateLimitCalls int
	modelScope          string
	modelReason         string
}

func (r *nonJSONTempUnschedAccountRepo) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, reason string) error {
	r.tempUnschedCalls++
	r.tempReason = reason
	return nil
}

func (r *nonJSONTempUnschedAccountRepo) SetModelRateLimit(_ context.Context, _ int64, scope string, _ time.Time, reason ...string) error {
	r.modelRateLimitCalls++
	r.modelScope = scope
	if len(reason) > 0 {
		r.modelReason = reason[0]
	}
	return nil
}

func TestHandleNonStreamingResponse_NonJSON2xxTriggersFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := []byte("(upstream request failed)")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/plain"},
			"X-Request-Id": []string{"rid-invalid-json"},
		},
		Body: io.NopCloser(bytes.NewReader(body)),
	}
	svc := &GatewayService{
		cfg:              &config.Config{},
		rateLimitService: &RateLimitService{},
	}

	usage, err := svc.handleNonStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, "claude-sonnet-4-6", "claude-sonnet-4-6")

	require.Nil(t, usage)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, body, failoverErr.ResponseBody)
	require.Equal(t, "rid-invalid-json", failoverErr.ResponseHeaders.Get("x-request-id"))
	require.False(t, c.Writer.Written(), "invalid upstream response must not be committed before failover")
}

func TestHandleNonStreamingResponse_ValidJSONUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := []byte(`{"id":"msg_1","type":"message","usage":{"input_tokens":12,"output_tokens":7}}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	svc := &GatewayService{
		cfg:              &config.Config{},
		rateLimitService: &RateLimitService{},
	}

	usage, err := svc.handleNonStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, "claude-sonnet-4-6", "claude-sonnet-4-6")

	require.NoError(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 12, usage.InputTokens)
	require.Equal(t, 7, usage.OutputTokens)
	require.JSONEq(t, string(body), rec.Body.String())
}

func TestHandleNonStreamingResponseAnthropicAPIKeyPassthrough_NonJSON2xxTriggersFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := []byte("(upstream request failed)")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	svc := &GatewayService{cfg: &config.Config{}}

	usage, err := svc.handleNonStreamingResponseAnthropicAPIKeyPassthrough(context.Background(), resp, c, &Account{ID: 2})

	require.Nil(t, usage)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, body, failoverErr.ResponseBody)
	require.False(t, c.Writer.Written(), "invalid passthrough response must not be committed before failover")
}

func TestHandleNonStreamingResponseAnthropicAPIKeyPassthrough_ValidJSONUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := []byte(`{"id":"msg_1","type":"message","usage":{"input_tokens":5,"output_tokens":3}}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	svc := &GatewayService{cfg: &config.Config{}}

	usage, err := svc.handleNonStreamingResponseAnthropicAPIKeyPassthrough(context.Background(), resp, c, &Account{ID: 2})

	require.NoError(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 5, usage.InputTokens)
	require.Equal(t, 3, usage.OutputTokens)
	require.JSONEq(t, string(body), rec.Body.String())
}

func TestHandleNonStreamingResponseAnthropicAPIKeyPassthrough_ForceCacheBillingResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "converts input tokens for downstream billing",
			body: `{"id":"msg_1","type":"message","content":[{"type":"text","text":"unchanged"}],"usage":{"input_tokens":5,"output_tokens":3}}`,
			want: `{"id":"msg_1","type":"message","content":[{"type":"text","text":"unchanged"}],"usage":{"input_tokens":0,"output_tokens":3,"cache_read_input_tokens":5}}`,
		},
		{
			name: "adds to genuine cache reads",
			body: `{"id":"msg_2","type":"message","usage":{"input_tokens":5,"output_tokens":3,"cache_read_input_tokens":7,"cache_creation_input_tokens":11}}`,
			want: `{"id":"msg_2","type":"message","usage":{"input_tokens":0,"output_tokens":3,"cache_read_input_tokens":12,"cache_creation_input_tokens":11}}`,
		},
		{
			name: "zero input leaves response unchanged",
			body: `{"id":"msg_3","type":"message","usage":{"input_tokens":0,"output_tokens":3,"cache_read_input_tokens":7}}`,
			want: `{"id":"msg_3","type":"message","usage":{"input_tokens":0,"output_tokens":3,"cache_read_input_tokens":7}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(tt.body)),
			}
			svc := &GatewayService{cfg: &config.Config{}}

			usage, err := svc.handleNonStreamingResponseAnthropicAPIKeyPassthrough(WithForceCacheBilling(context.Background()), resp, c, &Account{ID: 2})

			require.NoError(t, err)
			require.Equal(t, int(gjson.Get(tt.body, "usage.input_tokens").Int()), usage.InputTokens, "local accounting must retain the unclassified usage")
			require.Equal(t, int(gjson.Get(tt.body, "usage.cache_read_input_tokens").Int()), usage.CacheReadInputTokens, "local accounting must convert exactly once in RecordUsage")
			require.JSONEq(t, tt.want, rec.Body.String())
		})
	}
}

func TestHandleNonStreamingResponse_NonJSON2xxMatchesModelScopedTempUnschedulableRule(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	repo := &nonJSONTempUnschedAccountRepo{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &GatewayService{
		cfg:              &config.Config{},
		rateLimitService: rateLimitService,
	}
	account := &Account{
		ID:       3,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusBadGateway),
					"keywords":         []any{"upstream request failed"},
					"duration_minutes": float64(10),
				},
			},
		},
	}
	body := []byte("(upstream request failed)")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}

	_, err := svc.handleNonStreamingResponse(context.Background(), resp, c, account, "claude-sonnet-4-6", "claude-sonnet-4-6")

	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, body, failoverErr.ResponseBody)
	require.Zero(t, repo.tempUnschedCalls)
	require.Equal(t, 1, repo.modelRateLimitCalls)
	require.Equal(t, "claude-sonnet-4-6", repo.modelScope)
	require.Contains(t, repo.modelReason, `"status_code":502`)
	require.Contains(t, repo.modelReason, `"matched_keyword":"upstream request failed"`)
}

// ---- merged from gateway_oauth_metadata_test.go ----
func TestBuildOAuthMetadataUserID_FallbackWithoutAccountUUID(t *testing.T) {
	svc := &GatewayService{}

	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-5",
		Stream:         true,
		MetadataUserID: "",
	}

	account := &Account{
		ID:    123,
		Type:  AccountTypeOAuth,
		Extra: map[string]any{}, // intentionally missing account_uuid / claude_user_id
	}

	fp := &Fingerprint{ClientID: "deadbeef"} // should be used as user id in legacy format

	got := svc.buildOAuthMetadataUserID(parsed, account, fp)
	require.NotEmpty(t, got)

	// Legacy format: user_{client}_account__session_{uuid}
	re := regexp.MustCompile(`^user_[a-zA-Z0-9]+_account__session_[a-f0-9-]{36}$`)
	require.True(t, re.MatchString(got), "unexpected user_id format: %s", got)
}

func TestBuildOAuthMetadataUserID_UsesAccountUUIDWhenPresent(t *testing.T) {
	svc := &GatewayService{}

	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-5",
		Stream:         true,
		MetadataUserID: "",
	}

	account := &Account{
		ID:   123,
		Type: AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":      "acc-uuid",
			"claude_user_id":    "clientid123",
			"anthropic_user_id": "",
		},
	}

	got := svc.buildOAuthMetadataUserID(parsed, account, nil)
	require.NotEmpty(t, got)

	// New format: user_{client}_account_{account_uuid}_session_{uuid}
	re := regexp.MustCompile(`^user_clientid123_account_acc-uuid_session_[a-f0-9-]{36}$`)
	require.True(t, re.MatchString(got), "unexpected user_id format: %s", got)
}

// TestBuildOAuthMetadataUserID_SessionIDStableAcrossTurns 验证伪装路径合成的
// metadata.user_id 在同一会话多轮请求间保持不变（session_id 稳定），贴近真实 Claude Code
// 进程级稳定的 session。账号 / 指纹 / UA 版本均相同，唯一可能变化的就是 session_id，
// 因此直接比较完整 user_id 字符串即可判定 session_id 是否稳定。
func TestBuildOAuthMetadataUserID_SessionIDStableAcrossTurns(t *testing.T) {
	svc := &GatewayService{}
	account := &Account{ID: 777, Type: AccountTypeOAuth, Extra: map[string]any{"account_uuid": "acc-uuid"}}
	fp := &Fingerprint{ClientID: "clientid777", UserAgent: "claude-cli/2.1.161 (external, cli)"}

	mustParse := func(body string) *ParsedRequest {
		parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformAnthropic)
		require.NoError(t, err)
		return parsed
	}

	round1 := mustParse(`{"model":"claude-sonnet-4-5","system":"sys","messages":[` +
		`{"role":"user","content":"first question"}]}`)
	round2 := mustParse(`{"model":"claude-sonnet-4-5","system":"sys","messages":[` +
		`{"role":"user","content":"first question"},` +
		`{"role":"assistant","content":"answer 1"},` +
		`{"role":"user","content":"second question"}]}`)
	round3 := mustParse(`{"model":"claude-sonnet-4-5","system":"sys","messages":[` +
		`{"role":"user","content":"first question"},` +
		`{"role":"assistant","content":"answer 1"},` +
		`{"role":"user","content":"second question"},` +
		`{"role":"assistant","content":"answer 2"},` +
		`{"role":"user","content":"third question"}]}`)

	id1 := svc.buildOAuthMetadataUserID(round1, account, fp)
	id2 := svc.buildOAuthMetadataUserID(round2, account, fp)
	id3 := svc.buildOAuthMetadataUserID(round3, account, fp)

	require.NotEmpty(t, id1)
	require.Equal(t, id1, id2, "session_id 应随对话增长保持不变")
	require.Equal(t, id2, id3, "session_id 应跨所有轮次保持不变")

	// 不同的首条 user 消息应派生出不同的 session_id（不同会话）。
	other := mustParse(`{"model":"claude-sonnet-4-5","system":"sys","messages":[` +
		`{"role":"user","content":"a completely different opener"}]}`)
	idOther := svc.buildOAuthMetadataUserID(other, account, fp)
	require.NotEqual(t, id1, idOther, "不同首条消息应派生不同 session_id")
}

// ---- merged from gateway_pool_mode_retry_test.go ----
func TestGatewayCompatPoolMode429AllowsSameAccountRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name string
		path string
		body []byte
		call func(*GatewayService, context.Context, *gin.Context, *Account, []byte) (*ForwardResult, error)
	}{
		{
			name: "chat completions",
			path: "/v1/chat/completions",
			body: []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`),
			call: func(svc *GatewayService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
				return svc.ForwardAsChatCompletions(ctx, c, account, body, nil)
			},
		},
		{
			name: "responses",
			path: "/v1/responses",
			body: []byte(`{"model":"claude-sonnet-4-5","input":"hello"}`),
			call: func(svc *GatewayService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
				return svc.ForwardAsResponses(ctx, c, account, body, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"X-Request-Id": []string{"pool-429"}},
				Body:       io.NopCloser(http.NoBody),
			}}}
			svc := &GatewayService{
				cfg:                 &config.Config{},
				httpUpstream:        upstream,
				tlsFPProfileService: &TLSFingerprintProfileService{},
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, nil)
			account := &Account{
				ID:       1,
				Name:     "pool-account",
				Platform: PlatformAnthropic,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"api_key":   "test-key",
					"pool_mode": true,
				},
			}

			result, err := tt.call(svc, context.Background(), c, account, tt.body)
			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
			require.True(t, failoverErr.RetryableOnSameAccount)
			require.Equal(t, 1, upstream.callCount)
			require.Empty(t, recorder.Body.String())
		})
	}
}

// ---- merged from gateway_sanitize_test.go ----
func TestSanitizeOpenCodeText_RewritesCanonicalSentence(t *testing.T) {
	in := "You are OpenCode, the best coding agent on the planet."
	got := sanitizeSystemText(in)
	require.Equal(t, strings.TrimSpace(claudeCodeSystemPrompt), got)
}

// ---- merged from gateway_service_bedrock_model_support_test.go ----
func TestGatewayServiceIsModelSupportedByAccount_BedrockDefaultMappingRestrictsModels(t *testing.T) {
	svc := &GatewayService{}
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeBedrock,
		Credentials: map[string]any{
			"aws_region": "us-east-1",
		},
	}

	if !svc.isModelSupportedByAccount(account, "claude-sonnet-4-5") {
		t.Fatalf("expected default Bedrock alias to be supported")
	}

	if svc.isModelSupportedByAccount(account, "claude-3-5-sonnet-20241022") {
		t.Fatalf("expected unsupported alias to be rejected for Bedrock account")
	}
}

func TestGatewayServiceIsModelSupportedByAccount_BedrockCustomMappingStillActsAsAllowlist(t *testing.T) {
	svc := &GatewayService{}
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeBedrock,
		Credentials: map[string]any{
			"aws_region": "eu-west-1",
			"model_mapping": map[string]any{
				"claude-sonnet-*": "claude-sonnet-4-6",
			},
		},
	}

	if !svc.isModelSupportedByAccount(account, "claude-sonnet-4-6") {
		t.Fatalf("expected matched custom mapping to be supported")
	}

	if !svc.isModelSupportedByAccount(account, "claude-opus-4-6") {
		t.Fatalf("expected default Bedrock alias fallback to remain supported")
	}

	if svc.isModelSupportedByAccount(account, "claude-3-5-sonnet-20241022") {
		t.Fatalf("expected unsupported model to still be rejected")
	}
}

// ---- merged from gateway_service_selection_failure_stats_test.go ----
func TestCollectSelectionFailureStats(t *testing.T) {
	svc := &GatewayService{}
	model := "gpt-5.4"
	resetAt := time.Now().Add(2 * time.Minute).Format(time.RFC3339)

	accounts := []Account{
		// excluded
		{
			ID:          1,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: true,
		},
		// unschedulable
		{
			ID:          2,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: false,
		},
		// platform filtered
		{
			ID:          3,
			Platform:    PlatformAntigravity,
			Status:      StatusActive,
			Schedulable: true,
		},
		// model unsupported
		{
			ID:          4,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"model_mapping": map[string]any{
					"gpt-image": "gpt-image",
				},
			},
		},
		// model rate limited
		{
			ID:          5,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: true,
			Extra: map[string]any{
				"model_rate_limits": map[string]any{
					model: map[string]any{
						"rate_limit_reset_at": resetAt,
					},
				},
			},
		},
		// eligible
		{
			ID:          6,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: true,
		},
	}

	excluded := map[int64]struct{}{1: {}}
	stats := svc.collectSelectionFailureStats(context.Background(), accounts, model, PlatformOpenAI, excluded, false)

	if stats.Total != 6 {
		t.Fatalf("total=%d want=6", stats.Total)
	}
	if stats.Excluded != 1 {
		t.Fatalf("excluded=%d want=1", stats.Excluded)
	}
	if stats.Unschedulable != 1 {
		t.Fatalf("unschedulable=%d want=1", stats.Unschedulable)
	}
	if stats.PlatformFiltered != 1 {
		t.Fatalf("platform_filtered=%d want=1", stats.PlatformFiltered)
	}
	if stats.ModelUnsupported != 1 {
		t.Fatalf("model_unsupported=%d want=1", stats.ModelUnsupported)
	}
	if stats.ModelRateLimited != 1 {
		t.Fatalf("model_rate_limited=%d want=1", stats.ModelRateLimited)
	}
	if stats.Eligible != 1 {
		t.Fatalf("eligible=%d want=1", stats.Eligible)
	}
}

func TestDiagnoseSelectionFailure_UnschedulableDetail(t *testing.T) {
	svc := &GatewayService{}
	acc := &Account{
		ID:          7,
		Platform:    PlatformOpenAI,
		Status:      StatusActive,
		Schedulable: false,
	}

	diagnosis := svc.diagnoseSelectionFailure(context.Background(), acc, "gpt-5.4", PlatformOpenAI, map[int64]struct{}{}, false)
	if diagnosis.Category != "unschedulable" {
		t.Fatalf("category=%s want=unschedulable", diagnosis.Category)
	}
	if diagnosis.Detail != "generic_unschedulable" {
		t.Fatalf("detail=%s want=generic_unschedulable", diagnosis.Detail)
	}
}

func TestDiagnoseSelectionFailure_ModelRateLimitedDetail(t *testing.T) {
	svc := &GatewayService{}
	model := "gpt-5.4"
	resetAt := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339)
	acc := &Account{
		ID:          8,
		Platform:    PlatformOpenAI,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			"model_rate_limits": map[string]any{
				model: map[string]any{
					"rate_limit_reset_at": resetAt,
				},
			},
		},
	}

	diagnosis := svc.diagnoseSelectionFailure(context.Background(), acc, model, PlatformOpenAI, map[int64]struct{}{}, false)
	if diagnosis.Category != "model_rate_limited" {
		t.Fatalf("category=%s want=model_rate_limited", diagnosis.Category)
	}
	if !strings.Contains(diagnosis.Detail, "remaining=") {
		t.Fatalf("detail=%s want contains remaining=", diagnosis.Detail)
	}
}

// ---- merged from gateway_service_streaming_test.go ----
type upstreamContextTestKey string

func newStreamingResponseTestGatewayService() *GatewayService {
	return &GatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				StreamDataIntervalTimeout: 0,
				MaxLineSize:               defaultMaxLineSize,
			},
		},
		rateLimitService: &RateLimitService{},
	}
}

func TestGatewayService_StreamingReusesScannerBufferAndStillParsesUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStreamingResponseTestGatewayService()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		// Minimal SSE event to trigger parseSSEUsage
		_, _ = pw.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n"))
		_, _ = pw.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n"))
		_, _ = pw.Write([]byte("data: [DONE]\n\n"))
	}()

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.usage)
	require.Equal(t, 3, result.usage.InputTokens)
	require.Equal(t, 7, result.usage.OutputTokens)
}

func TestGatewayService_StreamingKeepaliveUsesIdleTimer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStreamingResponseTestGatewayService()
	svc.cfg.Gateway.StreamKeepaliveInterval = 1

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = pw.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}()

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "event: ping")
}

func TestGatewayService_StreamingKeepaliveUsesNoopDeltaForAffectedClaudeCodeVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStreamingResponseTestGatewayService()
	svc.cfg.Gateway.StreamKeepaliveInterval = 1

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.198 (external, cli)")

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"))
		_, _ = pw.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = pw.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
		_, _ = pw.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}()

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	body := rec.Body.String()
	require.Contains(t, body, "event: content_block_delta")
	require.Contains(t, body, `"delta":{"type":"text_delta","text":""}`)
}

func TestGatewayService_StreamingKeepaliveUsesNoopDeltaDuringToolUseForAffectedClaudeCodeVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStreamingResponseTestGatewayService()
	svc.cfg.Gateway.StreamKeepaliveInterval = 1

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.198 (external, cli)")

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"))
		_, _ = pw.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"Edit\",\"input\":{}}}\n\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = pw.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"))
		_, _ = pw.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}()

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	body := rec.Body.String()
	require.Contains(t, body, "event: content_block_delta")
	require.Contains(t, body, `"index":1`)
	require.Contains(t, body, `"delta":{"type":"input_json_delta","partial_json":""}`)
}

func TestGatewayService_StreamingKeepaliveKeepsPingForOlderClaudeCodeVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStreamingResponseTestGatewayService()
	svc.cfg.Gateway.StreamKeepaliveInterval = 1

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.187 (external, cli)")

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"))
		_, _ = pw.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = pw.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
		_, _ = pw.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}()

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	body := rec.Body.String()
	require.Contains(t, body, "event: ping")
	require.NotContains(t, body, `"delta":{"type":"text_delta","text":""}`)
}

func TestDetachUpstreamContextIgnoresClientCancel(t *testing.T) {
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), upstreamContextTestKey("test-key"), "test-value"))
	upstreamCtx, release := detachUpstreamContext(parent)
	defer release()

	cancel()

	require.NoError(t, upstreamCtx.Err())
	require.Equal(t, "test-value", upstreamCtx.Value(upstreamContextTestKey("test-key")))
}

// ---- merged from gateway_sonnet55_toolset_beta_test.go ----
func TestSonnet55ToolsetDropsLegacyStreamingBeta(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{InjectBetaForAPIKey: true}}}
	for _, tc := range []struct {
		name     string
		model    string
		toolType string
		wantDrop bool
	}{
		{"computer toolset", "claude-sonnet-5-5", "computer_toolset_20260801", true},
		{"browser toolset", "claude-sonnet-5-5", "browser_toolset_20260801", true},
		{"legacy computer", "claude-sonnet-5-5", "computer_20251124", false},
		{"older model", "claude-sonnet-5", "computer_toolset_20260801", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"` + tc.model + `","tools":[{"type":"` + tc.toolType + `"}],"messages":[{"role":"user","content":"hi"}]}`)
			headers := http.Header{}
			headers.Set("anthropic-beta", claude.BetaFineGrainedToolStreaming+",context-1m-2025-08-07")
			for _, tokens := range []struct {
				name       string
				count      bool
				clientBeta bool
			}{
				{"explicit messages", false, true},
				{"injected messages", false, false},
				{"explicit count_tokens", true, true},
				{"injected count_tokens", true, false},
			} {
				t.Run(tokens.name, func(t *testing.T) {
					requestHeaders := http.Header{}
					if tokens.clientBeta {
						requestHeaders = headers
					}
					var got string
					var set bool
					if tokens.count {
						got, set = svc.computeFinalCountTokensAnthropicBeta("apikey", false, tc.model, requestHeaders, body, nil)
					} else {
						got, set = svc.computeFinalAnthropicBeta("apikey", false, tc.model, requestHeaders, body, nil)
					}
					require.True(t, set)
					require.Equal(t, !tc.wantDrop, containsBetaToken(got, claude.BetaFineGrainedToolStreaming))
					if tokens.clientBeta {
						require.True(t, containsBetaToken(got, claude.BetaContext1M))
					}
				})
			}
		})
	}
}

func TestSonnet55ToolsetBetaFilteredAfterAccountOverrideAndOnVertex(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-5-5","max_tokens":128,"tools":[{"type":"browser_toolset_20260801"}],"messages":[{"role":"user","content":"hi"}]}`)
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides:       map[string]any{"anthropic-beta": claude.BetaFineGrainedToolStreaming + ",context-1m-2025-08-07"},
		}}
	svc := &GatewayService{cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	for _, build := range []struct {
		name string
		fn   func() (*http.Request, error)
	}{
		{"normal", func() (*http.Request, error) {
			req, _, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "key", "apikey", "claude-sonnet-5-5", false, false)
			return req, err
		}},
		{"passthrough", func() (*http.Request, error) {
			req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(context.Background(), c, account, body, "key")
			return req, err
		}},
	} {
		t.Run(build.name, func(t *testing.T) {
			req, err := build.fn()
			require.NoError(t, err)
			header := getHeaderRaw(req.Header, "anthropic-beta")
			require.False(t, containsBetaToken(header, claude.BetaFineGrainedToolStreaming))
			require.True(t, containsBetaToken(header, claude.BetaContext1M))
		})
	}

	vertexContext := newVertexBetaTestContext(t, claude.BetaFineGrainedToolStreaming+","+claude.BetaContext1M)
	req, _, err := svc.buildUpstreamRequest(context.Background(), vertexContext, newVertexServiceAccount(9001), body,
		"vertex-token", "service_account", "claude-sonnet-5-5@20260928", false, false)
	require.NoError(t, err)
	vertexHeader := getHeaderRaw(req.Header, "anthropic-beta")
	require.False(t, containsBetaToken(vertexHeader, claude.BetaFineGrainedToolStreaming))
	require.True(t, containsBetaToken(vertexHeader, claude.BetaContext1M))

	nativeAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides:       map[string]any{"anthropic-beta": claude.BetaFineGrainedToolStreaming + ",context-1m-2025-08-07"},
		}}
	native := &OpenAIGatewayService{}
	nativeReq, _, err := native.buildNativeAnthropicUpstreamRequest(context.Background(), c, nativeAccount, body,
		"key", "https://api.anthropic.com/v1/messages")
	require.NoError(t, err)
	nativeHeader := getHeaderRaw(nativeReq.Header, "anthropic-beta")
	require.False(t, containsBetaToken(nativeHeader, claude.BetaFineGrainedToolStreaming))
	require.True(t, containsBetaToken(nativeHeader, claude.BetaContext1M))
}

// ---- merged from gateway_system_cache_control_test.go ----
// 客户端打在 system 上的 cache_control 是它自己的缓存意图，网关不该替它删掉。
//
// 这些用例守的是 #2369 里被漏掉的那一半：messages 层的同类改写已经收进
// rewrite_message_cache_control 开关（默认不动客户端断点），system 层却一直没有
// 对应的保护——mimic 路径无条件剥离，且不打日志、不报错。
//
// 剥离在两种情形下都没有成立的前提：注入开启时 system 已被整个替换、客户端断点
// 早就不在了；注入关闭时 system 是客户端原样，断点就是它的意图。

// 多块 system 各自带断点时，一块都不能少：Anthropic 允许最多 4 个，
// 真正的上限兜底是 enforceCacheControlLimit 的事，不该在这里提前砍。
func TestNormalizeClaudeOAuthRequestBody_KeepsEveryClientSystemBreakpoint(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","system":[` +
		`{"type":"text","text":"block one","cache_control":{"type":"ephemeral","ttl":"5m"}},` +
		`{"type":"text","text":"block two"},` +
		`{"type":"text","text":"block three","cache_control":{"type":"ephemeral","ttl":"1h"}}` +
		`],"messages":[{"role":"user","content":"hi"}]}`)

	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-sonnet-4-6", claudeOAuthNormalizeOptions{})

	require.Equal(t, "5m", gjson.GetBytes(out, "system.0.cache_control.ttl").String(),
		"客户端选的 TTL 要原样留着")
	require.False(t, gjson.GetBytes(out, "system.1.cache_control").Exists(), "没有的不该凭空长出来")
	require.Equal(t, "1h", gjson.GetBytes(out, "system.2.cache_control.ttl").String())
}

// 保留断点不等于放弃 system 文本的规范化：OpenCode 身份句该改的还得改。
// 这条用例区分「拿掉剥离」与「把整段 normalize 关掉」——后者会让第三方指纹漏上去。
func TestNormalizeClaudeOAuthRequestBody_StillSanitizesTextWhileKeepingBreakpoint(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","system":[{"type":"text","text":"You are OpenCode, the best coding agent on the planet.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`)

	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-sonnet-4-6", claudeOAuthNormalizeOptions{})

	require.Equal(t, strings.TrimSpace(claudeCodeSystemPrompt), gjson.GetBytes(out, "system.0.text").String())
	require.True(t, gjson.GetBytes(out, "system.0.cache_control").Exists(),
		"文本被规范化，断点仍要留着")
}

// 注入开启这条路径上，system 是我们自己拼的；blocks 配置里自带的 cache_control
// 是稳定缓存锚点（对齐真实 CLI 的形态），同样不能在 normalize 里掉。
func TestNormalizeClaudeOAuthRequestBody_KeepsInjectedSystemBreakpoint(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","system":"client instructions","messages":[{"role":"user","content":"hi"}]}`)
	rewritten := rewriteSystemForNonClaudeCode(body, "client instructions")

	out, _ := normalizeClaudeOAuthRequestBody(rewritten, "claude-sonnet-4-6", claudeOAuthNormalizeOptions{})

	// 只断言「注入后的 system 仍带着锚点」，不写死是第几块：
	// 块数与布局由 blocks 配置决定，焊进断言会让配置一改就误报。
	_, _, _, systemPaths := collectCacheControlPaths(out)
	require.NotEmpty(t, systemPaths, "注入路径自带的稳定锚点不该在 normalize 里掉")
}

// count_tokens 是五条出口里唯一没有在自己转发路径上调过 enforceCacheControlLimit 的
// 那条。不再剥离客户端 system 断点之后，客户端自带的断点会和 tools[-1] 上注入的那个
// 叠加，可以直接顶破 4 块上限——而上游对超限是 400。
//
// 用 setup-token 账号构造 mimic 分支：IsOAuth 认它，取 token 只读 credentials，
// 不碰 DB。UA 非 claude-cli 且无 metadata.user_id，于是 shouldMimicClaudeCode 成立。
func TestForwardCountTokens_EnforcesCacheControlLimitOnMimicPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	c.Request.Header.Set("User-Agent", "third-party-client/1.0")

	// 客户端自己打满 4 个断点：system 一个、messages 三个。
	// 加上 mimic 注入的 tools[-1] 与 system blocks 自带的锚点，必然超过 4。
	body := []byte(`{"model":"claude-sonnet-4-6",` +
		`"system":[{"type":"text","text":"stable client prefix","cache_control":{"type":"ephemeral","ttl":"5m"}}],` +
		`"tools":[{"name":"probe","description":"d","input_schema":{"type":"object"}}],` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"one","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"two","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"user","content":[{"type":"text","text":"three","cache_control":{"type":"ephemeral"}}]}` +
		`]}`)
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-6"}

	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"input_tokens":42}`)),
		},
	}
	svc := &GatewayService{
		cfg:              &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream:     upstream,
		rateLimitService: &RateLimitService{},
	}
	account := &Account{
		ID:          401,
		Name:        "count-tokens-ceiling-test",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeSetupToken,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-oauth-token"},
		Status:      StatusActive,
		Schedulable: true,
	}

	require.NoError(t, svc.ForwardCountTokens(context.Background(), c, account, parsed))

	_, messagePaths, toolPaths, systemPaths := collectCacheControlPaths(upstream.lastBody)
	total := len(messagePaths) + len(toolPaths) + len(systemPaths)
	require.LessOrEqual(t, total, maxCacheControlBlocks,
		"出站 body 的 cache_control 块数必须被砍到上限内，实测 %d 块", total)
}

// ---- merged from gateway_usage_billing_simple_mode_test.go ----
func TestBuildUsageBillingCommandSimpleModeOnlyChargesAPIKeyWindows(t *testing.T) {
	apiKey := &APIKey{ID: 13, Quota: 100, RateLimit5h: 10}
	user := &User{ID: 7}
	account := &Account{ID: 9, Type: AccountTypeAPIKey}
	p := &postUsageBillingParams{
		Cost:                       &CostBreakdown{ActualCost: 3.25, TotalCost: 2.5},
		User:                       user,
		APIKey:                     apiKey,
		Account:                    account,
		APIKeyService:              &apiKeyQuotaUpdaterStub{},
		SimpleModeKeyRateLimitOnly: true,
	}

	cmd := buildUsageBillingCommand("simple-req", nil, p)
	require.NotNil(t, cmd)
	require.Zero(t, cmd.BalanceCost)
	require.Zero(t, cmd.SubscriptionCost)
	require.Zero(t, cmd.APIKeyQuotaCost)
	require.Zero(t, cmd.AccountQuotaCost)
	require.Equal(t, 3.25, cmd.APIKeyRateLimitCost)
}

type apiKeyQuotaUpdaterStub struct{}

func (apiKeyQuotaUpdaterStub) UpdateQuotaUsed(context.Context, int64, float64) error { return nil }
func (apiKeyQuotaUpdaterStub) UpdateRateLimitUsage(context.Context, int64, float64) error {
	return nil
}

type simpleModeUsageBillingRepoStub struct {
	UsageBillingRepository
	seen map[string]struct{}
	cmds []*UsageBillingCommand
}

func (s *simpleModeUsageBillingRepoStub) Apply(_ context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	s.cmds = append(s.cmds, cmd)
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}
	key := cmd.RequestID + ":" + strconv.FormatInt(cmd.APIKeyID, 10)
	if _, ok := s.seen[key]; ok {
		return &UsageBillingApplyResult{Applied: false}, nil
	}
	s.seen[key] = struct{}{}
	return &UsageBillingApplyResult{Applied: true}, nil
}

func TestApplyUsageBillingSimpleModeDeduplicatesWithoutBalanceEffects(t *testing.T) {
	repo := &simpleModeUsageBillingRepoStub{}
	p := &postUsageBillingParams{
		Cost:                       &CostBreakdown{ActualCost: 3.25, TotalCost: 3.25},
		User:                       &User{ID: 7, Balance: 0},
		APIKey:                     &APIKey{ID: 13, Quota: 100, RateLimit5h: 10},
		Account:                    &Account{ID: 9, Type: AccountTypeAPIKey},
		APIKeyService:              &apiKeyQuotaUpdaterStub{},
		SimpleModeKeyRateLimitOnly: true,
	}

	first, err := applyUsageBilling(context.Background(), "simple-req", nil, p, &billingDeps{deferredService: &DeferredService{}}, repo)
	require.NoError(t, err)
	require.True(t, first)
	second, err := applyUsageBilling(context.Background(), "simple-req", nil, p, &billingDeps{deferredService: &DeferredService{}}, repo)
	require.NoError(t, err)
	require.False(t, second)
	require.Len(t, repo.cmds, 2)
	for _, cmd := range repo.cmds {
		require.Zero(t, cmd.BalanceCost)
		require.Zero(t, cmd.SubscriptionCost)
		require.Zero(t, cmd.APIKeyQuotaCost)
		require.Zero(t, cmd.AccountQuotaCost)
		require.Equal(t, 3.25, cmd.APIKeyRateLimitCost)
	}
}

func TestApplyUsageBillingSimpleModeRejectsLegacyFallback(t *testing.T) {
	for _, missing := range []string{"repository", "request_id", "api_key"} {
		t.Run(missing, func(t *testing.T) {
			p := &postUsageBillingParams{
				Cost: &CostBreakdown{ActualCost: 1}, User: &User{ID: 7},
				APIKey: &APIKey{ID: 13, RateLimit5h: 10}, Account: &Account{ID: 9},
				SimpleModeKeyRateLimitOnly: true,
			}
			var repo UsageBillingRepository = &simpleModeUsageBillingRepoStub{}
			requestID := "simple-req"
			switch missing {
			case "repository":
				repo = nil
			case "request_id":
				requestID = ""
			case "api_key":
				p.APIKey = nil
			}
			_, err := applyUsageBilling(context.Background(), requestID, nil, p, &billingDeps{deferredService: &DeferredService{}}, repo)
			require.ErrorIs(t, err, ErrSimpleModeKeyRateLimitBillingUnavailable)
		})
	}
}

// A failed cache eviction must not fall through into standard-mode charges.
type simpleModeInvalidationCache struct {
	BillingCache
	invalidated []int64
}

func (c *simpleModeInvalidationCache) InvalidateAPIKeyRateLimit(_ context.Context, keyID int64) error {
	c.invalidated = append(c.invalidated, keyID)
	return errors.New("redis unavailable")
}
func TestFinalizeSimpleModePreservesLastUsedWithoutFinancialCacheWrites(t *testing.T) {
	for _, subscription := range []bool{false, true} {
		t.Run(strconv.FormatBool(subscription), func(t *testing.T) {
			cache := &simpleModeInvalidationCache{}
			writes := make(chan cacheWriteTask, 4)
			deferred := &DeferredService{}
			groupID := int64(5)
			p := &postUsageBillingParams{
				Cost: &CostBreakdown{ActualCost: 3}, User: &User{ID: 7},
				APIKey:  &APIKey{ID: 13, GroupID: &groupID, RateLimit5h: 10},
				Account: &Account{ID: 9}, IsSubscriptionBill: subscription,
				SimpleModeKeyRateLimitOnly: true,
			}
			finalizePostUsageBilling(context.Background(), p, &billingDeps{
				billingCacheService: &BillingCacheService{cache: cache, cacheWriteChan: writes},
				deferredService:     deferred,
			}, &UsageBillingApplyResult{Applied: true})
			require.Equal(t, []int64{13}, cache.invalidated)
			require.Empty(t, writes, "simple mode must not enqueue balance, subscription or window increments")
			_, scheduled := deferred.lastUsedUpdates.Load(int64(9))
			require.True(t, scheduled, "early return must preserve the account activity update")
		})
	}
}
