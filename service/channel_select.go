package service

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/mujianprovider"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

type RetryParam struct {
	Ctx               *gin.Context
	TokenGroup        string
	ModelName         string
	AllowedChannelIDs []int
	Retry             *int
}

const managedChannelDBValidatedContextKey = "mujian_managed_channel_db_validated"
const locallyExcludedChannelIDsContextKey = "mujian_locally_excluded_channels"
const autoGroupRouteSnapshotContextKey = "mujian_auto_group_route_snapshot"

type managedChannelValidation struct {
	ChannelID    int
	CatalogID    string
	RoutingGroup string
}

// AutoGroupsForRequest freezes the ordered auto-group candidates on first
// use so distribution, price authorization, and retries share one cursor.
func AutoGroupsForRequest(c *gin.Context, userGroup string) []string {
	if c != nil {
		if snapshot, ok := c.Get(autoGroupRouteSnapshotContextKey); ok {
			if groups, valid := snapshot.([]string); valid {
				return append([]string(nil), groups...)
			}
		}
	}
	groups := GetUserAutoGroup(userGroup)
	groups = append([]string(nil), groups...)
	if c != nil {
		c.Set(autoGroupRouteSnapshotContextKey, groups)
	}
	return append([]string(nil), groups...)
}

func MarkManagedChannelDBValidated(c *gin.Context, catalogID string, price types.ChannelModelPriceSnapshot) {
	if c != nil {
		c.Set(managedChannelDBValidatedContextKey, managedChannelValidation{
			ChannelID: price.ChannelID, CatalogID: catalogID, RoutingGroup: price.RoutingGroup,
		})
		common.SetContextKey(c, constant.ContextKeyManagedChannelPrice, price)
	}
}

func IsManagedChannelDBValidated(c *gin.Context, channelID int, catalogID, routingGroup string) bool {
	if c == nil {
		return false
	}
	value, ok := c.Get(managedChannelDBValidatedContextKey)
	validation, valid := value.(managedChannelValidation)
	return ok && valid && validation.ChannelID == channelID &&
		validation.CatalogID == catalogID && validation.RoutingGroup == routingGroup
}

func CurrentRoutingGroup(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if group := common.GetContextKeyString(c, constant.ContextKeyAutoGroup); group != "" {
		return group
	}
	return common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
}

func attemptedChannelIDs(c *gin.Context) []int {
	if c == nil {
		return nil
	}
	return parseChannelIDs(c.GetStringSlice("use_channel"))
}

func locallyExcludedChannelIDs(c *gin.Context) []int {
	if c == nil {
		return nil
	}
	return parseChannelIDs(c.GetStringSlice(locallyExcludedChannelIDsContextKey))
}

func LocallyExcludedChannelIDsForRequest(c *gin.Context) []int {
	return locallyExcludedChannelIDs(c)
}

func catalogPriceAllowedChannelIDs(c *gin.Context, group string, requested []int) []int {
	if !common.GetContextKeyBool(c, constant.ContextKeyCatalogPriceAuthorized) {
		return requested
	}
	routes, ok := common.GetContextKeyType[map[string][]int](c, constant.ContextKeyCatalogPriceRoutes)
	authorized, exists := routes[group]
	if !ok || !exists {
		return []int{}
	}
	if requested == nil {
		return append([]int(nil), authorized...)
	}
	requestedSet := make(map[int]struct{}, len(requested))
	for _, channelID := range requested {
		requestedSet[channelID] = struct{}{}
	}
	intersection := make([]int, 0, len(authorized))
	for _, channelID := range authorized {
		if _, allowed := requestedSet[channelID]; allowed {
			intersection = append(intersection, channelID)
		}
	}
	return intersection
}

