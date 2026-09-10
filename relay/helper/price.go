package helper

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/mujianprovider"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func modelPriceNotConfiguredError(modelName string, userId int) error {
	if model.IsAdmin(userId) {
		return fmt.Errorf(
			"模型 %s 的价格未配置。请前往「系统设置 → 运营设置」开启自用模式，或在「系统设置 → 分组与模型定价设置」中为该模型配置价格；"+
				"Model %s price not configured. Go to System Settings → Operation Settings to enable self-use mode, or configure the model price in System Settings → Group & Model Pricing.",
			modelName, modelName,
		)
	}
	return fmt.Errorf(
		"模型 %s 的价格尚未由管理员配置，暂时无法使用，请联系站点管理员开启该模型；"+
			"Model %s has not been priced by the administrator yet. Please contact the site administrator to enable this model.",
		modelName, modelName,
	)
}

// https://docs.claude.com/en/docs/build-with-claude/prompt-caching#1-hour-cache-duration
const claudeCacheCreation1hMultiplier = 6 / 3.75

// HandleGroupRatio checks for "auto_group" in the context and updates the group ratio and relayInfo.UsingGroup if present
func HandleGroupRatio(ctx *gin.Context, relayInfo *relaycommon.RelayInfo) types.GroupRatioInfo {
	// check auto group
	autoGroup, exists := ctx.Get("auto_group")
	if exists {
		logger.LogDebug(ctx, fmt.Sprintf("final group: %s", autoGroup))
		relayInfo.UsingGroup = autoGroup.(string)
	}

	return groupRatioInfo(relayInfo.UserGroup, relayInfo.UsingGroup)
}

func groupRatioInfo(userGroup, usingGroup string) types.GroupRatioInfo {
	info := types.GroupRatioInfo{
		GroupRatio:        1.0,
		GroupSpecialRatio: -1,
	}
	userGroupRatio, ok := ratio_setting.GetGroupGroupRatio(userGroup, usingGroup)
	if ok {
		info.GroupSpecialRatio = userGroupRatio
		info.GroupRatio = userGroupRatio
		info.HasSpecialRatio = true
	} else {
		info.GroupRatio = ratio_setting.GetGroupRatio(usingGroup)
	}
	return info
}

