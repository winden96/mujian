package mujianprovider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupProviderDB(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.ChannelModelPrice{}, &model.Ability{}, &model.Option{}))
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
}

func seedRoutableZenMuxSonnet46(t *testing.T) model.Channel {
	t.Helper()
	require.NoError(t, Configure("zenmux", "managed-provider-key"))
	channels, err := providerChannels("zenmux", true)
	require.NoError(t, err)
	require.Len(t, channels, 1)
	channel := channels[0]
	const syncedAt = int64(100)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
		"models":        "claude-sonnet-4-6",
		"model_mapping": `{"claude-sonnet-4-6":"anthropic/claude-sonnet-4.6"}`,
		"status":        common.ChannelStatusEnabled,
		"test_time":     syncedAt,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "claude-sonnet-4-6", ChannelId: channel.Id,
		Enabled: true, Priority: channel.Priority,
	}).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: "claude-sonnet-4-6",
		UpstreamModelID: "anthropic/claude-sonnet-4.6", Provider: "zenmux",
		BillingType: model.ChannelModelBillingToken, Currency: "USD",
		InputPrice: 3, OutputPrice: 15, CacheRatio: 0.1, CacheCreationRatio: 1.25,
		Available: true, SyncedAt: syncedAt, TestedAt: syncedAt,
	}).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	return channel
}

func TestCatalogReasoningProtocols(t *testing.T) {
	tests := []struct {
		modelID  string
		protocol string
	}{
		{modelID: "deepseek-v4-flash", protocol: ReasoningProtocolOpenAIEffort},
		{modelID: "gpt-5.6-sol", protocol: ReasoningProtocolOpenAIEffort},
		{modelID: "claude-opus-5", protocol: ReasoningProtocolClaudeAdaptive},
		{modelID: "claude-sonnet-4-6", protocol: ReasoningProtocolClaudeAdaptive},
		{modelID: "grok-4.5", protocol: ReasoningProtocolOpenAIEffort},
		{modelID: "gpt-5.6-terra"},
		{modelID: "missing-model"},
	}
	for _, test := range tests {
		t.Run(test.modelID, func(t *testing.T) {
			require.Equal(t, test.protocol, ModelReasoningProtocol(test.modelID))
			require.Equal(t, test.protocol != "", SupportsReasoning(test.modelID))
		})
	}

	for _, entry := range Catalog() {
		require.Equal(t, entry.ReasoningProtocol != "", entry.SupportsReasoning, entry.ID)
	}
}

func TestManagedRelaySnapshotBindsCurrentGroupAndPrice(t *testing.T) {
	setupProviderDB(t)
	channel := seedRoutableZenMuxSonnet46(t)
	cached := channel

	current, price, managed, err := LoadManagedRelayChannelForRequest(cached, "claude-sonnet-4-6", "default")
	require.NoError(t, err)
	require.True(t, managed)
	require.Equal(t, "default", current.Group)
	require.Equal(t, 3.0, price.InputPrice)
	require.Equal(t, "default", price.RoutingGroup)
	routable, err := CatalogModelRoutableForGroupTx(model.DB, "claude-sonnet-4-6", "default")
	require.NoError(t, err)
	require.True(t, routable)

	require.NoError(t, model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("group", "mujian-canary").Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", channel.Id).Delete(&model.Ability{}).Error; err != nil {
			return err
		}
		return tx.Create(&model.Ability{
			Group: "mujian-canary", Model: "claude-sonnet-4-6", ChannelId: channel.Id,
			Enabled: true, Priority: channel.Priority,
		}).Error
	}))

	_, _, managed, err = LoadManagedRelayChannelForRequest(cached, "claude-sonnet-4-6", "default")
	require.True(t, managed)
	require.Error(t, err)
	routable, err = CatalogModelRoutableForGroupTx(model.DB, "claude-sonnet-4-6", "default")
	require.NoError(t, err)
	require.False(t, routable)
	current, price, managed, err = LoadManagedRelayChannelForRequest(cached, "claude-sonnet-4-6", "mujian-canary")
	require.NoError(t, err)
	require.True(t, managed)
	require.Equal(t, "mujian-canary", current.Group)
	require.Equal(t, "mujian-canary", price.RoutingGroup)
	routable, err = CatalogModelRoutableForGroupTx(model.DB, "claude-sonnet-4-6", "mujian-canary")
	require.NoError(t, err)
	require.True(t, routable)
}

