package mujianprovider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

const yuYuPricingOption = "_mujian_yuyu_pricing"
const yuYuPricingURL = "https://api.yu-yu.ai/api/pricing"

func yuYuCapabilities() providerCapabilities {
	capabilities := providerCapabilities{ChannelType: constant.ChannelTypeOpenAI, StrictLifecycle: true}
	// Image routes use verified native protocols and separate fixed/token pricing.
	for _, entry := range catalog {
		if entry.Kind == "chat" || entry.ID == "nano-banana-2" || entry.ID == "gpt-image-2" {
			capabilities.CatalogIDs = append(capabilities.CatalogIDs, entry.ID)
		}
	}
	return capabilities
}

// YuYu's dashboard pricing requires a login session, not a relay API key.
// Import its response as evidence instead of retaining browser credentials.
type YuYuPricingImport struct {
	UpstreamGroup string `json:"upstream_group"`
	Pricing       struct {
		Success    bool               `json:"success"`
		Version    string             `json:"pricing_version"`
		GroupRatio map[string]float64 `json:"group_ratio"`
		AutoGroups []string           `json:"auto_groups"`
		Data       []yuYuPricingItem  `json:"data"`
	} `json:"pricing"`
}

type yuYuPricingItem struct {
	pricingItem
	BillingMode  string   `json:"billing_mode"`
	BillingExpr  string   `json:"billing_expr"`
	EnableGroups []string `json:"enable_groups"`
}

type yuYuStoredPricing struct {
	YuYuPricingImport
	KeyDigest string `json:"key_digest"`
}

func yuYuKeyDigest(key string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(digest[:])
}

func ImportYuYuPricing(input YuYuPricingImport) error {
	if _, err := normalizeYuYuPricing(input); err != nil {
		return err
	}
	mutex := providerConfigureMutex("yuyu")
	mutex.Lock()
	defer mutex.Unlock()
	provider, _ := definition("yuyu")
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := acquireProviderAdvisoryLock(tx, "yuyu"); err != nil {
			return err
		}
		channels, err := providerChannelsWithDB(tx, provider, true, true)
		if err != nil {
			return err
		}
		if len(channels) == 0 || strings.TrimSpace(channels[0].Key) == "" {
			return errors.New("请先保存羽宇 API Key")
		}
		key := channels[0].Key
		for _, channel := range channels {
			if channel.Key != key {
				return errors.New("羽宇渠道密钥不一致，请重新保存 API Key")
			}
		}
		raw, err := common.Marshal(yuYuStoredPricing{YuYuPricingImport: input, KeyDigest: yuYuKeyDigest(key)})
		if err != nil {
			return err
		}
		if err = tx.Save(&model.Option{Key: yuYuPricingOption, Value: string(raw)}).Error; err != nil {
			return err
		}
		if err = failClosedProviderTx(tx, provider, channels, "已导入羽宇价格，请同步模型并测试后启用", true); err != nil {
			return err
		}
		return advanceProviderLifecycleGeneration(tx, provider.ID)
	})
	if err == nil {
		model.InitChannelCache()
	}
	return err
}

func resolveYuYuPricing(key string) (pricingSnapshot, error) {
	var option model.Option
	err := model.DB.Where("key = ?", yuYuPricingOption).First(&option).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return pricingSnapshot{}, errors.New("羽宇价格接口使用登录认证，请先导入官方价格 JSON")
	}
	if err != nil {
		return pricingSnapshot{}, err
	}
	var stored yuYuStoredPricing
	if err = common.UnmarshalJsonStr(option.Value, &stored); err != nil {
		return pricingSnapshot{}, errors.New("羽宇价格快照损坏，请重新导入")
	}
	if stored.KeyDigest != yuYuKeyDigest(key) {
		return pricingSnapshot{}, errors.New("羽宇 API Key 已变化，请重新导入当前账户的价格")
	}
	return normalizeYuYuPricing(stored.YuYuPricingImport)
}