func ModelPriceHelper(c *gin.Context, info *relaycommon.RelayInfo, promptTokens int, meta *types.TokenCountMeta) (types.PriceData, error) {
	groupRatioInfo := HandleGroupRatio(c, info)
	if !mujianprovider.IsCatalogModel(info.OriginModelName) {
		priceData, err := legacyModelPriceData(info, promptTokens, meta, groupRatioInfo)
		if err != nil {
			return types.PriceData{}, err
		}
		info.LegacyPriceData = nil
		info.PriceData = priceData
		return priceData, nil
	}
	allowedChannelIDs, err := pricingAllowedChannelIDs(c)
	if err != nil {
		return types.PriceData{}, err
	}
	retryBudget := common.RetryTimes
	_, specificChannel := common.GetContextKey(c, constant.ContextKeyTokenSpecificChannelId)
	if specificChannel || service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		retryBudget = 0
	}
	pricingGroups := []string{info.UsingGroup}
	// Cross-group retries can settle against any reachable auto group, so the
	// preauthorization must cover the most expensive routable outcome.
	if retryBudget > 0 && info.TokenGroup == "auto" && common.GetContextKeyBool(c, constant.ContextKeyTokenCrossGroupRetry) {
		if autoGroups := service.AutoGroupsForRequest(c, info.UserGroup); len(autoGroups) > 0 {
			// Auto retry starts at the selected group's saved index and never
			// returns to an earlier group. If configuration changed and the
			// selected group disappeared, the selector restarts at index zero.
			selectedGroupFound := false
			for index, autoGroup := range autoGroups {
				if autoGroup == info.UsingGroup {
					pricingGroups = append(pricingGroups, autoGroups[index+1:]...)
					selectedGroupFound = true
					break
				}
			}
			if !selectedGroupFound {
				pricingGroups = append(pricingGroups, autoGroups...)
			}
		}
	}
	initialChannelID := common.GetContextKeyInt(c, constant.ContextKeyChannelId)
	initialManaged := c.GetBool("mujian_managed_provider")
	initialSnapshot, _ := common.GetContextKeyType[types.ChannelModelPriceSnapshot](c, constant.ContextKeyManagedChannelPrice)
	if initialManaged && (initialSnapshot.PriceID <= 0 || initialSnapshot.ChannelID != initialChannelID ||
		initialSnapshot.CatalogID != info.OriginModelName || initialSnapshot.RoutingGroup != info.UsingGroup) {
		return types.PriceData{}, errors.New("所选托管渠道缺少有效的请求价格快照")
	}
	usesClaudeWebSearch, err := requestUsesClaudeWebSearch(info.Request)
	if err != nil {
		return types.PriceData{}, err
	}
	usesUnboundedClaudeMedia, err := relaycommon.ClaudeRequestUsesUnboundedMedia(info.Request)
	if err != nil {
		return types.PriceData{}, err
	}
	if usesClaudeWebSearch && initialManaged && isStrictClaudePriceProvider(initialSnapshot.Provider) {
		return types.PriceData{}, errors.New("托管 Claude 渠道暂不支持未接入按次价格的 web_search")
	}
	if usesUnboundedClaudeMedia {
		if initialManaged {
			return types.PriceData{}, relaycommon.UnboundedClaudeMediaError()
		}
		initialPrice, priceErr := model.GetAvailableChannelModelPrice(initialChannelID, info.OriginModelName)
		switch {
		case priceErr == nil && isStrictClaudePriceProvider(initialPrice.Provider):
			return types.PriceData{}, relaycommon.UnboundedClaudeMediaError()
		case priceErr != nil && !errors.Is(priceErr, gorm.ErrRecordNotFound):
			return types.PriceData{}, priceErr
		}
	}
	locallyExcludedChannelIDs := service.LocallyExcludedChannelIDsForRequest(c)
	if priceData, legacyFallback, authorizedRoutes, found, err := channelModelPreConsumePriceForGroups(info, pricingGroups, allowedChannelIDs, locallyExcludedChannelIDs, promptTokens, meta, initialChannelID, initialManaged, initialSnapshot, retryBudget, usesClaudeWebSearch, usesUnboundedClaudeMedia); err != nil {
		return types.PriceData{}, err
	} else if found {
		if len(authorizedRoutes) == 0 {
			return types.PriceData{}, errors.New("请求未找到可预授权的渠道")
		}
		bindPriceAuthorizationBounds(c, promptTokens, meta, authorizedRoutes)
		info.AuthorizedPromptTokens, info.AuthorizedCompletionTokens = preConsumeTokenBounds(promptTokens, meta)
		info.LegacyPriceData = legacyFallback
		info.PriceData = priceData
		return priceData, nil
	}

	priceData, err := legacyModelPriceData(info, promptTokens, meta, groupRatioInfo)
	if err != nil {
		return types.PriceData{}, err
	}
	info.LegacyPriceData = nil
	info.PriceData = priceData
	return priceData, nil
}