func TestCatalogModelRoutableForGroupRejectsUnsafeOrdinaryPrice(t *testing.T) {
	setupProviderDB(t)
	priority := int64(500)
	channel := model.Channel{
		Name: "ordinary", Key: "ordinary-key", Group: "default", Models: "claude-sonnet-4-6",
		Status: common.ChannelStatusEnabled, Priority: &priority,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "claude-sonnet-4-6", ChannelId: channel.Id,
		Enabled: true, Priority: &priority,
	}).Error)
	price := model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: "claude-sonnet-4-6", UpstreamModelID: "claude-sonnet-4-6",
		Provider: "ordinary", BillingType: model.ChannelModelBillingToken, Currency: "USD",
		InputPrice: 0, OutputPrice: 15, CacheRatio: 0.1, CacheCreationRatio: 1.25, Available: true,
	}
	require.NoError(t, model.DB.Create(&price).Error)

	routable, err := CatalogModelRoutableForGroupTx(model.DB, "claude-sonnet-4-6", "default")
	require.NoError(t, err)
	require.False(t, routable)

	require.NoError(t, model.DB.Model(&price).Updates(map[string]any{
		"input_price": 3.0, "output_price": 15.0, "cache_ratio": -1.0,
	}).Error)
	routable, err = CatalogModelRoutableForGroupTx(model.DB, "claude-sonnet-4-6", "default")
	require.NoError(t, err)
	require.False(t, routable)

	require.NoError(t, model.DB.Model(&price).Updates(map[string]any{
		"cache_ratio": 0.1, "cache_creation_ratio": 1.25,
	}).Error)
	routable, err = CatalogModelRoutableForGroupTx(model.DB, "claude-sonnet-4-6", "default")
	require.NoError(t, err)
	require.True(t, routable)
}

func TestCatalogAvailabilityFiltersTamperedManagedRoutes(t *testing.T) {
	tests := []struct {
		name   string
		column string
		value  any
	}{
		{name: "model mapping", column: "model_mapping", value: `{"claude-sonnet-4-6":"anthropic/claude-opus-5"}`},
		{name: "base URL", column: "base_url", value: "https://example.invalid"},
		{name: "authorization headers", column: "header_override", value: `{"anthropic-version":"2024-01-01"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setupProviderDB(t)
			channel := seedRoutableZenMuxSonnet46(t)
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Update(test.column, test.value).Error)

			items, err := CatalogAvailabilityListForGroup("default")
			require.NoError(t, err)
			for _, item := range items {
				if item.ID == "claude-sonnet-4-6" {
					require.False(t, item.Available)
					require.Zero(t, item.ChannelCount)
				}
			}
			ids, err := AvailableCatalogIDsForGroup("default")
			require.NoError(t, err)
			require.NotContains(t, ids, "claude-sonnet-4-6")
		})
	}
}

func TestResolveProviderUsesStatusServerAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"server_address":"https://api.example.com/"}}`))
	}))
	t.Cleanup(server.Close)

	provider, err := resolveProvider(t.Context(), Definition{Name: "GeekNow", StatusURL: server.URL})

	require.NoError(t, err)
	require.Equal(t, "https://api.example.com", provider.BaseURL)
	require.Equal(t, "https://api.example.com/api/pricing", provider.PricingURL)
}

func TestConfigureManagedProviderIsConcurrentAndIdempotent(t *testing.T) {
	setupProviderDB(t)
	const workers = 12
	errorsByWorker := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			errorsByWorker <- Configure("zenmux", fmt.Sprintf("concurrent-key-%02d", index))
		}(index)
	}
	group.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		require.NoError(t, err)
	}

	channels, err := providerChannels("zenmux", true)
	require.NoError(t, err)
	require.Len(t, channels, 1)
	require.Equal(t, providerTag("zenmux", "chat"), channels[0].GetTag())
}

func TestConfigureLegacyProviderPreservesExistingChannelCustomizations(t *testing.T) {
	setupProviderDB(t)
	require.NoError(t, Configure("zex", "legacy-provider-key"))
	channels, err := providerChannels("zex", true)
	require.NoError(t, err)
	require.NotEmpty(t, channels)
	channel := channels[0]
	organization := "legacy-org"
	statusMapping := `{"429":503}`
	setting := `{"force_format":true}`
	paramOverride := `{"temperature":0.2}`
	headerOverride := `{"x-legacy":"preserve"}`
	channelInfo := model.ChannelInfo{IsMultiKey: true, MultiKeySize: 1}
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
		"open_ai_organization": organization,
		"other":               "legacy-region",
		"status_code_mapping": statusMapping,
		"auto_ban":            0,
		"setting":             setting,
		"param_override":      paramOverride,
		"header_override":     headerOverride,
		"settings":            `{"legacy":true}`,
		"channel_info":        channelInfo,
	}).Error)

	require.NoError(t, Configure("zex", "rotated-legacy-provider-key"))
	updated, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	require.Equal(t, organization, pointerValue(updated.OpenAIOrganization))
	require.Equal(t, "legacy-region", updated.Other)
	require.Equal(t, statusMapping, pointerValue(updated.StatusCodeMapping))
	require.False(t, updated.GetAutoBan())
	require.Equal(t, setting, pointerValue(updated.Setting))
	require.Equal(t, paramOverride, pointerValue(updated.ParamOverride))
	require.Equal(t, headerOverride, pointerValue(updated.HeaderOverride))
	require.Equal(t, `{"legacy":true}`, updated.OtherSettings)
	require.True(t, updated.ChannelInfo.IsMultiKey)
	require.Equal(t, 1, updated.ChannelInfo.MultiKeySize)
}

