package controller

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	claudeRelayCatalogModel  = "claude-sonnet-4-6"
	claudeRelayZenModel      = "anthropic/claude-sonnet-4.6"
	claudeRelayInitialQuota  = 100_000
	claudeRelayPreauthorized = 2_813 // ceil(2250 * 1.25)
)

type capturedClaudeUpstreamRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

type claudeUpstreamCapture struct {
	mu       sync.Mutex
	requests []capturedClaudeUpstreamRequest
}

type claudeRelayHostRoutingTransport struct {
	base    http.RoundTripper
	targets map[string]*url.URL
}

func (transport *claudeRelayHostRoutingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	target, ok := transport.targets[request.URL.Host]
	if !ok {
		return transport.base.RoundTrip(request)
	}
	clone := request.Clone(request.Context())
	requestURL := *request.URL
	requestURL.Scheme = target.Scheme
	requestURL.Host = target.Host
	clone.URL = &requestURL
	clone.Host = target.Host
	return transport.base.RoundTrip(clone)
}

func (capture *claudeUpstreamCapture) record(request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.requests = append(capture.requests, capturedClaudeUpstreamRequest{
		Method: request.Method,
		Path:   request.URL.Path,
		Header: request.Header.Clone(),
		Body:   append([]byte(nil), body...),
	})
}

func (capture *claudeUpstreamCapture) snapshot() []capturedClaudeUpstreamRequest {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	requests := make([]capturedClaudeUpstreamRequest, len(capture.requests))
	copy(requests, capture.requests)
	return requests
}

type claudeRelayIntegrationFixture struct {
	db         *gorm.DB
	router     *gin.Engine
	user       model.User
	token      model.Token
	zenChannel model.Channel
	tabChannel model.Channel
	zenCapture *claudeUpstreamCapture
	tabCapture *claudeUpstreamCapture
}

type claudeRelayQuotaAudit struct {
	Remaining int `gorm:"column:remaining"`
	Used      int `gorm:"column:used"`
}