func parseChannelIDs(values []string) []int {
	ids := make([]int, 0, len(values))
	for _, value := range values {
		id, err := strconv.Atoi(value)
		if err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func (p *RetryParam) GetRetry() int {
	if p.Retry == nil {
		return 0
	}
	return *p.Retry
}

func (p *RetryParam) IncreaseRetry() {
	if p.Retry == nil {
		p.Retry = new(int)
	}
	(*p.Retry)++
}

// CacheGetRandomSatisfiedChannel returns a channel for the current route attempt.
// For auto routing, ContextKeyAutoGroupIndex tracks the active group while
// ContextKeyAutoGroupRetryIndex is a priority cursor local to that group. The
// selector never mutates RetryParam.Retry: the controller owns that global
// attempt budget and advances it monotonically.
func CacheGetRandomSatisfiedChannel(param *RetryParam) (*model.Channel, string, error) {
	for {
		channel, group, err := selectCachedChannel(param)
		if err != nil || channel == nil {
			return channel, group, err
		}
		current, price, managed, validationErr := mujianprovider.LoadManagedRelayChannelForRequest(*channel, param.ModelName, group)
		if !managed {
			return channel, group, nil
		}
		if validationErr == nil {
			MarkManagedChannelDBValidated(param.Ctx, param.ModelName, price)
			return &current, group, nil
		}

		// A different instance may have fail-closed this route after our local
		// cache refresh. Exclude the stale candidate without spending an upstream
		// retry, then continue to the next cached route.
		logger.LogWarn(param.Ctx, fmt.Sprintf("Skipping stale managed channel %d: %s", channel.Id, validationErr.Error()))
		excluded := param.Ctx.GetStringSlice(locallyExcludedChannelIDsContextKey)
		excluded = append(excluded, strconv.Itoa(channel.Id))
		param.Ctx.Set(locallyExcludedChannelIDsContextKey, excluded)
		param.Ctx.Set("mujian_managed_provider", true)
	}
}

func selectCachedChannel(param *RetryParam) (*model.Channel, string, error) {
	var channel *model.Channel
	var err error
	selectGroup := param.TokenGroup
	userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)
	attemptedIDs := attemptedChannelIDs(param.Ctx)
	excludedChannelIDs := locallyExcludedChannelIDs(param.Ctx)
	if param.Ctx.GetBool("mujian_managed_provider") ||
		common.GetContextKeyBool(param.Ctx, constant.ContextKeyCatalogPriceAuthorized) {
		excludedChannelIDs = append(excludedChannelIDs, attemptedIDs...)
	}

	if param.TokenGroup == "auto" {
		autoGroups := AutoGroupsForRequest(param.Ctx, userGroup)
		if len(autoGroups) == 0 {
			return nil, selectGroup, errors.New("no auto group is available for the current user")
		}

		startGroupIndex := 0
		if lastGroupIndex, exists := common.GetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex); exists {
			if idx, ok := lastGroupIndex.(int); ok {
				startGroupIndex = idx
			}
		} else if currentGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyAutoGroup); currentGroup != "" {
			for i, autoGroup := range autoGroups {
				if autoGroup == currentGroup {
					startGroupIndex = i
					break
				}
			}
		}
		if startGroupIndex >= len(autoGroups) {
			return nil, selectGroup, nil
		}

		priorityIndex := param.GetRetry()
		if retryIndex, exists := common.GetContextKey(param.Ctx, constant.ContextKeyAutoGroupRetryIndex); exists {
			if idx, ok := retryIndex.(int); ok && idx >= 0 {
				priorityIndex = idx
			}
		}
		lastGroupIndex := len(autoGroups) - 1
		isRetry := param.GetRetry() > 0 || len(attemptedIDs) > 0
		// Initial routing may scan forward to find the first viable group. Once an
		// upstream has been attempted, leaving that group requires token consent.
		if isRetry && !common.GetContextKeyBool(param.Ctx, constant.ContextKeyTokenCrossGroupRetry) {
			lastGroupIndex = startGroupIndex
		}

		for i := startGroupIndex; i <= lastGroupIndex; i++ {
			autoGroup := autoGroups[i]
			if i > startGroupIndex {
				priorityIndex = 0
			}
			logger.LogDebug(param.Ctx, "Auto selecting group: %s, priorityRetry: %d", autoGroup, priorityIndex)

			channel, _ = model.GetRandomSatisfiedChannelWithFilters(
				autoGroup, param.ModelName, priorityIndex,
				catalogPriceAllowedChannelIDs(param.Ctx, autoGroup, param.AllowedChannelIDs), excludedChannelIDs,
			)
			if channel == nil {
				logger.LogDebug(param.Ctx, "No available channel in group %s for model %s at priorityRetry %d", autoGroup, param.ModelName, priorityIndex)
				if i < lastGroupIndex {
					common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i+1)
					common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupRetryIndex, 0)
				}
				continue
			}
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, autoGroup)
			selectGroup = autoGroup
			logger.LogDebug(param.Ctx, "Auto selected group: %s", autoGroup)

			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i)
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupRetryIndex, priorityIndex+1)
			break
		}
	} else {
		channel, err = model.GetRandomSatisfiedChannelWithFilters(
			param.TokenGroup, param.ModelName, param.GetRetry(),
			catalogPriceAllowedChannelIDs(param.Ctx, param.TokenGroup, param.AllowedChannelIDs), excludedChannelIDs,
		)
		if err != nil {
			return nil, param.TokenGroup, err
		}
	}
	return channel, selectGroup, nil
}