func normalizeYuYuPricing(input YuYuPricingImport) (pricingSnapshot, error) {
	p := input.Pricing
	if !p.Success || len(p.Data) == 0 || strings.TrimSpace(p.Version) == "" {
		return pricingSnapshot{}, errors.New("请导入成功返回且包含 pricing_version 的羽宇完整价格响应")
	}
	groups := []string{input.UpstreamGroup}
	if input.UpstreamGroup == "auto" {
		groups = p.AutoGroups
	}
	if input.UpstreamGroup == "" || len(groups) == 0 {
		return pricingSnapshot{}, errors.New("缺少羽宇令牌的上游分组")
	}
	for _, group := range groups {
		ratio, ok := p.GroupRatio[group]
		if !ok || !validPositivePrice(ratio) {
			return pricingSnapshot{}, fmt.Errorf("上游分组 %s 缺少有效倍率", group)
		}
	}
	raw, err := common.Marshal(input)
	if err != nil {
		return pricingSnapshot{}, err
	}
	digest := sha256.Sum256(raw)
	snapshot := pricingSnapshot{SourceURL: yuYuPricingURL, Version: "import-" + hex.EncodeToString(digest[:])}
	seen := map[string]bool{}
	for _, entry := range p.Data {
		if entry.ModelName == "" || seen[entry.ModelName] {
			return pricingSnapshot{}, errors.New("价格响应包含空模型名或重复模型")
		}
		seen[entry.ModelName] = true
		item := entry.pricingItem
		original, err := common.Marshal(entry)
		if err != nil {
			return pricingSnapshot{}, err
		}
		item.OriginalPricing = string(original)
		ratio := 0.0
		for _, group := range groups {
			if !containsString(entry.EnableGroups, group) && !containsString(entry.EnableGroups, "all") {
				continue
			}
			if ratio != 0 && ratio != p.GroupRatio[group] {
				item.ValidationError = "自动分组存在不同价格，请使用固定上游分组后重新导入"
				break
			}
			ratio = p.GroupRatio[group]
		}
		if ratio == 0 {
			item.ValidationError = "模型未向令牌上游分组开放"
		}
		if item.ValidationError == "" {
			item.ValidationError = normalizeYuYuItem(&item, entry, ratio)
		}
		snapshot.Items = append(snapshot.Items, item)
	}
	return snapshot, nil
}

var yuYuLinearTier = regexp.MustCompile(`^tier\("base",\s*(.+)\)$`)
var yuYuTerm = regexp.MustCompile(`^(p|c|cr|cc|cc1h|img_o)\s*\*\s*([0-9]+(?:\.[0-9]+)?)$`)

func normalizeYuYuItem(item *pricingItem, entry yuYuPricingItem, groupRatio float64) string {
	if entry.BillingMode != "" && entry.BillingMode != "tiered_expr" {
		return "不支持的羽宇计费模式"
	}
	if entry.BillingMode == "tiered_expr" || entry.BillingExpr != "" {
		match := yuYuLinearTier.FindStringSubmatch(strings.TrimSpace(entry.BillingExpr))
		if match == nil {
			return "当前渠道账单不支持上下文阶梯计费"
		}
		prices := map[string]float64{}
		for _, term := range strings.Split(match[1], "+") {
			parts := yuYuTerm.FindStringSubmatch(strings.TrimSpace(term))
			if parts == nil {
				return "当前渠道账单不支持该计费表达式的变量或运算"
			}
			if _, exists := prices[parts[1]]; exists {
				return "计费表达式包含重复变量"
			}
			value, err := strconv.ParseFloat(parts[2], 64)
			if err != nil || !validNonNegativePrice(value) {
				return "计费表达式价格无效"
			}
			prices[parts[1]] = value
		}
		if !validPositivePrice(prices["p"]) || !validPositivePrice(prices["c"]) {
			return "计费表达式缺少输入或输出单价"
		}
		if math.Abs(prices["cc1h"]-prices["cc"]*claudeCacheCreationOneHourMultiplier) > 1e-9 {
			return "一小时缓存价格与当前账单协议不兼容"
		}
		if imagePrice, ok := prices["img_o"]; ok {
			if entry.ModelName != "gpt-image-2" || !validPositivePrice(imagePrice) || imagePrice*groupRatio > maxExternalTokenPriceUSDPerMillion {
				return "不支持该模型的图片输出计费"
			}
			item.ImageOutputPrice = imagePrice * groupRatio
		}
		item.QuotaType, item.ModelPrice = 0, 0
		item.InputPrice, item.OutputPrice = prices["p"]*groupRatio, prices["c"]*groupRatio
		item.CacheRatio, item.CreateCacheRatio = prices["cr"]/prices["p"], prices["cc"]/prices["p"]
		if item.ImageOutputPrice > 0 {
			if _, separatelyPriced := prices["cr"]; !separatelyPriced {
				item.CacheRatio = 1
			}
		}
	} else {
		if entry.QuotaType != 0 && entry.QuotaType != 1 {
			return "不支持的计费类型"
		}
		if entry.QuotaType == 1 {
			item.ModelPrice *= groupRatio
			if !validPositivePrice(item.ModelPrice) {
				return "模型缺少有效按次价格"
			}
			return ""
		}
		if item.ModelPrice != 0 {
			return "Token 计费包含冲突的按次价格"
		}
		item.InputPrice = item.ModelRatio * 2 * groupRatio
		item.OutputPrice = item.InputPrice * item.CompletionRatio
	}
	if !validPositivePrice(item.InputPrice) || !validPositivePrice(item.OutputPrice) ||
		item.InputPrice > maxExternalTokenPriceUSDPerMillion || item.OutputPrice > maxExternalTokenPriceUSDPerMillion ||
		!validNonNegativePrice(item.CacheRatio) || !validNonNegativePrice(item.CreateCacheRatio) ||
		!validNonNegativePrice(item.InputPrice*item.CacheRatio) || !validNonNegativePrice(item.InputPrice*item.CreateCacheRatio*claudeCacheCreationOneHourMultiplier) {
		return "模型 Token 价格或缓存倍率无效"
	}
	return ""
}