func TestConfigureLegacyProviderReusesExistingGroupWhenRepairingMissingRoute(t *testing.T) {
	setupProviderDB(t)
	require.NoError(t, Configure("zex", "legacy-provider-key"))
	channels, err := providerChannels("zex", true)
	require.NoError(t, err)
	require.Len(t, channels, 3)
	require.NoError(t, model.DB.Model(&model.Channel{}).
		Where("tag LIKE ?", "mujian-provider:zex:%").Update("group", "legacy-group").Error)
	require.NoError(t, model.DB.Where("id = ?", channels[0].Id).Delete(&model.Channel{}).Error)

	require.NoError(t, ConfigureFromAPI("zex", "rotated-legacy-key", nil))
	repaired, err := providerChannels("zex", true)
	require.NoError(t, err)
	require.Len(t, repaired, 3)
	for _, channel := range repaired {
		require.Equal(t, "legacy-group", channel.Group)
	}
}

func TestConfigureLegacyProviderRejectsAmbiguousExistingGroupsDuringRepair(t *testing.T) {
	setupProviderDB(t)
	require.NoError(t, Configure("zex", "legacy-provider-key"))
	channels, err := providerChannels("zex", true)
	require.NoError(t, err)
	require.Len(t, channels, 3)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channels[0].Id).Update("group", "group-a").Error)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channels[1].Id).Update("group", "group-b").Error)
	require.NoError(t, model.DB.Where("id = ?", channels[2].Id).Delete(&model.Channel{}).Error)

	err = ConfigureFromAPI("zex", "rotated-legacy-key", nil)
	require.ErrorContains(t, err, "路由分组不一致")
}

func TestConfigureAndSetProviderGroupUpdateChannelAndAbilitiesAtomically(t *testing.T) {
	setupProviderDB(t)
	require.NoError(t, ConfigureInGroup("tabcode", "managed-provider-key", "vip"))
	channel := loadOnlyProviderChannel(t, "tabcode")
	require.Equal(t, "vip", channel.Group)
	require.Equal(t, common.ChannelStatusManuallyDisabled, channel.Status)

	// Key rotation is group-neutral; moving routes is an explicit SetGroup action.
	require.NoError(t, ConfigureInGroup("tabcode", "rotated-managed-provider-key", "default"))
	channel = loadOnlyProviderChannel(t, "tabcode")
	require.Equal(t, "vip", channel.Group)

	channel.Models = "claude-sonnet-4-6"
	channel.Status = common.ChannelStatusEnabled
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
		"models": channel.Models, "status": channel.Status,
	}).Error)
	require.NoError(t, channel.UpdateAbilities(nil))

	require.NoError(t, SetGroup("tabcode", "default"))
	channel = loadOnlyProviderChannel(t, "tabcode")
	require.Equal(t, "default", channel.Group)
	require.Equal(t, common.ChannelStatusEnabled, channel.Status)
	var abilities []model.Ability
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
	require.Len(t, abilities, 1)
	require.Equal(t, "default", abilities[0].Group)
	require.True(t, abilities[0].Enabled)

	require.ErrorContains(t, SetGroup("tabcode", "missing-canary"), "尚未创建")
	channel = loadOnlyProviderChannel(t, "tabcode")
	require.Equal(t, "default", channel.Group)
}

func TestStrictProviderWritesRejectStaleKeySnapshots(t *testing.T) {
	setupProviderDB(t)
	require.NoError(t, Configure("tabcode", "old-provider-key"))
	require.NoError(t, Configure("tabcode", "new-provider-key"))
	provider, ok := definition("tabcode")
	require.True(t, ok)
	generation, err := providerLifecycleGeneration(model.DB, provider.ID)
	require.NoError(t, err)

	_, err = persistStrictProviderSync(
		provider,
		"old-provider-key",
		generation,
		pricingSnapshot{},
		map[string][]model.ChannelModelPrice{"chat": nil},
		map[string]map[string]string{"chat": {}},
		map[string]bool{"chat": true},
	)
	require.ErrorIs(t, err, errProviderConfigurationChanged)
	require.ErrorIs(t, recordProviderTestSuccess(provider, "old-provider-key", generation, []string{"claude-sonnet-4-6"}, 1), errProviderConfigurationChanged)

	channel := loadOnlyProviderChannel(t, "tabcode")
	require.Equal(t, "new-provider-key", channel.Key)
	require.Empty(t, channel.Models)
	require.Zero(t, channel.TestTime)
	var prices []model.ChannelModelPrice
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Find(&prices).Error)
	for _, price := range prices {
		require.False(t, price.Available)
		require.Zero(t, price.TestedAt)
	}
}