func newClaudeRelayIntegrationFixture(
	t *testing.T,
	zenBehavior func(http.ResponseWriter),
	tabBehavior func(http.ResponseWriter),
) *claudeRelayIntegrationFixture {
	t.Helper()

	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousSQLitePath := common.SQLitePath
	previousUsingSQLite := common.UsingSQLite
	previousUsingMySQL := common.UsingMySQL
	previousUsingPostgreSQL := common.UsingPostgreSQL
	previousRedisEnabled := common.RedisEnabled
	previousMemoryCacheEnabled := common.MemoryCacheEnabled
	previousBatchUpdateEnabled := common.BatchUpdateEnabled
	previousLogConsumeEnabled := common.LogConsumeEnabled
	previousDataExportEnabled := common.DataExportEnabled
	previousAutomaticDisable := common.AutomaticDisableChannelEnabled
	previousRetryTimes := common.RetryTimes
	previousPreConsumedQuota := common.PreConsumedQuota
	previousQuotaPerUnit := common.QuotaPerUnit
	previousCountToken := constant.CountToken
	previousErrorLogEnabled := constant.ErrorLogEnabled
	previousIsMasterNode := common.IsMasterNode
	previousGroupRatios := ratio_setting.GroupRatio2JSONString()
	previousRetryRanges := append([]operation_setting.StatusCodeRange(nil), operation_setting.AutomaticRetryStatusCodeRanges...)
	previousGlobalModelSettings := *model_setting.GetGlobalSettings()
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SQLitePath = previousSQLitePath
		common.UsingSQLite = previousUsingSQLite
		common.UsingMySQL = previousUsingMySQL
		common.UsingPostgreSQL = previousUsingPostgreSQL
		common.RedisEnabled = previousRedisEnabled
		common.MemoryCacheEnabled = previousMemoryCacheEnabled
		common.BatchUpdateEnabled = previousBatchUpdateEnabled
		common.LogConsumeEnabled = previousLogConsumeEnabled
		common.DataExportEnabled = previousDataExportEnabled
		common.AutomaticDisableChannelEnabled = previousAutomaticDisable
		common.RetryTimes = previousRetryTimes
		common.PreConsumedQuota = previousPreConsumedQuota
		common.QuotaPerUnit = previousQuotaPerUnit
		common.IsMasterNode = previousIsMasterNode
		constant.CountToken = previousCountToken
		constant.ErrorLogEnabled = previousErrorLogEnabled
		operation_setting.AutomaticRetryStatusCodeRanges = previousRetryRanges
		*model_setting.GetGlobalSettings() = previousGlobalModelSettings
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroupRatios))
	})

	t.Setenv("SQL_DSN", "")
	t.Setenv("LOG_SQL_DSN", "")
	common.SQLitePath = "file:claude-relay-" + uuid.NewString() + "?mode=memory&cache=shared"
	common.UsingSQLite = false
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	common.BatchUpdateEnabled = false
	common.LogConsumeEnabled = true
	common.DataExportEnabled = false
	common.AutomaticDisableChannelEnabled = false
	common.RetryTimes = 1
	common.PreConsumedQuota = 500
	common.QuotaPerUnit = 500_000
	common.IsMasterNode = false
	constant.CountToken = false
	constant.ErrorLogEnabled = false
	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 500, End: 502}}
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	model_setting.GetGlobalSettings().PassThroughRequestEnabled = false
	model_setting.GetGlobalSettings().ChatCompletionsToResponsesPolicy.Enabled = false

	// Use the production initializer so reserved key/group column quoting follows
	// the same SQLite dialect path exercised by TokenAuth and channel selection.
	require.NoError(t, model.InitDB())
	db := model.DB
	model.LOG_DB = db
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.Token{},
		&model.Channel{},
		&model.Ability{},
		&model.ChannelModelPrice{},
		&model.SubscriptionPreConsumeRecord{},
		&model.Log{},
	))

	zenCapture := &claudeUpstreamCapture{}
	tabCapture := &claudeUpstreamCapture{}
	zenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		zenCapture.record(request)
		zenBehavior(writer)
	}))
	tabServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tabCapture.record(request)
		tabBehavior(writer)
	}))
	t.Cleanup(zenServer.Close)
	t.Cleanup(tabServer.Close)

	user := model.User{
		Username: "claude-relay-user-" + uuid.NewString(),
		Password: "test-password-hash",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Quota:    claudeRelayInitialQuota,
		Group:    "default",
		Setting:  `{"billing_preference":"wallet_only"}`,
	}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId:      user.Id,
		Key:         "clauderelay" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Status:      common.TokenStatusEnabled,
		Name:        "claude-relay-integration",
		CreatedTime: time.Now().Unix(),
		ExpiredTime: -1,
		RemainQuota: claudeRelayInitialQuota,
	}
	require.NoError(t, db.Create(&token).Error)

	zenPriority := int64(600)
	tabPriority := int64(550)
	weight := uint(100)
	autoBan := 1
	zenBaseURL := "https://zenmux.ai/api/anthropic"
	tabBaseURL := "https://api2.tabcode.cc/claude/kiropower"
	zenMapping := `{"claude-sonnet-4-6":"anthropic/claude-sonnet-4.6"}`
	tabMapping := `{"claude-sonnet-4-6":"claude-sonnet-4-6"}`
	zenTag := "mujian-provider:zenmux:chat"
	tabTag := "mujian-provider:tabcode:chat"
	zenHeaderOverride := `{"anthropic-version":"2023-06-01"}`
	tabHeaderOverride := `{"Authorization":"Bearer {api_key}","anthropic-version":"2023-06-01"}`
	testModel := claudeRelayCatalogModel
	now := time.Now().Unix()
	channels := []model.Channel{
		{
			Type: constant.ChannelTypeAnthropic, Key: "zen-test-key", Status: common.ChannelStatusEnabled,
			Name: "幕间 · ZenMux · 对话", Weight: &weight, CreatedTime: time.Now().Unix(), BaseURL: &zenBaseURL,
			Models: claudeRelayCatalogModel, Group: "default", ModelMapping: &zenMapping, Priority: &zenPriority,
			AutoBan: &autoBan, Tag: &zenTag, HeaderOverride: &zenHeaderOverride, TestModel: &testModel, TestTime: now,
		},
		{
			Type: constant.ChannelTypeAnthropic, Key: "tab-test-key", Status: common.ChannelStatusEnabled,
			Name: "幕间 · TabCode Kiro · 对话", Weight: &weight, CreatedTime: time.Now().Unix(), BaseURL: &tabBaseURL,
			Models: claudeRelayCatalogModel, Group: "default", ModelMapping: &tabMapping, Priority: &tabPriority,
			AutoBan: &autoBan, Tag: &tabTag, HeaderOverride: &tabHeaderOverride, TestModel: &testModel, TestTime: now,
		},
	}
	require.NoError(t, db.Create(&channels).Error)
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: "default", Model: claudeRelayCatalogModel, ChannelId: channels[0].Id, Enabled: true, Priority: &zenPriority, Weight: weight, Tag: &zenTag},
		{Group: "default", Model: claudeRelayCatalogModel, ChannelId: channels[1].Id, Enabled: true, Priority: &tabPriority, Weight: weight, Tag: &tabTag},
	}).Error)
	require.NoError(t, db.Create(&[]model.ChannelModelPrice{
		{
			ChannelID: channels[0].Id, CatalogID: claudeRelayCatalogModel, UpstreamModelID: claudeRelayZenModel,
			Provider: "zenmux", BillingType: model.ChannelModelBillingToken, InputPrice: 3, OutputPrice: 15,
			CacheRatio: 0.1, CacheCreationRatio: 1.25, Currency: "USD",
			SourceURL: "https://zenmux.ai/api/anthropic/v1/models", SourceVersion: "zen-integration-fixture",
			Available: true, OriginalPricing: `{"unit":"perMTokens"}`, SyncedAt: now, TestedAt: now,
		},
		{
			ChannelID: channels[1].Id, CatalogID: claudeRelayCatalogModel, UpstreamModelID: claudeRelayCatalogModel,
			Provider: "tabcode", BillingType: model.ChannelModelBillingToken, InputPrice: 2.25, OutputPrice: 11.25,
			CacheRatio: 0.1, CacheCreationRatio: 1.25, Currency: "USD",
			SourceURL: "https://tabcode.cc/api/v1/billing/channels", SourceVersion: "kiro-0.75",
			Available: true, OriginalPricing: `{"multiplier":0.75}`, SyncedAt: now, TestedAt: now,
		},
	}).Error)

	require.NoError(t, db.Exec(`CREATE TABLE claude_relay_quota_audit (
		sequence INTEGER PRIMARY KEY AUTOINCREMENT,
		entity TEXT NOT NULL,
		remaining INTEGER NOT NULL,
		used INTEGER NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER claude_relay_user_quota_audit
		AFTER UPDATE OF quota ON users
		BEGIN
			INSERT INTO claude_relay_quota_audit(entity, remaining, used)
			VALUES ('user', NEW.quota, NEW.used_quota);
		END`).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER claude_relay_token_quota_audit
		AFTER UPDATE OF remain_quota ON tokens
		BEGIN
			INSERT INTO claude_relay_quota_audit(entity, remaining, used)
			VALUES ('token', NEW.remain_quota, NEW.used_quota);
		END`).Error)

	gin.SetMode(gin.TestMode)
	if service.GetHttpClient() == nil {
		service.InitHttpClient()
	}
	client := service.GetHttpClient()
	originalTransport := client.Transport
	zenTarget, err := url.Parse(zenServer.URL)
	require.NoError(t, err)
	tabTarget, err := url.Parse(tabServer.URL)
	require.NoError(t, err)
	localTransport := http.DefaultTransport.(*http.Transport).Clone()
	localTransport.Proxy = nil
	client.Transport = &claudeRelayHostRoutingTransport{
		base: localTransport,
		targets: map[string]*url.URL{
			"zenmux.ai":       zenTarget,
			"api2.tabcode.cc": tabTarget,
		},
	}
	t.Cleanup(func() {
		client.Transport = originalTransport
		localTransport.CloseIdleConnections()
	})
	router := gin.New()
	router.Use(middleware.BodyStorageCleanup())
	router.POST(
		"/v1/messages",
		middleware.TokenAuth(),
		middleware.Distribute(),
		func(context *gin.Context) { Relay(context, types.RelayFormatClaude) },
	)
	router.POST(
		"/v1/chat/completions",
		middleware.TokenAuth(),
		middleware.Distribute(),
		func(context *gin.Context) { Relay(context, types.RelayFormatOpenAI) },
	)

	fixture := &claudeRelayIntegrationFixture{
		db: db, router: router, user: user, token: token,
		zenChannel: channels[0], tabChannel: channels[1],
		zenCapture: zenCapture, tabCapture: tabCapture,
	}
	fixture.assertRelayRouteConfiguration(t)
	return fixture
}

func (fixture *claudeRelayIntegrationFixture) assertRelayRouteConfiguration(t *testing.T) {
	t.Helper()

	var channels []model.Channel
	require.NoError(t, fixture.db.Order("priority DESC").Find(&channels).Error)
	require.Len(t, channels, 2)
	require.Equal(t, []int64{600, 550}, []int64{channels[0].GetPriority(), channels[1].GetPriority()})
	for _, channel := range channels {
		require.Equal(t, constant.ChannelTypeAnthropic, channel.Type)
		require.Equal(t, common.ChannelStatusEnabled, channel.Status)
		require.Equal(t, claudeRelayCatalogModel, channel.Models)
		require.Equal(t, "default", channel.Group)
	}
	require.Equal(t, "https://zenmux.ai/api/anthropic", channels[0].GetBaseURL())
	require.Equal(t, "mujian-provider:zenmux:chat", channels[0].GetTag())
	require.Equal(t, "https://api2.tabcode.cc/claude/kiropower", channels[1].GetBaseURL())
	require.Equal(t, "mujian-provider:tabcode:chat", channels[1].GetTag())
	require.Equal(t, `{"claude-sonnet-4-6":"anthropic/claude-sonnet-4.6"}`, channels[0].GetModelMapping())
	require.Equal(t, `{"claude-sonnet-4-6":"claude-sonnet-4-6"}`, channels[1].GetModelMapping())
	require.Equal(t, map[string]interface{}{"anthropic-version": "2023-06-01"}, channels[0].GetHeaderOverride())
	require.Equal(t, map[string]interface{}{
		"Authorization": "Bearer {api_key}", "anthropic-version": "2023-06-01",
	}, channels[1].GetHeaderOverride())

	var abilities []model.Ability
	require.NoError(t, fixture.db.Order("priority DESC").Find(&abilities).Error)
	require.Len(t, abilities, 2)
	require.Equal(t, []int{fixture.zenChannel.Id, fixture.tabChannel.Id}, []int{abilities[0].ChannelId, abilities[1].ChannelId})
	require.Equal(t, []int64{600, 550}, []int64{*abilities[0].Priority, *abilities[1].Priority})
	for _, ability := range abilities {
		require.True(t, ability.Enabled)
		require.Equal(t, "default", ability.Group)
		require.Equal(t, claudeRelayCatalogModel, ability.Model)
	}

	var prices []model.ChannelModelPrice
	require.NoError(t, fixture.db.Order("channel_id ASC").Find(&prices).Error)
	require.Len(t, prices, 2)
	require.Equal(t, fixture.zenChannel.Id, prices[0].ChannelID)
	require.Equal(t, "zenmux", prices[0].Provider)
	require.Equal(t, claudeRelayZenModel, prices[0].UpstreamModelID)
	require.Equal(t, 3.0, prices[0].InputPrice)
	require.Equal(t, 15.0, prices[0].OutputPrice)
	require.Equal(t, fixture.tabChannel.Id, prices[1].ChannelID)
	require.Equal(t, "tabcode", prices[1].Provider)
	require.Equal(t, claudeRelayCatalogModel, prices[1].UpstreamModelID)
	require.Equal(t, 2.25, prices[1].InputPrice)
	require.Equal(t, 11.25, prices[1].OutputPrice)
	require.InDelta(t, 0.225, prices[1].InputPrice*prices[1].CacheRatio, 1e-12)
	require.InDelta(t, 2.8125, prices[1].InputPrice*prices[1].CacheCreationRatio, 1e-12)
	require.InDelta(t, 4.5, prices[1].InputPrice*prices[1].CacheCreationRatio*1.6, 1e-12)
	for _, price := range prices {
		require.Equal(t, claudeRelayCatalogModel, price.CatalogID)
		require.Equal(t, model.ChannelModelBillingToken, price.BillingType)
		require.Equal(t, "USD", price.Currency)
		require.Equal(t, 0.1, price.CacheRatio)
		require.Equal(t, 1.25, price.CacheCreationRatio)
		require.True(t, price.Available)
		require.NotZero(t, price.SyncedAt)
		require.NotZero(t, price.TestedAt)
	}
}

func (fixture *claudeRelayIntegrationFixture) relay(t *testing.T, stream bool) *httptest.ResponseRecorder {
	return fixture.relayContent(t, stream, "hello")
}

func (fixture *claudeRelayIntegrationFixture) relayRaw(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer sk-"+fixture.token.Key)
	request.Header.Set("Content-Type", "application/json")
	if path == "/v1/messages" {
		request.Header.Set("anthropic-version", "2023-06-01")
	}
	response := httptest.NewRecorder()
	fixture.router.ServeHTTP(response, request)
	return response
}

func (fixture *claudeRelayIntegrationFixture) relayContent(t *testing.T, stream bool, content string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(
		`{"model":%q,"max_tokens":100,"stream":%t,"messages":[{"role":"user","content":%q}]}`,
		claudeRelayCatalogModel,
		stream,
		content,
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer sk-"+fixture.token.Key)
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	fixture.router.ServeHTTP(response, request)
	return response
}

func (fixture *claudeRelayIntegrationFixture) relayOpenAIWorkbenchTurn(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	content := strings.Repeat("a", 2048)
	body := fmt.Sprintf(`{
		"model":%q,
		"max_tokens":100,
		"stream":true,
		"stream_options":{"include_usage":true},
		"thinking":{"type":"adaptive","display":"summarized"},
		"output_config":{"effort":"high"},
		"messages":[{"role":"user","content":%q}],
		"tools":[{"type":"function","function":{"name":"lookup","description":"lookup a value","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}}]
	}`, claudeRelayCatalogModel, content)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer sk-"+fixture.token.Key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	response := httptest.NewRecorder()
	fixture.router.ServeHTTP(response, request)
	return response
}

func writeOversizedClaudeCommentEnvelope(writer io.Writer) {
	const lineBytes = 1 << 20
	line := ": " + strings.Repeat("p", lineBytes-3) + "\n"
	for range 17 {
		if _, err := io.WriteString(writer, line); err != nil {
			return
		}
	}
}

func TestManagedClaudeLongPromptUsesSerializedBoundWithTokenCountingOnOrOff(t *testing.T) {
	for _, countToken := range []bool{false, true} {
		t.Run(fmt.Sprintf("CountToken=%t", countToken), func(t *testing.T) {
			fixture := newClaudeRelayIntegrationFixture(
				t,
				func(writer http.ResponseWriter) {
					writer.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(writer, `{"id":"msg_zen_long","type":"message","role":"assistant","model":"anthropic/claude-sonnet-4.6","content":[{"type":"text","text":"bounded"}],"stop_reason":"end_turn","usage":{"input_tokens":1000,"output_tokens":20}}`)
				},
				func(writer http.ResponseWriter) {
					writer.WriteHeader(http.StatusInternalServerError)
				},
			)
			constant.CountToken = countToken

			response := fixture.relayContent(t, false, strings.Repeat("a", 2048))
			require.Equal(t, http.StatusOK, response.Code)
			require.Contains(t, response.Body.String(), "bounded")
			require.Len(t, fixture.zenCapture.snapshot(), 1)
			require.Empty(t, fixture.tabCapture.snapshot(), "a covered long prompt must not trigger a paid fallback")
			fixture.assertFinalAccounting(t, fixture.zenChannel, 2063, 1000, 20, false)

			userAudit := fixture.quotaAudit(t, "user")
			require.Len(t, userAudit, 2)
			reserved := claudeRelayInitialQuota - userAudit[0].Remaining
			require.Greater(t, reserved, claudeRelayPreauthorized)
		})
	}
}

func TestStrictClaudeRejectsRemoteAndOpaqueMediaBeforeDownloadOrUpstream(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		countToken bool
		mediaPath  string
		body       func(string) string
	}{
		{
			name: "native non-stream system PDF with token counting disabled", path: "/v1/messages", mediaPath: "/large.pdf",
			body: func(mediaURL string) string {
				return fmt.Sprintf(`{"model":%q,"max_tokens":100,"stream":false,"system":[{"type":"document","source":{"type":"url","url":%q}}],"messages":[{"role":"user","content":"summarize"}]}`, claudeRelayCatalogModel, mediaURL)
			},
		},
		{
			name: "native stream message image with token counting enabled", path: "/v1/messages", countToken: true, mediaPath: "/large.png",
			body: func(mediaURL string) string {
				return fmt.Sprintf(`{"model":%q,"max_tokens":100,"stream":true,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":%q}}]}]}`, claudeRelayCatalogModel, mediaURL)
			},
		},
		{
			name: "native inline base64 image", path: "/v1/messages", countToken: true,
			body: func(string) string {
				return fmt.Sprintf(`{"model":%q,"max_tokens":100,"stream":false,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`, claudeRelayCatalogModel)
			},
		},
		{
			name: "native inline base64 document", path: "/v1/messages",
			body: func(string) string {
				return fmt.Sprintf(`{"model":%q,"max_tokens":100,"stream":false,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"aGVsbG8="}}]}]}`, claudeRelayCatalogModel)
			},
		},
		{
			name: "native tool result nested document content URL", path: "/v1/messages", mediaPath: "/nested.png",
			body: func(mediaURL string) string {
				return fmt.Sprintf(`{
					"model":%q,
					"max_tokens":100,
					"stream":false,
					"messages":[
						{"role":"user","content":"inspect the tool result"},
						{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"inspect","input":{}}]},
						{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":[{"type":"document","source":{"type":"content","content":[{"type":"image","source":{"type":"url","url":%q}}]}}]}]}
					]
				}`, claudeRelayCatalogModel, mediaURL)
			},
		},
		{
			name: "OpenAI non-stream image URL with token counting enabled", path: "/v1/chat/completions", countToken: true, mediaPath: "/large.png",
			body: func(mediaURL string) string {
				return fmt.Sprintf(`{"model":%q,"max_tokens":100,"stream":false,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`, claudeRelayCatalogModel, mediaURL)
			},
		},
		{
			name: "OpenAI inline data image", path: "/v1/chat/completions", countToken: true,
			body: func(string) string {
				return fmt.Sprintf(`{"model":%q,"max_tokens":100,"stream":false,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`, claudeRelayCatalogModel)
			},
		},
		{
			name: "OpenAI stream file id with token counting disabled", path: "/v1/chat/completions",
			body: func(string) string {
				return fmt.Sprintf(`{"model":%q,"max_tokens":100,"stream":true,"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file_opaque"}}]}]}`, claudeRelayCatalogModel)
			},
		},
		{
			name: "OpenAI remote file data", path: "/v1/chat/completions", mediaPath: "/large.pdf",
			body: func(mediaURL string) string {
				return fmt.Sprintf(`{"model":%q,"max_tokens":100,"stream":false,"messages":[{"role":"user","content":[{"type":"file","file":{"filename":"large.pdf","file_data":%q}}]}]}`, claudeRelayCatalogModel, mediaURL)
			},
		},
		{
			name: "OpenAI remote video URL", path: "/v1/chat/completions", countToken: true, mediaPath: "/large.mp4",
			body: func(mediaURL string) string {
				return fmt.Sprintf(`{"model":%q,"max_tokens":100,"stream":true,"messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":%q}}]}]}`, claudeRelayCatalogModel, mediaURL)
			},
		},
		{
			name: "OpenAI remote input audio data", path: "/v1/chat/completions", mediaPath: "/large.wav",
			body: func(mediaURL string) string {
				return fmt.Sprintf(`{"model":%q,"max_tokens":100,"stream":false,"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":%q,"format":"wav"}}]}]}`, claudeRelayCatalogModel, mediaURL)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var mediaHits atomic.Int32
			mediaServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				mediaHits.Add(1)
				if strings.HasSuffix(request.URL.Path, ".pdf") {
					writer.Header().Set("Content-Type", "application/pdf")
				} else {
					writer.Header().Set("Content-Type", "image/png")
				}
				_, _ = io.WriteString(writer, strings.Repeat("x", 2<<20))
			}))
			t.Cleanup(mediaServer.Close)

			fixture := newClaudeRelayIntegrationFixture(
				t,
				func(writer http.ResponseWriter) { writer.WriteHeader(http.StatusInternalServerError) },
				func(writer http.ResponseWriter) { writer.WriteHeader(http.StatusInternalServerError) },
			)
			constant.CountToken = test.countToken
			mediaURL := ""
			if test.mediaPath != "" {
				mediaURL = mediaServer.URL + test.mediaPath
			}

			response := fixture.relayRaw(t, test.path, test.body(mediaURL))

			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Contains(t, response.Body.String(), "file_id")
			require.Zero(t, mediaHits.Load(), "remote media must be rejected before local token counting downloads it")
			require.Empty(t, fixture.zenCapture.snapshot(), "the primary provider must not be charged")
			require.Empty(t, fixture.tabCapture.snapshot(), "an unbounded request must not be retried on the backup provider")

			var user model.User
			require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
			require.Equal(t, claudeRelayInitialQuota, user.Quota)
			require.Zero(t, user.UsedQuota)
			var token model.Token
			require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
			require.Equal(t, claudeRelayInitialQuota, token.RemainQuota)
			require.Zero(t, token.UsedQuota)
			var consumeLogs int64
			require.NoError(t, fixture.db.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&consumeLogs).Error)
			require.Zero(t, consumeLogs)
		})
	}
}

