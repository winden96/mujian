package helper

import (
	"errors"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type routableChannelPriceView struct {
	channelID  int
	channelTag string
	priority   int64
	weight     uint
	price      *model.ChannelModelPrice
}

type priceAuthorizedRoute struct {
	ChannelID    int
	RoutingGroup string
}

const priceAuthorizationBoundsContextKey = "mujian_price_authorization_bounds"

type priceAuthorizationBounds struct {
	PromptTokens     int
	CompletionTokens int
	Multiplier       float64
	Routes           []priceAuthorizedRoute
}

func bindPriceAuthorizationBounds(c *gin.Context, promptTokens int, meta *types.TokenCountMeta, routes []priceAuthorizedRoute) {
	if c == nil {
		return
	}
	promptBound, completionBound := preConsumeTokenBounds(promptTokens, meta)
	multiplier := 1.0
	if meta != nil && meta.ImagePriceRatio > 0 {
		multiplier = meta.ImagePriceRatio
	}
	c.Set(priceAuthorizationBoundsContextKey, priceAuthorizationBounds{
		PromptTokens: promptBound, CompletionTokens: completionBound, Multiplier: multiplier,
		Routes: append([]priceAuthorizedRoute(nil), routes...),
	})
	if len(routes) > 0 {
		routesByGroup := make(map[string][]int)
		for _, route := range routes {
			routesByGroup[route.RoutingGroup] = append(routesByGroup[route.RoutingGroup], route.ChannelID)
		}
		common.SetContextKey(c, constant.ContextKeyCatalogPriceAuthorized, true)
		common.SetContextKey(c, constant.ContextKeyCatalogPriceRoutes, routesByGroup)
	}
}

func priceAuthorizationForRequest(c *gin.Context) (priceAuthorizationBounds, error) {
	if c == nil {
		// Nil contexts are used only by isolated helper tests and legacy callers.
		// Every HTTP relay request supplies a Gin context and is fail-closed below.
		return priceAuthorizationBounds{}, nil
	}
	value, ok := c.Get(priceAuthorizationBoundsContextKey)
	if !ok {
		return priceAuthorizationBounds{}, errors.New("请求缺少渠道价格预授权边界")
	}
	bounds, ok := value.(priceAuthorizationBounds)
	if !ok || bounds.PromptTokens < 0 || bounds.CompletionTokens < 0 ||
		!finitePositive(bounds.Multiplier) || len(bounds.Routes) == 0 {
		return priceAuthorizationBounds{}, errors.New("请求的渠道价格预授权边界无效")
	}
	for _, route := range bounds.Routes {
		if route.ChannelID <= 0 || route.RoutingGroup == "" || route.RoutingGroup == "auto" {
			return priceAuthorizationBounds{}, errors.New("请求的渠道价格预授权边界无效")
		}
	}
	return bounds, nil
}

func (bounds priceAuthorizationBounds) authorizes(channelID int, routingGroup string) bool {
	for _, route := range bounds.Routes {
		if route.ChannelID == channelID && route.RoutingGroup == routingGroup {
			return true
		}
	}
	return false
}

func routableChannelPriceViews(modelID, routingGroup string, allowedChannelIDs []int) ([]routableChannelPriceView, error) {
	cachedRoutes, cached := model.SnapshotCachedChannelRoutes(routingGroup, modelID, allowedChannelIDs)
	if !cached {
		return routableChannelPriceViewsFromDB(modelID, routingGroup, allowedChannelIDs)
	}
	if len(cachedRoutes) == 0 {
		return []routableChannelPriceView{}, nil
	}
	localIDs := make([]int, 0, len(cachedRoutes))
	for _, route := range cachedRoutes {
		localIDs = append(localIDs, route.ChannelID)
	}
	databaseRoutes, err := routableChannelPriceViewsFromDB(modelID, routingGroup, localIDs)
	if err != nil {
		return nil, err
	}
	databaseByID := make(map[int]routableChannelPriceView, len(databaseRoutes))
	for _, route := range databaseRoutes {
		databaseByID[route.channelID] = route
	}
	views := make([]routableChannelPriceView, 0, len(cachedRoutes))
	for _, cachedRoute := range cachedRoutes {
		route, ok := databaseByID[cachedRoute.ChannelID]
		if !ok || route.channelTag != cachedRoute.Tag {
			// The selector still sees a stale route generation. Excluding it is
			// fail-closed and lets the normal retry path move to a healthy route.
			continue
		}
		route.priority = cachedRoute.Priority
		route.weight = cachedRoute.Weight
		views = append(views, route)
	}
	return views, nil
}

func routableChannelPriceViewsFromDB(modelID, routingGroup string, allowedChannelIDs []int) ([]routableChannelPriceView, error) {
	states, err := model.ListRoutableChannelPriceViewsForGroup(modelID, routingGroup, allowedChannelIDs)
	if err != nil {
		return nil, err
	}
	views := make([]routableChannelPriceView, 0, len(states))
	for _, state := range states {
		views = append(views, routableChannelPriceView{
			channelID: state.ChannelID, channelTag: state.ChannelTag,
			priority: state.RoutePriority, weight: state.RouteWeight, price: state.ModelPrice(modelID),
		})
	}
	return views, nil
}

func channelModelPreConsumePrice(modelID, routingGroup string, allowedChannelIDs []int, promptTokens int, meta *types.TokenCountMeta, group types.GroupRatioInfo) (types.PriceData, bool, error) {
	prices, err := model.ListRoutableChannelModelPricesForGroup(modelID, routingGroup, allowedChannelIDs)
	if err != nil || len(prices) == 0 {
		return types.PriceData{}, false, err
	}
	multiplier := 1.0
	if meta != nil && meta.ImagePriceRatio > 0 {
		multiplier = meta.ImagePriceRatio
	}
	promptPreConsumedTokens, maxCompletionTokens := preConsumeTokenBounds(promptTokens, meta)
	var selected types.PriceData
	selectedSet := false
	for _, price := range prices {
		candidate, err := preConsumePriceFromSnapshot(price, group, multiplier, promptPreConsumedTokens, maxCompletionTokens)
		if err != nil {
			return types.PriceData{}, false, err
		}
		if preferPreConsumeCandidate(candidate, selected, selectedSet) {
			selected = candidate
			selectedSet = true
		}
	}
	return selected, true, nil
}

func preConsumeTokenBounds(promptTokens int, meta *types.TokenCountMeta) (int, int) {
	promptPreConsumedTokens := common.Max(promptTokens, common.PreConsumedQuota)
	if meta == nil {
		return promptPreConsumedTokens, 0
	}
	return promptPreConsumedTokens, meta.MaxTokens
}

func preConsumePriceFromSnapshot(price model.ChannelModelPrice, group types.GroupRatioInfo, multiplier float64, promptTokens, maxCompletionTokens int) (types.PriceData, error) {
	if !finiteNonNegative(group.GroupRatio) {
		return types.PriceData{}, errors.New("渠道分组倍率无法安全计费")
	}
	candidate, err := priceDataFromSnapshot(price, group, multiplier)
	if err != nil {
		return types.PriceData{}, err
	}
	var quota decimal.Decimal
	if candidate.UsePrice {
		quota = decimal.NewFromFloat(price.FixedPrice).Mul(decimal.NewFromFloat(multiplier)).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	} else {
		weightedPrompt := decimal.NewFromInt(int64(promptTokens)).Mul(decimal.NewFromFloat(worstPromptBillingRatio(candidate)))
		weightedOutput := decimal.NewFromInt(int64(maxCompletionTokens)).Mul(decimal.NewFromFloat(math.Max(candidate.CompletionRatio, candidate.ImageCompletionRatio)))
		quota = weightedPrompt.Add(weightedOutput).Mul(decimal.NewFromFloat(candidate.ModelRatio))
	}
	quota = quota.Mul(decimal.NewFromFloat(group.GroupRatio)).Mul(decimal.NewFromFloat(candidate.SalesRatio()))
	candidate.QuotaToPreConsume, err = checkedPreConsumeDecimal(quota)
	if err != nil {
		return types.PriceData{}, err
	}
	applyFreeModelPreConsumePolicy(&candidate)
	return candidate, nil
}

// ApplyChannelModelPrice replaces the worst-case preauthorization snapshot
// with the price of the selected channel while preserving the preauthorized
// amount. Settlement can therefore refund the difference after a retry.
// A fixed retail image quote caps customer liability independently of upstream costs.
func ApplyChannelModelPrice(c *gin.Context, info *relaycommon.RelayInfo, channelID int, strictManaged bool) error {
	if !info.PriceData.ChannelSpecific {
		if c == nil {
			if strictManaged {
				return errors.New("所选受管渠道缺少请求价格预授权")
			}
			return nil
		}
		if _, authorizedCatalogRequest := c.Get(priceAuthorizationBoundsContextKey); !authorizedCatalogRequest {
			if strictManaged {
				return errors.New("所选受管渠道缺少请求价格预授权")
			}
			return nil
		}
	}
	bounds, err := priceAuthorizationForRequest(c)
	if err != nil {
		return err
	}
	if c != nil && !info.PriceData.ChannelSpecific && !bounds.authorizes(channelID, info.UsingGroup) {
		return errors.New("所选渠道不在本次请求的价格预授权范围内")
	}
	if !info.PriceData.ChannelSpecific {
		if strictManaged {
			return errors.New("所选受管渠道未纳入本次请求的渠道价格预授权")
		}
		preConsumed := info.PriceData.QuotaToPreConsume
		meta := &types.TokenCountMeta{MaxTokens: bounds.CompletionTokens, ImagePriceRatio: bounds.Multiplier}
		required, legacyErr := legacyCatalogPreConsumePriceData(info, bounds.PromptTokens, meta, info.PriceData.GroupRatioInfo)
		if legacyErr != nil {
			return legacyErr
		}
		if info.ImageRetail == nil && required.QuotaToPreConsume > preConsumed {
			return errors.New("所选渠道的当前价格超过本次请求已预授权额度")
		}
		actual, legacyErr := legacyModelPriceData(info, bounds.PromptTokens, meta, info.PriceData.GroupRatioInfo)
		if legacyErr != nil {
			return legacyErr
		}
		actual.QuotaToPreConsume = preConsumed
		info.PriceData = actual
		return nil
	}
	var price *model.ChannelModelPrice
	if strictManaged {
		price, err = managedChannelPriceForRequest(c, info, channelID)
	} else {
		price, err = model.GetAvailableChannelModelPrice(channelID, info.OriginModelName)
	}
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("查询所选渠道的模型价格失败")
		}
		if strictManaged || info.LegacyPriceData == nil {
			return errors.New("所选渠道缺少已验证的模型价格")
		}
		preConsumed := info.PriceData.QuotaToPreConsume
		actual := *info.LegacyPriceData
		if c != nil {
			meta := &types.TokenCountMeta{MaxTokens: bounds.CompletionTokens, ImagePriceRatio: bounds.Multiplier}
			required, requiredErr := legacyCatalogPreConsumePriceData(info, bounds.PromptTokens, meta, info.PriceData.GroupRatioInfo)
			if requiredErr != nil {
				return requiredErr
			}
			if info.ImageRetail == nil && required.QuotaToPreConsume > preConsumed {
				return errors.New("所选渠道的当前价格超过本次请求已预授权额度")
			}
			actual, err = legacyModelPriceData(info, bounds.PromptTokens, meta, info.PriceData.GroupRatioInfo)
			if err != nil {
				return err
			}
		}
		actual.GroupRatioInfo = info.PriceData.GroupRatioInfo
		actual.ChannelSpecific = true
		actual.PriceProvider = ""
		applyFreeModelPreConsumePolicy(&actual)
		actual.QuotaToPreConsume = preConsumed
		info.PriceData = actual
		return nil
	}
	if info.RelayMode == relayconstant.RelayModeImagesEdits {
		if !price.SupportsReferenceImages() {
			return errors.New("所选渠道未声明当前模型的多图参考协议")
		}
	}
	preConsumed := info.PriceData.QuotaToPreConsume
	if c != nil {
		required, requiredErr := preConsumePriceFromSnapshot(
			*price, info.PriceData.GroupRatioInfo, bounds.Multiplier, bounds.PromptTokens, bounds.CompletionTokens,
		)
		if requiredErr != nil {
			return requiredErr
		}
		if info.ImageRetail == nil && required.QuotaToPreConsume > preConsumed {
			return errors.New("所选渠道的当前价格超过本次请求已预授权额度")
		}
	}
	actualMultiplier := info.PriceData.UnitPriceMultiplier
	if c != nil {
		actualMultiplier = bounds.Multiplier
	}
	actual, err := priceDataFromSnapshot(*price, info.PriceData.GroupRatioInfo, actualMultiplier)
	if err != nil {
		return err
	}
	applyFreeModelPreConsumePolicy(&actual)
	actual.QuotaToPreConsume = preConsumed
	info.PriceData = actual
	return nil
}