func TestStrictProviderLifecycleGenerationRejectsStaleRemoteResults(t *testing.T) {
	setupProviderDB(t)
	require.NoError(t, Configure("tabcode", "managed-provider-key"))
	provider, ok := definition("tabcode")
	require.True(t, ok)
	initialGeneration, err := providerLifecycleGeneration(model.DB, provider.ID)
	require.NoError(t, err)
	price := model.ChannelModelPrice{
		CatalogID: "claude-sonnet-4-6", UpstreamModelID: "claude-sonnet-4-6",
		Provider: "tabcode", BillingType: model.ChannelModelBillingToken,
		InputPrice: 2.25, OutputPrice: 11.25, Currency: "USD",
	}
	prices := map[string][]model.ChannelModelPrice{"chat": {price}}
	mappings := map[string]map[string]string{"chat": {"claude-sonnet-4-6": "claude-sonnet-4-6"}}
	touched := map[string]bool{"chat": true}

	_, err = persistStrictProviderSync(
		provider, "managed-provider-key", initialGeneration, pricingSnapshot{}, prices, mappings, touched,
	)
	require.NoError(t, err)
	currentGeneration, err := providerLifecycleGeneration(model.DB, provider.ID)
	require.NoError(t, err)
	require.NotEqual(t, initialGeneration, currentGeneration)

	stalePrice := price
	stalePrice.InputPrice = 999
	_, err = persistStrictProviderSync(
		provider, "managed-provider-key", initialGeneration, pricingSnapshot{},
		map[string][]model.ChannelModelPrice{"chat": {stalePrice}}, mappings, touched,
	)
	require.ErrorIs(t, err, errProviderConfigurationChanged)
	channel := loadOnlyProviderChannel(t, "tabcode")
	stored := loadProviderPrice(t, channel.Id, "claude-sonnet-4-6")
	require.Equal(t, 2.25, stored.InputPrice)

	probeGeneration, err := prepareProviderTest(provider, "managed-provider-key")
	require.NoError(t, err)
	require.NoError(t, model.DB.Transaction(func(tx *gorm.DB) error {
		return advanceProviderLifecycleGeneration(tx, provider.ID)
	}))
	err = recordProviderTestSuccess(
		provider, "managed-provider-key", probeGeneration, []string{"claude-sonnet-4-6"}, 1,
	)
	require.ErrorIs(t, err, errProviderConfigurationChanged)
	stored = loadProviderPrice(t, channel.Id, "claude-sonnet-4-6")
	require.False(t, stored.Available)
	require.Zero(t, stored.TestedAt)
}

func TestPrepareProviderSyncImmediatelyFailsClosed(t *testing.T) {
	setupProviderDB(t)
	require.NoError(t, Configure("tabcode", "managed-provider-key"))
	provider, ok := definition("tabcode")
	require.True(t, ok)
	channel := loadOnlyProviderChannel(t, "tabcode")
	mapping := `{"claude-sonnet-4-6":"claude-sonnet-4-6"}`
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
		"models":        "claude-sonnet-4-6",
		"model_mapping": mapping,
		"status":        common.ChannelStatusEnabled,
		"test_time":     int64(123),
	}).Error)
	priority := channel.GetPriority()
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: channel.Group, Model: "claude-sonnet-4-6", ChannelId: channel.Id,
		Enabled: true, Priority: &priority,
	}).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: "claude-sonnet-4-6", UpstreamModelID: "claude-sonnet-4-6",
		Provider: "tabcode", BillingType: model.ChannelModelBillingToken, Currency: "USD",
		InputPrice: 2.25, OutputPrice: 11.25, Available: true, TestedAt: 123, SyncedAt: 100,
	}).Error)
	before, err := providerLifecycleGeneration(model.DB, provider.ID)
	require.NoError(t, err)

	generation, err := prepareProviderSync(provider, "managed-provider-key")

	require.NoError(t, err)
	require.NotEqual(t, before, generation)
	channel = loadOnlyProviderChannel(t, "tabcode")
	require.Equal(t, common.ChannelStatusManuallyDisabled, channel.Status)
	require.Zero(t, channel.TestTime)
	requireProviderAbilitiesEmpty(t, channel.Id)
	price := loadProviderPrice(t, channel.Id, "claude-sonnet-4-6")
	require.Equal(t, "unavailable", price.BillingType)
	require.False(t, price.Available)
	require.Zero(t, price.TestedAt)
	require.Equal(t, "价格同步中，请稍候", price.LastError)
}