// ValidateClaudeMediaBeforeTokenCount rejects an initially selected strict
// price route before token counting can download a remote image or document.
// ModelPriceHelper repeats the check because channel tests and internal callers
// can invoke pricing without passing through the public relay controller.
func ValidateClaudeMediaBeforeTokenCount(c *gin.Context, info *relaycommon.RelayInfo) error {
	if info == nil || !mujianprovider.IsClaudeCatalogModel(info.OriginModelName) {
		return nil
	}
	usesUnboundedMedia, err := relaycommon.ClaudeRequestUsesUnboundedMedia(info.Request)
	if err != nil || !usesUnboundedMedia {
		return err
	}
	if c.GetBool("mujian_managed_provider") || (info.ChannelMeta != nil && info.ChannelMeta.ManagedProvider) {
		return relaycommon.UnboundedClaudeMediaError()
	}
	channelID := common.GetContextKeyInt(c, constant.ContextKeyChannelId)
	price, err := model.GetAvailableChannelModelPrice(channelID, info.OriginModelName)
	if err == nil && isStrictClaudePriceProvider(price.Provider) {
		return relaycommon.UnboundedClaudeMediaError()
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}

func requestUsesClaudeWebSearch(request dto.Request) (bool, error) {
	switch typed := request.(type) {
	case *dto.GeneralOpenAIRequest:
		return typed != nil && typed.WebSearchOptions != nil, nil
	case *dto.ClaudeRequest:
		if typed == nil || typed.Tools == nil {
			return false, nil
		}
		data, err := common.Marshal(typed.Tools)
		if err != nil {
			return false, errors.New("Claude tools 无法用于价格预授权")
		}
		return claudeToolPayloadUsesWebSearch(data)
	case *dto.OpenAIResponsesRequest:
		if typed == nil || len(typed.Tools) == 0 {
			return false, nil
		}
		return claudeToolPayloadUsesWebSearch(typed.Tools)
	default:
		return false, nil
	}
}

func claudeToolPayloadUsesWebSearch(data []byte) (bool, error) {
	var payload any
	if err := common.Unmarshal(data, &payload); err != nil {
		return false, errors.New("Claude tools 格式无法用于价格预授权")
	}
	var tools []any
	switch typed := payload.(type) {
	case []any:
		tools = typed
	case map[string]any:
		tools = []any{typed}
	case nil:
		return false, nil
	default:
		return false, errors.New("Claude tools 格式无法用于价格预授权")
	}
	for _, tool := range tools {
		object, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		toolType, _ := object["type"].(string)
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(toolType)), "web_search") {
			return true, nil
		}
	}
	return false, nil
}