func managedChannelPriceForRequest(c *gin.Context, info *relaycommon.RelayInfo, channelID int) (*model.ChannelModelPrice, error) {
	if c == nil || info == nil {
		return nil, fmt.Errorf("%w: 所选托管渠道缺少请求价格快照", gorm.ErrRecordNotFound)
	}
	snapshot, ok := common.GetContextKeyType[types.ChannelModelPriceSnapshot](c, constant.ContextKeyManagedChannelPrice)
	if !ok || snapshot.PriceID <= 0 || snapshot.ChannelID != channelID ||
		snapshot.CatalogID != info.OriginModelName || snapshot.RoutingGroup != info.UsingGroup {
		return nil, fmt.Errorf("%w: 所选托管渠道的请求价格快照无效", gorm.ErrRecordNotFound)
	}
	price := channelModelPriceFromRequestSnapshot(snapshot)
	return &price, nil
}

func channelModelPriceFromRequestSnapshot(snapshot types.ChannelModelPriceSnapshot) model.ChannelModelPrice {
	return model.ChannelModelPrice{
		ID: snapshot.PriceID, ChannelID: snapshot.ChannelID, CatalogID: snapshot.CatalogID,
		UpstreamModelID: snapshot.UpstreamModelID, Provider: snapshot.Provider,
		BillingType: snapshot.BillingType, Currency: snapshot.Currency,
		InputPrice: snapshot.InputPrice, OutputPrice: snapshot.OutputPrice, ImageOutputPrice: snapshot.ImageOutputPrice, FixedPrice: snapshot.FixedPrice,
		CacheRatio: snapshot.CacheRatio, CacheCreationRatio: snapshot.CacheCreationRatio,
		ReferenceProtocol: snapshot.ReferenceProtocol, MaxReferenceImages: snapshot.MaxReferenceImages,
		Available: true,
	}
}