func TestSetEnabledRejectsUnknownOrTamperedManagedProvider(t *testing.T) {
	require.ErrorContains(t, SetEnabled("%", true), "未知供应商")

	tests := []struct {
		name    string
		updates map[string]any
	}{
		{name: "base URL", updates: map[string]any{"base_url": "https://evil.example"}},
		{name: "protocol", updates: map[string]any{"type": 1}},
		{name: "auth headers", updates: map[string]any{"header_override": `{"Authorization":"Bearer stolen"}`}},
		{name: "priority", updates: map[string]any{"priority": int64(1)}},
		{name: "test model", updates: map[string]any{"test_model": "claude-opus-5"}},
		{name: "model mapping", updates: map[string]any{"model_mapping": `{"claude-sonnet-4-6":"anthropic/claude-sonnet-5"}`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setupProviderDB(t)
			require.NoError(t, Configure("zenmux", "managed-provider-key"))
			channel := loadOnlyProviderChannel(t, "zenmux")
			now := common.GetTimestamp()
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
				"models": "claude-sonnet-4-6", "model_mapping": `{"claude-sonnet-4-6":"anthropic/claude-sonnet-4.6"}`,
				"test_time": now,
			}).Error)
			require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
				ChannelID: channel.Id, CatalogID: "claude-sonnet-4-6", UpstreamModelID: "anthropic/claude-sonnet-4.6",
				Provider: "zenmux", BillingType: model.ChannelModelBillingToken, Currency: "USD",
				InputPrice: 3, OutputPrice: 15, Available: true, SyncedAt: now, TestedAt: now,
			}).Error)
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(test.updates).Error)

			require.Error(t, SetEnabled("zenmux", true))
		})
	}
}

func TestManagedProviderGenericMutationAndOutboundGuards(t *testing.T) {
	setupProviderDB(t)
	require.NoError(t, Configure("tabcode", "managed-provider-key"))
	channel := loadOnlyProviderChannel(t, "tabcode")

	providerID, managed := StrictProviderIDForTag(channel.GetTag())
	require.True(t, managed)
	require.Equal(t, "tabcode", providerID)
	require.NoError(t, ValidateManagedRelayChannel(channel))
	require.ErrorContains(t, ValidateManagedChannelUpdate(channel, model.Channel{Group: "vip"}), "供应商面板")

	maliciousURL := "https://evil.example/claude/kiropower"
	require.ErrorContains(t, ValidateManagedChannelUpdate(channel, model.Channel{BaseURL: &maliciousURL}), "供应商面板")
	require.ErrorContains(t, ValidateManagedChannelUpdate(channel, model.Channel{Key: "rotated-provider-key"}), "供应商面板")
	require.ErrorContains(t, ValidateManagedChannelUpdate(channel, model.Channel{Status: common.ChannelStatusEnabled}), "供应商面板")
	paramOverride := `{"model":"claude-sonnet-5"}`
	require.ErrorContains(t, ValidateManagedChannelUpdate(channel, model.Channel{ParamOverride: &paramOverride}), "供应商面板")
	otherSettings := `{"upstream_model_update_check_enabled":true}`
	require.ErrorContains(t, ValidateManagedChannelUpdate(channel, model.Channel{OtherSettings: otherSettings}), "供应商面板")
	caseCollidingHeaders := `{"Authorization":"Bearer {api_key}","authorization":"Bearer attacker","anthropic-version":"2023-06-01"}`
	require.ErrorContains(t, ValidateManagedChannelUpdate(channel, model.Channel{HeaderOverride: &caseCollidingHeaders}), "供应商面板")

	channel.BaseURL = &maliciousURL
	require.Error(t, ValidateManagedRelayChannel(channel))

	reservedTag := providerTag("zenmux", "chat")
	require.ErrorContains(t, ValidateManagedChannelUpdate(model.Channel{}, model.Channel{Tag: &reservedTag}), "托管标签")
	spacedReservedTag := "  " + reservedTag + "  "
	spacedProviderID, spacedManaged := StrictProviderIDForTag(spacedReservedTag)
	require.True(t, spacedManaged)
	require.Equal(t, "zenmux", spacedProviderID)
	require.ErrorContains(t, ValidateManagedChannelUpdate(model.Channel{}, model.Channel{Tag: &spacedReservedTag}), "托管标签")
	spacedOwnTag := "  " + channel.GetTag() + "  "
	spacedChannel := channel
	spacedChannel.Tag = &spacedOwnTag
	validTabBaseURL := tabCodeKiroBaseURL
	spacedChannel.BaseURL = &validTabBaseURL
	require.ErrorContains(t, ValidateManagedRelayChannel(spacedChannel), "供应商标签无效")
	_, wildcardManaged := StrictProviderIDForTag("mujian-provider:%:chat")
	require.False(t, wildcardManaged)
	require.Equal(t, []string{"mujian-provider:tabcode:chat", "mujian-provider:yuyu:chat", "mujian-provider:yuyu:gpt-image", "mujian-provider:yuyu:nano", "mujian-provider:zenmux:chat"}, StrictProviderTags())
}