func TestRemoteMediaOrdinaryPrimaryDoesNotAuthorizeStrictTabCodeRetry(t *testing.T) {
	fixture := newClaudeRelayIntegrationFixture(
		t,
		func(writer http.ResponseWriter) {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(writer, `{"type":"error","error":{"type":"api_error","message":"ordinary primary failed"}}`)
		},
		func(writer http.ResponseWriter) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"id":"msg_tab_forbidden","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"served by strict backup"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":10}}`)
		},
	)
	ordinaryTag := "integration-provider:ordinary-zen:chat"
	require.NoError(t, fixture.db.Model(&model.Channel{}).
		Where("id = ?", fixture.zenChannel.Id).
		Update("tag", ordinaryTag).Error)
	require.NoError(t, fixture.db.Model(&model.Ability{}).
		Where("channel_id = ?", fixture.zenChannel.Id).
		Update("tag", ordinaryTag).Error)
	require.NoError(t, fixture.db.Model(&model.ChannelModelPrice{}).
		Where("channel_id = ? AND catalog_id = ?", fixture.zenChannel.Id, claudeRelayCatalogModel).
		Update("provider", "ordinary").Error)

	mediaURL := "https://media.example/ordinary.pdf"
	body := fmt.Sprintf(`{"model":%q,"max_tokens":100,"stream":false,"system":[{"type":"document","source":{"type":"url","url":%q}}],"messages":[{"role":"user","content":"summarize"}]}`, claudeRelayCatalogModel, mediaURL)
	response := fixture.relayRaw(t, "/v1/messages", body)

	require.GreaterOrEqual(t, response.Code, http.StatusBadRequest)
	require.NotContains(t, response.Body.String(), "served by strict backup")

	zenRequests := fixture.zenCapture.snapshot()
	require.Len(t, zenRequests, 1)
	require.Contains(t, string(zenRequests[0].Body), mediaURL, "the ordinary primary must receive the native remote-media request")
	require.Empty(t, fixture.tabCapture.snapshot(), "the strict TabCode route must not be authorized as a remote-media retry")
}

func assertCapturedClaudeRequest(
	t *testing.T,
	request capturedClaudeUpstreamRequest,
	wantPath string,
	wantModel string,
	wantXAPIKey string,
	wantAuthorization string,
	wantStream bool,
) {
	t.Helper()
	require.Equal(t, http.MethodPost, request.Method)
	require.Equal(t, wantPath, request.Path)
	require.Equal(t, "2023-06-01", request.Header.Get("anthropic-version"))
	require.Equal(t, wantXAPIKey, request.Header.Get("x-api-key"))
	require.Equal(t, wantAuthorization, request.Header.Get("Authorization"))
	var payload struct {
		Model  string `json:"model"`
		Stream *bool  `json:"stream"`
	}
	require.NoError(t, common.Unmarshal(request.Body, &payload))
	require.Equal(t, wantModel, payload.Model)
	require.NotNil(t, payload.Stream)
	require.Equal(t, wantStream, *payload.Stream)
}

func (fixture *claudeRelayIntegrationFixture) quotaAudit(t *testing.T, entity string) []claudeRelayQuotaAudit {
	t.Helper()
	var rows []claudeRelayQuotaAudit
	require.NoError(t, fixture.db.Table("claude_relay_quota_audit").Where("entity = ?", entity).Order("sequence ASC").Find(&rows).Error)
	return rows
}

func (fixture *claudeRelayIntegrationFixture) assertFinalAccounting(
	t *testing.T,
	settledChannel model.Channel,
	actualQuota int,
	promptTokens int,
	completionTokens int,
	stream bool,
) model.Log {
	t.Helper()

	var user model.User
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.Equal(t, claudeRelayInitialQuota-actualQuota, user.Quota)
	require.Equal(t, actualQuota, user.UsedQuota)
	require.Equal(t, 1, user.RequestCount)

	var token model.Token
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	require.Equal(t, claudeRelayInitialQuota-actualQuota, token.RemainQuota)
	require.Equal(t, actualQuota, token.UsedQuota)

	var zenChannel model.Channel
	var tabChannel model.Channel
	require.NoError(t, fixture.db.First(&zenChannel, fixture.zenChannel.Id).Error)
	require.NoError(t, fixture.db.First(&tabChannel, fixture.tabChannel.Id).Error)
	if settledChannel.Id == fixture.zenChannel.Id {
		require.Equal(t, int64(actualQuota), zenChannel.UsedQuota)
		require.Zero(t, tabChannel.UsedQuota)
	} else {
		require.Zero(t, zenChannel.UsedQuota)
		require.Equal(t, int64(actualQuota), tabChannel.UsedQuota)
	}

	var logs []model.Log
	require.NoError(t, fixture.db.Where("type = ?", model.LogTypeConsume).Find(&logs).Error)
	require.Len(t, logs, 1)
	log := logs[0]
	require.Equal(t, settledChannel.Id, log.ChannelId)
	require.Equal(t, fixture.user.Id, log.UserId)
	require.Equal(t, fixture.token.Id, log.TokenId)
	require.Equal(t, claudeRelayCatalogModel, log.ModelName)
	require.Equal(t, actualQuota, log.Quota)
	require.Equal(t, promptTokens, log.PromptTokens)
	require.Equal(t, completionTokens, log.CompletionTokens)
	require.Equal(t, stream, log.IsStream)
	require.Equal(t, "default", log.Group)
	return log
}

func TestClaudeRelayRetriesZenMuxBeforeBytesAndSettlesOnlyTabCode(t *testing.T) {
	fixture := newClaudeRelayIntegrationFixture(
		t,
		func(writer http.ResponseWriter) {
			// A mislabelled error response must not poison the original non-stream
			// request intent used by the JSON retry.
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(writer, `{"type":"error","error":{"type":"overloaded_error","message":"temporary failure"}}`)
		},
		func(writer http.ResponseWriter) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"id":"msg_tab","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"served by tab"}],"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":20}}`)
		},
	)

	response := fixture.relay(t, false)
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "served by tab")
	require.NotContains(t, response.Body.String(), "temporary failure")

	zenRequests := fixture.zenCapture.snapshot()
	tabRequests := fixture.tabCapture.snapshot()
	require.Len(t, zenRequests, 1)
	require.Len(t, tabRequests, 1)
	assertCapturedClaudeRequest(t, zenRequests[0], "/api/anthropic/v1/messages", claudeRelayZenModel, "zen-test-key", "", false)
	assertCapturedClaudeRequest(t, tabRequests[0], "/claude/kiropower/v1/messages", claudeRelayCatalogModel, "", "Bearer tab-test-key", false)

	const actualQuota = 281 // round((100 + 20 * 5) * 1.125 * 1.25)
	log := fixture.assertFinalAccounting(t, fixture.tabChannel, actualQuota, 100, 20, false)
	require.Equal(t, []claudeRelayQuotaAudit{
		{Remaining: claudeRelayInitialQuota - claudeRelayPreauthorized, Used: 0},
		{Remaining: claudeRelayInitialQuota - actualQuota, Used: actualQuota},
	}, fixture.quotaAudit(t, "user"))
	require.Equal(t, []claudeRelayQuotaAudit{
		{Remaining: claudeRelayInitialQuota - claudeRelayPreauthorized, Used: claudeRelayPreauthorized},
		{Remaining: claudeRelayInitialQuota - actualQuota, Used: actualQuota},
	}, fixture.quotaAudit(t, "token"))

	var other map[string]interface{}
	require.NoError(t, common.Unmarshal([]byte(log.Other), &other))
	require.Equal(t, 1.125, other["model_ratio"])
	require.Equal(t, 5.0, other["completion_ratio"])
	require.Equal(t, "anthropic", other["usage_semantic"])
	require.Equal(t, "wallet", other["billing_source"])
	adminInfo, ok := other["admin_info"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t,
		[]interface{}{fmt.Sprint(fixture.zenChannel.Id), fmt.Sprint(fixture.tabChannel.Id)},
		adminInfo["use_channel"],
	)
}