func pricingAllowedChannelIDs(c *gin.Context) ([]int, error) {
	allowedChannelIDs, hasAllowedChannelIDs := common.GetContextKeyType[[]int](c, constant.ContextKeyAllowedChannelIds)
	value, hasSpecificChannel := common.GetContextKey(c, constant.ContextKeyTokenSpecificChannelId)
	if !hasSpecificChannel {
		if !hasAllowedChannelIDs {
			return nil, nil
		}
		return allowedChannelIDs, nil
	}
	rawChannelID, ok := value.(string)
	if !ok {
		return nil, errors.New("指定渠道 ID 格式无效")
	}
	channelID, err := strconv.Atoi(rawChannelID)
	if err != nil || channelID <= 0 {
		return nil, errors.New("指定渠道 ID 格式无效")
	}
	if hasAllowedChannelIDs {
		allowed := false
		for _, candidateID := range allowedChannelIDs {
			if candidateID == channelID {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, errors.New("指定渠道不在当前请求的可用范围内")
		}
	}
	return []int{channelID}, nil
}

func legacyModelPriceData(info *relaycommon.RelayInfo, promptTokens int, meta *types.TokenCountMeta, groupRatioInfo types.GroupRatioInfo) (types.PriceData, error) {

	modelPrice, usePrice := ratio_setting.GetModelPrice(info.OriginModelName, false)

	var preConsumedQuota int
	var modelRatio float64
	var completionRatio float64
	var cacheRatio float64
	var imageRatio float64
	var cacheCreationRatio float64
	var cacheCreationRatio5m float64
	var cacheCreationRatio1h float64
	var audioRatio float64
	var audioCompletionRatio float64
	var freeModel bool
	unitPriceMultiplier := 1.0
	if !usePrice {
		preConsumedTokens := common.Max(promptTokens, common.PreConsumedQuota)
		if meta != nil && meta.MaxTokens != 0 {
			preConsumedTokens += meta.MaxTokens
		}
		var success bool
		var matchName string
		modelRatio, success, matchName = ratio_setting.GetModelRatio(info.OriginModelName)
		if !success {
			acceptUnsetRatio := false
			if info.UserSetting.AcceptUnsetRatioModel {
				acceptUnsetRatio = true
			}
			if !acceptUnsetRatio {
				return types.PriceData{}, modelPriceNotConfiguredError(matchName, info.UserId)
			}
		}
		completionRatio = ratio_setting.GetCompletionRatio(info.OriginModelName)
		cacheRatio, _ = ratio_setting.GetCacheRatio(info.OriginModelName)
		cacheCreationRatio, _ = ratio_setting.GetCreateCacheRatio(info.OriginModelName)
		cacheCreationRatio5m = cacheCreationRatio
		// 固定1h和5min缓存写入价格的比例
		cacheCreationRatio1h = cacheCreationRatio * claudeCacheCreation1hMultiplier
		imageRatio, _ = ratio_setting.GetImageRatio(info.OriginModelName)
		audioRatio = ratio_setting.GetAudioRatio(info.OriginModelName)
		audioCompletionRatio = ratio_setting.GetAudioCompletionRatio(info.OriginModelName)
		ratio := modelRatio * groupRatioInfo.GroupRatio
		preConsumedQuota = int(float64(preConsumedTokens) * ratio)
	} else {
		if meta != nil && meta.ImagePriceRatio != 0 {
			unitPriceMultiplier = meta.ImagePriceRatio
			modelPrice = modelPrice * meta.ImagePriceRatio
		}
		preConsumedQuota = int(modelPrice * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
	}

	priceData := types.PriceData{
		FreeModel:            freeModel,
		ModelPrice:           modelPrice,
		ModelRatio:           modelRatio,
		CompletionRatio:      completionRatio,
		GroupRatioInfo:       groupRatioInfo,
		UsePrice:             usePrice,
		CacheRatio:           cacheRatio,
		ImageRatio:           imageRatio,
		AudioRatio:           audioRatio,
		AudioCompletionRatio: audioCompletionRatio,
		CacheCreationRatio:   cacheCreationRatio,
		CacheCreation5mRatio: cacheCreationRatio5m,
		CacheCreation1hRatio: cacheCreationRatio1h,
		QuotaToPreConsume:    preConsumedQuota,
		UnitPriceMultiplier:  unitPriceMultiplier,
	}
	applyFreeModelPreConsumePolicy(&priceData)

	if common.DebugEnabled {
		println(fmt.Sprintf("model_price_helper result: %s", priceData.ToSetting()))
	}
	return priceData, nil
}

func legacyCatalogPreConsumePriceData(info *relaycommon.RelayInfo, promptTokens int, meta *types.TokenCountMeta, group types.GroupRatioInfo) (types.PriceData, error) {
	data, err := legacyModelPriceData(info, promptTokens, meta, group)
	if err != nil || data.UsePrice {
		return data, err
	}
	promptBound, completionBound := preConsumeTokenBounds(promptTokens, meta)
	quota := (float64(promptBound)*worstPromptBillingRatio(data) +
		float64(completionBound)*data.CompletionRatio) * data.ModelRatio * group.GroupRatio
	data.QuotaToPreConsume, err = checkedPreConsumeQuota(quota)
	if err != nil {
		return types.PriceData{}, err
	}
	applyFreeModelPreConsumePolicy(&data)
	return data, nil
}

func channelModelPreConsumePriceForGroups(info *relaycommon.RelayInfo, groups []string, allowedChannelIDs, locallyExcludedChannelIDs []int, promptTokens int, meta *types.TokenCountMeta, initialChannelID int, initialManaged bool, initialSnapshot types.ChannelModelPriceSnapshot, retryBudget int, usesClaudeWebSearch, usesUnboundedClaudeMedia bool) (types.PriceData, *types.PriceData, []priceAuthorizedRoute, bool, error) {
	var selected types.PriceData
	var legacyFallback *types.PriceData
	foundAny := false
	requiresChannelBinding := false
	seen := make(map[string]struct{}, len(groups))
	authorizedRoutes := make([]priceAuthorizedRoute, 0)
	authorizedRouteSet := make(map[string]struct{})
	authorize := func(channelID int, routingGroup string) {
		if channelID <= 0 {
			return
		}
		key := fmt.Sprintf("%d\x00%s", channelID, routingGroup)
		if _, exists := authorizedRouteSet[key]; exists {
			return
		}
		authorizedRouteSet[key] = struct{}{}
		authorizedRoutes = append(authorizedRoutes, priceAuthorizedRoute{ChannelID: channelID, RoutingGroup: routingGroup})
	}
	for _, routingGroup := range groups {
		if _, ok := seen[routingGroup]; ok {
			continue
		}
		seen[routingGroup] = struct{}{}
		group := groupRatioInfo(info.UserGroup, routingGroup)
		routes, err := routableChannelPriceViews(info.OriginModelName, routingGroup, allowedChannelIDs)
		if err != nil {
			return types.PriceData{}, nil, nil, false, err
		}
		initialGroup := routingGroup == info.UsingGroup
		routes = priceAuthorizableRoutes(routes, info.OriginModelName, routingGroup, locallyExcludedChannelIDs, usesClaudeWebSearch, usesUnboundedClaudeMedia)
		routes = reachablePriceRoutes(routes, initialChannelID, initialGroup, retryBudget)
		stopAtRetryFrontier := false
		if retryBudget == 1 {
			for _, route := range routes {
				if !initialGroup || route.channelID != initialChannelID {
					stopAtRetryFrontier = true
					break
				}
			}
		}
		promptBound, completionBound := preConsumeTokenBounds(promptTokens, meta)
		multiplier := 1.0
		if meta != nil && meta.ImagePriceRatio > 0 {
			multiplier = meta.ImagePriceRatio
		}
		needsLegacy := false
		initialCovered := false
		if initialManaged && initialSnapshot.PriceID > 0 && initialSnapshot.ChannelID == initialChannelID &&
			initialSnapshot.CatalogID == info.OriginModelName && initialSnapshot.RoutingGroup == routingGroup {
			boundPrice := channelModelPriceFromRequestSnapshot(initialSnapshot)
			candidate, candidateErr := preConsumePriceFromSnapshot(boundPrice, group, multiplier, promptBound, completionBound)
			if candidateErr != nil {
				return types.PriceData{}, nil, nil, false, candidateErr
			}
			initialCovered = true
			requiresChannelBinding = true
			authorize(initialChannelID, routingGroup)
			if preferPreConsumeCandidate(candidate, selected, foundAny) {
				selected = candidate
				foundAny = true
			}
		}
		for _, route := range routes {
			if route.channelID == initialChannelID {
				initialCovered = true
			}
			if route.price != nil {
				candidate, candidateErr := preConsumePriceFromSnapshot(*route.price, group, multiplier, promptBound, completionBound)
				if candidateErr != nil {
					return types.PriceData{}, nil, nil, false, candidateErr
				}
				requiresChannelBinding = true
				authorize(route.channelID, routingGroup)
				if preferPreConsumeCandidate(candidate, selected, foundAny) {
					selected = candidate
					foundAny = true
				}
				continue
			}
			if _, strict := mujianprovider.StrictProviderIDForTag(route.channelTag); strict {
				requiresChannelBinding = true
				continue
			}
			authorize(route.channelID, routingGroup)
			needsLegacy = true
		}
		if routingGroup == info.UsingGroup && initialChannelID > 0 && !initialCovered {
			price, priceErr := model.GetAvailableChannelModelPrice(initialChannelID, info.OriginModelName)
			switch {
			case priceErr == nil:
				direct, directErr := preConsumePriceFromSnapshot(*price, group, multiplier, promptBound, completionBound)
				if directErr != nil {
					return types.PriceData{}, nil, nil, false, directErr
				}
				requiresChannelBinding = true
				authorize(initialChannelID, routingGroup)
				if preferPreConsumeCandidate(direct, selected, foundAny) {
					selected = direct
					foundAny = true
				}
			case !errors.Is(priceErr, gorm.ErrRecordNotFound):
				return types.PriceData{}, nil, nil, false, priceErr
			case initialManaged:
				requiresChannelBinding = true
			default:
				authorize(initialChannelID, routingGroup)
				needsLegacy = true
			}
		}
		if needsLegacy {
			legacy, err := legacyCatalogPreConsumePriceData(info, promptTokens, meta, group)
			if err != nil {
				return types.PriceData{}, nil, nil, false, err
			}
			if legacyFallback == nil {
				fallback := legacy
				legacyFallback = &fallback
			}
			if preferPreConsumeCandidate(legacy, selected, foundAny) {
				selected = legacy
				foundAny = true
			}
		}
		if stopAtRetryFrontier {
			break
		}
	}
	if requiresChannelBinding && !foundAny {
		return types.PriceData{}, nil, nil, false, errors.New("受管渠道缺少已验证的模型价格")
	}
	if requiresChannelBinding {
		selected.ChannelSpecific = true
	} else {
		legacyFallback = nil
	}
	return selected, legacyFallback, authorizedRoutes, foundAny, nil
}

func priceAuthorizableRoutes(routes []routableChannelPriceView, catalogID, routingGroup string, excludedChannelIDs []int, usesClaudeWebSearch, usesUnboundedClaudeMedia bool) []routableChannelPriceView {
	excluded := make(map[int]struct{}, len(excludedChannelIDs))
	for _, channelID := range excludedChannelIDs {
		excluded[channelID] = struct{}{}
	}
	selected := make([]routableChannelPriceView, 0, len(routes))
	for _, route := range routes {
		if _, skip := excluded[route.channelID]; skip {
			continue
		}
		if route.price != nil && isStrictClaudePriceProvider(route.price.Provider) &&
			(usesClaudeWebSearch || usesUnboundedClaudeMedia) {
			continue
		}
		_, strict := mujianprovider.StrictProviderIDForTag(route.channelTag)
		if !strict {
			selected = append(selected, route)
			continue
		}
		if route.price == nil {
			continue
		}
		tag := route.channelTag
		cached := model.Channel{Id: route.channelID, Tag: &tag}
		_, snapshot, managed, err := mujianprovider.LoadManagedRelayChannelForRequest(cached, catalogID, routingGroup)
		if err != nil || !managed || snapshot.PriceID != route.price.ID {
			continue
		}
		price := channelModelPriceFromRequestSnapshot(snapshot)
		route.price = &price
		selected = append(selected, route)
	}
	return selected
}

func isStrictClaudePriceProvider(provider string) bool {
	return provider == types.PriceProviderZenMux || provider == types.PriceProviderTabCode
}

// reachablePriceRoutes mirrors the configured one-retry selector without
// letting lower, unreachable priority tiers inflate balance authorization.
// For larger custom retry budgets it deliberately keeps the conservative
// all-routes behavior until the selector's mixed managed/ordinary transitions
// can be represented without under-authorization.
func reachablePriceRoutes(routes []routableChannelPriceView, initialChannelID int, initialGroup bool, retryBudget int) []routableChannelPriceView {
	if len(routes) == 0 || initialChannelID <= 0 || retryBudget > 1 {
		return routes
	}
	selected := make([]routableChannelPriceView, 0, len(routes))
	seen := make(map[int]struct{}, len(routes))
	appendRoute := func(route routableChannelPriceView) {
		if _, exists := seen[route.channelID]; exists {
			return
		}
		seen[route.channelID] = struct{}{}
		selected = append(selected, route)
	}
	if !initialGroup {
		if retryBudget == 0 {
			return nil
		}
		remaining := make([]routableChannelPriceView, 0, len(routes))
		for _, route := range routes {
			if route.channelID != initialChannelID {
				remaining = append(remaining, route)
			}
		}
		for _, route := range routesAtPriority(remaining, highestRoutePriority(remaining)) {
			appendRoute(route)
		}
		return selected
	}
	for _, route := range routes {
		if route.channelID == initialChannelID {
			appendRoute(route)
		}
	}
	if retryBudget == 0 {
		return selected
	}
	remaining := make([]routableChannelPriceView, 0, len(routes))
	for _, route := range routes {
		if route.channelID != initialChannelID {
			remaining = append(remaining, route)
		}
	}
	for _, route := range routesAtPriority(remaining, highestRoutePriority(remaining)) {
		appendRoute(route)
	}
	return selected
}

func highestRoutePriority(routes []routableChannelPriceView) int64 {
	if len(routes) == 0 {
		return 0
	}
	priority := routes[0].priority
	for _, route := range routes[1:] {
		if route.priority > priority {
			priority = route.priority
		}
	}
	return priority
}

func routesAtPriority(routes []routableChannelPriceView, priority int64) []routableChannelPriceView {
	selected := make([]routableChannelPriceView, 0)
	hasPositiveWeight := false
	for _, route := range routes {
		if route.priority == priority {
			selected = append(selected, route)
			hasPositiveWeight = hasPositiveWeight || route.weight > 0
		}
	}
	if !common.MemoryCacheEnabled || !hasPositiveWeight {
		return selected
	}
	weighted := selected[:0]
	for _, route := range selected {
		if route.weight > 0 {
			weighted = append(weighted, route)
		}
	}
	return weighted
}

func preferPreConsumeCandidate(candidate, selected types.PriceData, selectedSet bool) bool {
	if !selectedSet || candidate.QuotaToPreConsume > selected.QuotaToPreConsume {
		return true
	}
	return candidate.QuotaToPreConsume == selected.QuotaToPreConsume && selected.FreeModel && !candidate.FreeModel
}

func applyFreeModelPreConsumePolicy(data *types.PriceData) {
	data.FreeModel = false
	if operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
		return
	}
	if data.GroupRatioInfo.GroupRatio == 0 || (data.UsePrice && data.ModelPrice == 0) || (!data.UsePrice && data.ModelRatio == 0) {
		data.QuotaToPreConsume = 0
		data.FreeModel = true
	}
}

// ModelPriceHelperPerCall 按次/按量计费的 PriceHelper (MJ、Task)
func ModelPriceHelperPerCall(c *gin.Context, info *relaycommon.RelayInfo) (types.PriceData, error) {
	groupRatioInfo := HandleGroupRatio(c, info)

	modelPrice, success := ratio_setting.GetModelPrice(info.OriginModelName, true)
	usePrice := success
	var modelRatio float64

	if !success {
		defaultPrice, ok := ratio_setting.GetDefaultModelPriceMap()[info.OriginModelName]
		if ok {
			modelPrice = defaultPrice
			usePrice = true
		} else {
			var ratioSuccess bool
			var matchName string
			modelRatio, ratioSuccess, matchName = ratio_setting.GetModelRatio(info.OriginModelName)
			acceptUnsetRatio := false
			if info.UserSetting.AcceptUnsetRatioModel {
				acceptUnsetRatio = true
			}
			if !ratioSuccess && !acceptUnsetRatio {
				return types.PriceData{}, modelPriceNotConfiguredError(matchName, info.UserId)
			}
		}
	}

	var quota int
	freeModel := false

	if usePrice {
		quota = int(modelPrice * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
			if groupRatioInfo.GroupRatio == 0 || modelPrice == 0 {
				quota = 0
				freeModel = true
			}
		}
	} else {
		// 按量计费：以模型倍率的一半作为预扣额度
		quota = int(modelRatio / 2 * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		modelPrice = -1
		if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
			if groupRatioInfo.GroupRatio == 0 || modelRatio == 0 {
				quota = 0
				freeModel = true
			}
		}
	}

	priceData := types.PriceData{
		FreeModel:      freeModel,
		ModelPrice:     modelPrice,
		ModelRatio:     modelRatio,
		UsePrice:       usePrice,
		Quota:          quota,
		GroupRatioInfo: groupRatioInfo,
	}
	return priceData, nil
}

func ContainPriceOrRatio(modelName string) bool {
	_, ok := ratio_setting.GetModelPrice(modelName, false)
	if ok {
		return true
	}
	_, ok, _ = ratio_setting.GetModelRatio(modelName)
	if ok {
		return true
	}
	return false
}