func TestProviderStatusesExposeSafeMappingAndPriceDetails(t *testing.T) {
	setupProviderDB(t)
	require.NoError(t, Configure("tabcode", "never-expose-this-key"))
	channel := loadOnlyProviderChannel(t, "tabcode")
	now := common.GetTimestamp()
	mapping := `{"claude-sonnet-4-6":"claude-sonnet-4-6"}`
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
		"models": "claude-sonnet-4-6", "model_mapping": mapping,
	}).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: "claude-sonnet-4-6", UpstreamModelID: "claude-sonnet-4-6",
		Provider: "tabcode", BillingType: model.ChannelModelBillingToken, Currency: "USD",
		InputPrice: 2.25, OutputPrice: 11.25, CacheRatio: 0.1, CacheCreationRatio: 1.25,
		Available: true, SourceURL: tabCodeChannelsURL, SourceVersion: "sha256:test", SyncedAt: now, TestedAt: now,
		OriginalPricing: `{"secret":"hidden-evidence"}`,
	}).Error)

	statuses, err := Statuses()
	require.NoError(t, err)
	var tabcode ProviderStatus
	for _, status := range statuses {
		if status.ID == "tabcode" {
			tabcode = status
			break
		}
	}
	require.Len(t, tabcode.Routes, 1)
	require.Len(t, tabcode.Routes[0].Prices, 1)
	require.True(t, tabcode.TestReady)
	price := tabcode.Routes[0].Prices[0]
	require.Equal(t, "claude-sonnet-4-6", price.CatalogID)
	require.Equal(t, "claude-sonnet-4-6", price.UpstreamModelID)
	require.NotNil(t, price.CacheReadPrice)
	require.NotNil(t, price.CacheWrite5mPrice)
	require.NotNil(t, price.CacheWrite1hPrice)
	require.InDelta(t, 0.225, *price.CacheReadPrice, 1e-9)
	require.InDelta(t, 2.8125, *price.CacheWrite5mPrice, 1e-9)
	require.InDelta(t, 4.5, *price.CacheWrite1hPrice, 1e-9)

	encoded, err := common.Marshal(tabcode)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "never-expose-this-key")
	require.NotContains(t, string(encoded), "hidden-evidence")
	require.NotContains(t, string(encoded), "original_pricing")
}