func TestClaudeRelayRetriesEmptyZenMuxStreamBeforeFirstByte(t *testing.T) {
	fixture := newClaudeRelayIntegrationFixture(
		t,
		func(writer http.ResponseWriter) {
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)
		},
		func(writer http.ResponseWriter) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer,
				"event: message_start\n"+
					`data: {"type":"message_start","message":{"id":"msg_tab_stream","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":100,"output_tokens":0}}}`+"\n\n"+
					"event: content_block_delta\n"+
					`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"served by tab stream"}}`+"\n\n"+
					"event: message_delta\n"+
					`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`+"\n\n"+
					"event: message_stop\n"+
					`data: {"type":"message_stop"}`+"\n\n",
			)
		},
	)

	response := fixture.relay(t, true)
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "served by tab stream")
	require.NotContains(t, response.Body.String(), "event: error")

	zenRequests := fixture.zenCapture.snapshot()
	tabRequests := fixture.tabCapture.snapshot()
	require.Len(t, zenRequests, 1)
	require.Len(t, tabRequests, 1)
	assertCapturedClaudeRequest(t, zenRequests[0], "/api/anthropic/v1/messages", claudeRelayZenModel, "zen-test-key", "", true)
	assertCapturedClaudeRequest(t, tabRequests[0], "/claude/kiropower/v1/messages", claudeRelayCatalogModel, "", "Bearer tab-test-key", true)

	const actualQuota = 281
	fixture.assertFinalAccounting(t, fixture.tabChannel, actualQuota, 100, 20, true)
	require.Equal(t, []claudeRelayQuotaAudit{
		{Remaining: claudeRelayInitialQuota - claudeRelayPreauthorized, Used: 0},
		{Remaining: claudeRelayInitialQuota - actualQuota, Used: actualQuota},
	}, fixture.quotaAudit(t, "user"))
	require.Equal(t, []claudeRelayQuotaAudit{
		{Remaining: claudeRelayInitialQuota - claudeRelayPreauthorized, Used: claudeRelayPreauthorized},
		{Remaining: claudeRelayInitialQuota - actualQuota, Used: actualQuota},
	}, fixture.quotaAudit(t, "token"))
}