func priceDataFromSnapshot(price model.ChannelModelPrice, group types.GroupRatioInfo, multiplier float64) (types.PriceData, error) {
	data := types.PriceData{
		GroupRatioInfo: group, ChannelSpecific: true, UnitPriceMultiplier: multiplier,
		PriceProvider: price.Provider, ModelPrice: -1,
		CacheRatio: price.CacheRatio, CacheCreationRatio: price.CacheCreationRatio,
		CacheCreation5mRatio: price.CacheCreationRatio,
		CacheCreation1hRatio: price.CacheCreationRatio * claudeCacheCreation1hMultiplier,
	}
	if price.BillingType == model.ChannelModelBillingFixed {
		data.UsePrice = true
		data.ModelPrice = price.FixedPrice * multiplier
		if !finitePositive(data.ModelPrice) {
			return types.PriceData{}, fmt.Errorf("渠道 %d 的按次价格无法安全计费", price.ChannelID)
		}
		return data, nil
	}
	if !finiteNonNegative(price.ImageOutputPrice) {
		return types.PriceData{}, fmt.Errorf("渠道 %d 的图片输出价格无效", price.ChannelID)
	}
	if price.ImageOutputPrice > 0 {
		data.ImageRatio = 1 // Input image tokens share the input rate in this price expression.
		data.ImageCompletionRatio = price.ImageOutputPrice / price.InputPrice
		if !finitePositive(data.ImageCompletionRatio) {
			return types.PriceData{}, fmt.Errorf("渠道 %d 的图片输出价格无效", price.ChannelID)
		}
	}
	data.ModelRatio = price.InputPrice / 2
	data.CompletionRatio = price.OutputPrice / price.InputPrice
	if !finitePositive(data.ModelRatio) || !finitePositive(data.CompletionRatio) ||
		!finiteNonNegative(data.CacheRatio) || !finiteNonNegative(data.CacheCreationRatio) ||
		!finiteNonNegative(data.CacheCreation1hRatio) {
		return types.PriceData{}, fmt.Errorf("渠道 %d 的 Token 价格无法安全计费", price.ChannelID)
	}
	return data, nil
}

func worstPromptBillingRatio(data types.PriceData) float64 {
	return math.Max(1, math.Max(data.CacheRatio, math.Max(data.CacheCreation5mRatio, data.CacheCreation1hRatio)))
}

func checkedPreConsumeQuota(value float64) (int, error) {
	maxInt := int(^uint(0) >> 1)
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > float64(maxInt) {
		return 0, errors.New("渠道价格计算超出可安全预授权范围")
	}
	return checkedPreConsumeDecimal(decimal.NewFromFloat(value))
}

func checkedPreConsumeDecimal(value decimal.Decimal) (int, error) {
	rounded := value.Ceil()
	if rounded.IsNegative() || rounded.GreaterThan(decimal.NewFromInt(int64(^uint(0)>>1))) {
		return 0, errors.New("渠道价格计算超出可安全预授权范围")
	}
	return int(rounded.IntPart()), nil
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