func TestLegacyProviderSyncPreservesStatusAndSnapshotOnFetchFailure(t *testing.T) {
	setupProviderDB(t)
	failSync := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failSync {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v4-flash"},{"id":"gemini-2.5-flash-image"},{"id":"image2"}]}`))
		case "/api/pricing":
			_, _ = w.Write([]byte(`{"success":true,"pricing_version":"legacy-live","data":[
				{"model_name":"deepseek-v4-flash","model_ratio":1,"completion_ratio":2},
				{"model_name":"gemini-2.5-flash-image","quota_type":1,"model_price":0.06,"supported_endpoint_types":["gemini"]},
				{"model_name":"image2","quota_type":1,"model_price":0.05,"supported_endpoint_types":["image-generation"]}
			]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	overrideProviderDefinition(t, "zex", func(provider *Definition) {
		provider.BaseURL = server.URL
		provider.PricingURL = server.URL + "/api/pricing"
	})

	require.NoError(t, Configure("zex", "legacy-provider-key"))
	require.NoError(t, model.DB.Model(&model.Channel{}).
		Where("tag IN ?", []string{providerTag("zex", "chat"), providerTag("zex", "nano"), providerTag("zex", "gpt-image")}).
		Update("status", common.ChannelStatusEnabled).Error)
	count, err := Sync("zex")
	require.NoError(t, err)
	require.Equal(t, 3, count)
	channels, err := providerChannels("zex", true)
	require.NoError(t, err)
	require.Len(t, channels, 3)
	for _, channel := range channels {
		require.Equal(t, common.ChannelStatusEnabled, channel.Status)
		require.Greater(t, channel.TestTime, int64(0))
	}
	price := loadProviderPrice(t, channels[0].Id, channels[0].GetModels()[0])
	require.True(t, price.Available)
	require.Equal(t, price.SyncedAt, price.TestedAt)

	failSync = true
	_, err = Sync("zex")
	require.ErrorContains(t, err, "HTTP 502")
	channelsAfterFailure, queryErr := providerChannels("zex", true)
	require.NoError(t, queryErr)
	for _, channel := range channelsAfterFailure {
		require.Equal(t, common.ChannelStatusEnabled, channel.Status)
	}
	stored := loadProviderPrice(t, channels[0].Id, channels[0].GetModels()[0])
	require.Equal(t, price.BillingType, stored.BillingType)
	require.True(t, stored.Available)
}

func TestMatchCatalogEntryRequiresImageGenerationEndpoint(t *testing.T) {
	entry := CatalogEntry{ID: "gpt-image-2", Kind: "image"}
	models := map[string]struct{}{"gpt-image-2": {}, "image2": {}}
	pricing := map[string]pricingItem{
		"gpt-image-2": {ModelName: "gpt-image-2", QuotaType: 1, ModelPrice: 0.05, SupportedEndpoints: []string{"openai"}},
		"image2":      {ModelName: "image2", QuotaType: 1, ModelPrice: 0.05, SupportedEndpoints: []string{"openai", "image-generation"}},
	}

	upstreamID, _, reason := matchCatalogEntry("zex", entry, models, pricing)
	require.Empty(t, reason)
	require.Equal(t, "image2", upstreamID)
}

func TestMatchCatalogEntryResolvesClaudeAlias(t *testing.T) {
	entry := CatalogEntry{ID: "claude-fable-5-nc", Kind: "chat"}
	models := map[string]struct{}{"claude-fable-5": {}}
	pricing := map[string]pricingItem{
		"claude-fable-5": {ModelName: "claude-fable-5", ModelRatio: 5, CompletionRatio: 5},
	}

	upstreamID, _, reason := matchCatalogEntry("yunwu", entry, models, pricing)
	require.Empty(t, reason)
	require.Equal(t, "claude-fable-5", upstreamID)
}

func TestMatchCatalogEntryAllowsZexNativeGeminiImages(t *testing.T) {
	entry := CatalogEntry{ID: "nano-banana-2", Kind: "image"}
	models := map[string]struct{}{"gemini-3.1-flash-image-preview": {}}
	pricing := map[string]pricingItem{
		"gemini-3.1-flash-image-preview": {
			ModelName: "gemini-3.1-flash-image-preview", QuotaType: 1, ModelPrice: 0.12,
			SupportedEndpoints: []string{"gemini", "openai"},
		},
	}

	upstreamID, _, reason := matchCatalogEntry("zex", entry, models, pricing)
	require.Empty(t, reason)
	require.Equal(t, "gemini-3.1-flash-image-preview", upstreamID)
}

func TestMatchCatalogEntryRejectsZexNanoThatCannotUseGeminiChannel(t *testing.T) {
	entry := CatalogEntry{ID: "nano-banana", Kind: "image"}
	models := map[string]struct{}{"nano-banana": {}}
	pricing := map[string]pricingItem{
		"nano-banana": {
			ModelName: "nano-banana", QuotaType: 1, ModelPrice: 0.06,
			SupportedEndpoints: []string{"image-generation"},
		},
	}

	_, _, reason := matchCatalogEntry("zex", entry, models, pricing)
	require.Equal(t, "上游接口不兼容工作台图像生成协议", reason)
}

func TestMatchCatalogEntryExplainsMissingPrice(t *testing.T) {
	entry := CatalogEntry{ID: "grok-4.5", Kind: "chat"}
	models := map[string]struct{}{"grok-4.5": {}}

	_, _, reason := matchCatalogEntry("yunwu", entry, models, map[string]pricingItem{})

	require.Equal(t, "价格接口没有该上游模型的明确价格", reason)
}

func TestResolvePricingUsesVerifiedGeekNowNanoBananaSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	snapshot, err := resolvePricing(t.Context(), Definition{
		ID: "geeknow", PricingURL: server.URL,
	}, "relay-key")

	require.NoError(t, err)
	require.True(t, snapshot.Partial)
	require.Equal(t, "https://api.geeknow.ai/", snapshot.SourceURL)
	require.Equal(t, "verified-2026-08-13", snapshot.Version)
	require.Len(t, snapshot.Items, 1)
	require.Equal(t, "gemini-2.5-flash-image", snapshot.Items[0].ModelName)
	require.Equal(t, 0.06, snapshot.Items[0].ModelPrice)
	require.Contains(t, snapshot.Items[0].SupportedEndpoints, "image-generation")
}

func TestResolvePricingPrefersLiveGeekNowPricingWhenAvailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"pricing_version":"live-version","data":[{"model_name":"gemini-2.5-flash-image","quota_type":1,"model_price":0.07,"supported_endpoint_types":["image-generation"]}]}`))
	}))
	t.Cleanup(server.Close)

	snapshot, err := resolvePricing(t.Context(), Definition{
		ID: "geeknow", PricingURL: server.URL,
	}, "relay-key")

	require.NoError(t, err)
	require.False(t, snapshot.Partial)
	require.Equal(t, server.URL, snapshot.SourceURL)
	require.Equal(t, "live-version", snapshot.Version)
	require.Equal(t, 0.07, snapshot.Items[0].ModelPrice)
}

func TestPartialPricingOnlyTouchesCatalogEntriesWithVerifiedCandidates(t *testing.T) {
	pricing := map[string]pricingItem{
		"gemini-2.5-flash-image": {ModelName: "gemini-2.5-flash-image"},
	}

	require.True(t, hasPricedCandidateForProvider("geeknow", CatalogEntry{ID: "nano-banana"}, pricing))
	require.False(t, hasPricedCandidateForProvider("geeknow", CatalogEntry{ID: "nano-banana-pro"}, pricing))
	require.False(t, hasPricedCandidateForProvider("geeknow", CatalogEntry{ID: "deepseek-v4-flash"}, pricing))
}