func TestClaudeRelayRetriesOnceWhenZenMuxCommentEnvelopeExceedsLimitBeforeFirstByte(t *testing.T) {
	fixture := newClaudeRelayIntegrationFixture(
		t,
		func(writer http.ResponseWriter) {
			writer.Header().Set("Content-Type", "text/event-stream")
			writeOversizedClaudeCommentEnvelope(writer)
		},
		func(writer http.ResponseWriter) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer,
				`data: {"type":"message_start","message":{"id":"msg_tab_after_envelope","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":100,"output_tokens":0}}}`+"\n\n"+
					`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"served by tab after envelope rejection"}}`+"\n\n"+
					`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`+"\n\n"+
					`data: {"type":"message_stop"}`+"\n\n",
			)
		},
	)

	response := fixture.relay(t, true)

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "served by tab after envelope rejection")
	require.NotContains(t, response.Body.String(), "event: error")
	require.Len(t, fixture.zenCapture.snapshot(), 1)
	require.Len(t, fixture.tabCapture.snapshot(), 1, "RetryTimes=1 must permit exactly one backup attempt")
	fixture.assertFinalAccounting(t, fixture.tabChannel, 281, 100, 20, true)
}

func TestClaudeRelayClearsEmptySSEHeadersBeforeNonStreamFallback(t *testing.T) {
	fixture := newClaudeRelayIntegrationFixture(
		t,
		func(writer http.ResponseWriter) {
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)
		},
		func(writer http.ResponseWriter) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"id":"msg_tab_json","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"served by tab json"}],"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":20}}`)
		},
	)

	response := fixture.relay(t, false)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "application/json", response.Header().Get("Content-Type"))
	require.Empty(t, response.Header().Get("Transfer-Encoding"))
	require.Contains(t, response.Body.String(), "served by tab json")
	require.Len(t, fixture.zenCapture.snapshot(), 1)
	require.Len(t, fixture.tabCapture.snapshot(), 1)
	fixture.assertFinalAccounting(t, fixture.tabChannel, 281, 100, 20, false)
}

func TestWorkbenchOpenAIStreamConvertsAdaptiveThinkingAndToolUseThroughTabCode(t *testing.T) {
	fixture := newClaudeRelayIntegrationFixture(
		t,
		func(writer http.ResponseWriter) {
			writer.WriteHeader(http.StatusBadGateway)
		},
		func(writer http.ResponseWriter) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer,
				`data: {"type":"message_start","message":{"id":"msg_workbench","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":100,"output_tokens":0}}}`+"\n\n"+
					`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`+"\n\n"+
					`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"I should look it up."}}`+"\n\n"+
					`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tool_1","name":"lookup","input":{}}}`+"\n\n"+
					`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"answer\"}"}}`+"\n\n"+
					`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`+"\n\n"+
					`data: {"type":"message_stop"}`+"\n\n",
			)
		},
	)

	response := fixture.relayOpenAIWorkbenchTurn(t)
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"reasoning_content":"I should look it up."`)
	require.Contains(t, response.Body.String(), `"name":"lookup"`)
	require.Contains(t, response.Body.String(), `"arguments":"{\"q\":\"answer\"}"`)
	require.Contains(t, response.Body.String(), `"finish_reason":"tool_calls"`)

	zenRequests := fixture.zenCapture.snapshot()
	tabRequests := fixture.tabCapture.snapshot()
	require.Len(t, zenRequests, 1)
	require.Len(t, tabRequests, 1)
	assertCapturedClaudeRequest(t, zenRequests[0], "/api/anthropic/v1/messages", claudeRelayZenModel, "zen-test-key", "", true)
	assertCapturedClaudeRequest(t, tabRequests[0], "/claude/kiropower/v1/messages", claudeRelayCatalogModel, "", "Bearer tab-test-key", true)
	var upstreamPayload struct {
		Thinking struct {
			Type    string `json:"type"`
			Display string `json:"display"`
		} `json:"thinking"`
		OutputConfig struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	require.NoError(t, common.Unmarshal(tabRequests[0].Body, &upstreamPayload))
	require.Equal(t, "adaptive", upstreamPayload.Thinking.Type)
	require.Equal(t, "summarized", upstreamPayload.Thinking.Display)
	require.Equal(t, "high", upstreamPayload.OutputConfig.Effort)
	require.Len(t, upstreamPayload.Tools, 1)
	require.Equal(t, "lookup", upstreamPayload.Tools[0].Name)

	fixture.assertFinalAccounting(t, fixture.tabChannel, 281, 100, 20, true)
}

func TestClaudeRelayDoesNotReplayAfterZenMuxStreamBytes(t *testing.T) {
	fixture := newClaudeRelayIntegrationFixture(
		t,
		func(writer http.ResponseWriter) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "event: message_start\n")
			_, _ = io.WriteString(writer, `data: {"type":"message_start","message":{"id":"msg_zen_partial","type":"message","role":"assistant","model":"anthropic/claude-sonnet-4.6","content":[],"usage":{"input_tokens":40,"output_tokens":0}}}`+"\n\n")
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
			// Returning without message_stop simulates an upstream interruption after
			// the first valid event has already been committed downstream.
		},
		func(writer http.ResponseWriter) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "event: message_stop\n")
			_, _ = io.WriteString(writer, `data: {"type":"message_stop"}`+"\n\n")
		},
	)

	response := fixture.relay(t, true)
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "event: message_start")
	require.Contains(t, response.Body.String(), "msg_zen_partial")
	require.Contains(t, response.Body.String(), "event: error")

	zenRequests := fixture.zenCapture.snapshot()
	tabRequests := fixture.tabCapture.snapshot()
	require.Len(t, zenRequests, 1)
	require.Empty(t, tabRequests, "a stream that already emitted bytes must never be replayed")
	assertCapturedClaudeRequest(t, zenRequests[0], "/api/anthropic/v1/messages", claudeRelayZenModel, "zen-test-key", "", true)

	const partialQuota = 75 // 40 input tokens * 1.5 * 1.25
	log := fixture.assertFinalAccounting(t, fixture.zenChannel, partialQuota, 40, 0, true)
	require.Equal(t, []claudeRelayQuotaAudit{
		{Remaining: claudeRelayInitialQuota - claudeRelayPreauthorized, Used: 0},
		{Remaining: claudeRelayInitialQuota - partialQuota, Used: partialQuota},
	}, fixture.quotaAudit(t, "user"))
	require.Equal(t, []claudeRelayQuotaAudit{
		{Remaining: claudeRelayInitialQuota - claudeRelayPreauthorized, Used: claudeRelayPreauthorized},
		{Remaining: claudeRelayInitialQuota - partialQuota, Used: partialQuota},
	}, fixture.quotaAudit(t, "token"))

	var other map[string]interface{}
	require.NoError(t, common.Unmarshal([]byte(log.Other), &other))
	require.Equal(t, 1.5, other["model_ratio"])
	require.Equal(t, "anthropic", other["usage_semantic"])
	adminInfo, ok := other["admin_info"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, []interface{}{fmt.Sprint(fixture.zenChannel.Id)}, adminInfo["use_channel"])
}

func TestClaudeRelayDoesNotReplayWhenCommentsExceedLimitAfterTextAndSettlesPartialUsage(t *testing.T) {
	const partialText = "partial before envelope overflow"
	fixture := newClaudeRelayIntegrationFixture(
		t,
		func(writer http.ResponseWriter) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer,
				`data: {"type":"message_start","message":{"id":"msg_zen_envelope_partial","type":"message","role":"assistant","model":"anthropic/claude-sonnet-4.6","content":[],"usage":{"input_tokens":40,"output_tokens":0}}}`+"\n\n"+
					`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+partialText+`"}}`+"\n\n",
			)
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
			writeOversizedClaudeCommentEnvelope(writer)
		},
		func(writer http.ResponseWriter) {
			writer.WriteHeader(http.StatusInternalServerError)
		},
	)

	response := fixture.relay(t, true)

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), partialText)
	require.Contains(t, response.Body.String(), "event: error")
	require.Len(t, fixture.zenCapture.snapshot(), 1)
	require.Empty(t, fixture.tabCapture.snapshot(), "a stream with committed text must not be replayed")

	var logs []model.Log
	require.NoError(t, fixture.db.Where("type = ?", model.LogTypeConsume).Find(&logs).Error)
	require.Len(t, logs, 1)
	log := logs[0]
	require.Equal(t, fixture.zenChannel.Id, log.ChannelId)
	require.Equal(t, 40, log.PromptTokens)
	require.GreaterOrEqual(t, log.CompletionTokens, (len(partialText)+127)/128)
	require.Positive(t, log.Quota)
	require.Less(t, log.Quota, claudeRelayPreauthorized)

	var user model.User
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.Equal(t, claudeRelayInitialQuota-log.Quota, user.Quota)
	require.Equal(t, log.Quota, user.UsedQuota)
	var token model.Token
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	require.Equal(t, claudeRelayInitialQuota-log.Quota, token.RemainQuota)
	require.Equal(t, log.Quota, token.UsedQuota)
}