func TestToChannelPriceNormalizesNewAPIRatios(t *testing.T) {
	provider := definitions[0]
	price, err := toChannelPrice(provider, "deepseek-v4-flash", "deepseek-v4-flash", pricingItem{
		ModelRatio: 0.5, CompletionRatio: 2, CacheRatio: 0.1,
	}, "version-1")

	require.NoError(t, err)
	require.Equal(t, model.ChannelModelBillingToken, price.BillingType)
	require.Equal(t, 1.0, price.InputPrice)
	require.Equal(t, 2.0, price.OutputPrice)
	require.Equal(t, "version-1", price.SourceVersion)
}

func TestToChannelPriceNormalizesFixedPrice(t *testing.T) {
	provider := definitions[0]
	price, err := toChannelPrice(provider, "gpt-image-2", "image2", pricingItem{
		QuotaType: 1, ModelPrice: 0.05,
	}, "version-2")

	require.NoError(t, err)
	require.Equal(t, model.ChannelModelBillingFixed, price.BillingType)
	require.Equal(t, 0.05, price.FixedPrice)
}

func TestReferenceCapabilityRequiresExplicitEditEndpointForGPT(t *testing.T) {
	provider := Definition{ID: "yunwu", PricingURL: "https://example.com/pricing"}
	price, err := toChannelPrice(provider, "gpt-image-2", "gpt-image-2", pricingItem{
		QuotaType: 1, ModelPrice: 0.05,
		SupportedEndpoints: []string{"openai", "image-generation", "openai编辑图片"},
	}, "version")

	require.NoError(t, err)
	require.Equal(t, ReferenceProtocolOpenAIEditMultipart, price.ReferenceProtocol)
	require.Equal(t, MaxReferenceImages, price.MaxReferenceImages)

	price, err = toChannelPrice(Definition{ID: "zex"}, "gpt-image-2", "gpt-image-2", pricingItem{
		QuotaType: 1, ModelPrice: 0.05, SupportedEndpoints: []string{"openai", "image-generation"},
	}, "version")
	require.NoError(t, err)
	require.Empty(t, price.ReferenceProtocol)
	require.Zero(t, price.MaxReferenceImages)
}

func TestReferenceCapabilityAllowsOnlyNativeGeminiNano(t *testing.T) {
	price, err := toChannelPrice(Definition{ID: "zex"}, "nano-banana-2", "gemini-3.1-flash-image-preview", pricingItem{
		QuotaType: 1, ModelPrice: 0.12, SupportedEndpoints: []string{"gemini", "openai"},
	}, "version")

	require.NoError(t, err)
	require.Equal(t, ReferenceProtocolGeminiInline, price.ReferenceProtocol)
	require.Equal(t, MaxReferenceImages, price.MaxReferenceImages)

	price, err = toChannelPrice(Definition{ID: "geeknow"}, "nano-banana", "gemini-2.5-flash-image", pricingItem{
		QuotaType: 1, ModelPrice: 0.06, SupportedEndpoints: []string{"image-generation"},
	}, "version")
	require.NoError(t, err)
	require.Empty(t, price.ReferenceProtocol)
}

func TestReferenceImageModelAvailableBackfillsOnlyExplicitLegacyCapability(t *testing.T) {
	setupProviderDB(t)
	priority := int64(100)
	channels := []model.Channel{
		{Id: 1, Key: "yunwu-key", Name: "Yunwu", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Id: 2, Key: "zex-key", Name: "Zex", Status: common.ChannelStatusEnabled, Priority: &priority},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	prices := []model.ChannelModelPrice{
		{
			ChannelID: 1, CatalogID: "gpt-image-2", Provider: "yunwu", UpstreamModelID: "gpt-image-2",
			BillingType: model.ChannelModelBillingFixed, FixedPrice: 0.05, Available: true,
			OriginalPricing: `{"supported_endpoint_types":["openai","openai编辑图片"]}`,
		},
		{
			ChannelID: 2, CatalogID: "nano-banana", Provider: "geeknow", UpstreamModelID: "gemini-2.5-flash-image",
			BillingType: model.ChannelModelBillingFixed, FixedPrice: 0.06, Available: true,
			OriginalPricing: `{"supported_endpoint_types":["image-generation"]}`,
		},
	}
	require.NoError(t, model.DB.Create(&prices).Error)

	available, err := ReferenceImageModelAvailable("gpt-image-2")
	require.NoError(t, err)
	require.True(t, available)
	var stored model.ChannelModelPrice
	require.NoError(t, model.DB.First(&stored, prices[0].ID).Error)
	require.Equal(t, ReferenceProtocolOpenAIEditMultipart, stored.ReferenceProtocol)
	require.Equal(t, MaxReferenceImages, stored.MaxReferenceImages)

	available, err = ReferenceImageModelAvailable("nano-banana")
	require.NoError(t, err)
	require.False(t, available)
}