func TestWorkbenchClaudePartialStreamSettlesInsteadOfRefunding(t *testing.T) {
	fixture := newClaudeRelayIntegrationFixture(
		t,
		func(writer http.ResponseWriter) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer,
				`data: {"type":"message_start","message":{"id":"msg_workbench_partial","type":"message","role":"assistant","model":"anthropic/claude-sonnet-4.6","content":[],"usage":{"input_tokens":40,"output_tokens":0}}}`+"\n\n"+
					`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial workbench output"}}`+"\n\n",
			)
		},
		func(writer http.ResponseWriter) {
			writer.WriteHeader(http.StatusInternalServerError)
		},
	)

	response := fixture.relayOpenAIWorkbenchTurn(t)

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "partial workbench output")
	require.Contains(t, response.Body.String(), `"error"`)
	require.NotContains(t, response.Body.String(), "[DONE]")
	require.Len(t, fixture.zenCapture.snapshot(), 1)
	require.Empty(t, fixture.tabCapture.snapshot(), "a committed OpenAI stream must not be replayed")

	var logs []model.Log
	require.NoError(t, fixture.db.Where("type = ?", model.LogTypeConsume).Find(&logs).Error)
	require.Len(t, logs, 1)
	require.Equal(t, fixture.zenChannel.Id, logs[0].ChannelId)
	require.Equal(t, 40, logs[0].PromptTokens)
	require.Positive(t, logs[0].CompletionTokens)
	require.Positive(t, logs[0].Quota)
	require.Less(t, logs[0].Quota, claudeRelayPreauthorized)

	var user model.User
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.Equal(t, claudeRelayInitialQuota-logs[0].Quota, user.Quota)
	require.Equal(t, logs[0].Quota, user.UsedQuota)
	var token model.Token
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	require.Equal(t, claudeRelayInitialQuota-logs[0].Quota, token.RemainQuota)
	require.Equal(t, logs[0].Quota, token.UsedQuota)
}
