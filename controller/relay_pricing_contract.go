package controller

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/setting/billing_setting"

	"github.com/gin-gonic/gin"
)

const relayPricingCacheCreation1hMultiplier = 6 / 3.75

type relayPricingSource struct {
	modelRatio           map[string]float64
	completionRatio      map[string]float64
	cacheRatio           map[string]float64
	createCacheRatio     map[string]float64
	createCache5mRatio   map[string]float64
	createCache1hRatio   map[string]float64
	imageRatio           map[string]float64
	audioRatio           map[string]float64
	audioCompletionRatio map[string]float64
	modelPrice           map[string]float64
	billingMode          map[string]string
	billingExpr          map[string]string
}

func GetRelayPricingContractV1(c *gin.Context) {
	pricingSnapshot := billing_setting.GetPricingSnapshot()
	quotaPerUnit := pricingSnapshot.QuotaPerUnit
	if math.IsNaN(quotaPerUnit) || math.IsInf(quotaPerUnit, 0) || quotaPerUnit <= 0 {
		common.SysError("relay pricing contract is unavailable because QuotaPerUnit is invalid")
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "pricing configuration is unavailable"})
		return
	}

	groupRatios := pricingSnapshot.GroupRatio
	upstreamGroup := strings.TrimSpace(c.Query("group"))
	createCacheRatio := pricingSnapshot.CreateCacheRatio
	createCache1hRatio := make(map[string]float64, len(createCacheRatio))
	for modelName, ratio := range createCacheRatio {
		createCache1hRatio[modelName] = ratio * relayPricingCacheCreation1hMultiplier
	}
	source := relayPricingSource{
		modelRatio:           pricingSnapshot.ModelRatio,
		completionRatio:      pricingSnapshot.CompletionRatio,
		cacheRatio:           pricingSnapshot.CacheRatio,
		createCacheRatio:     createCacheRatio,
		createCache5mRatio:   createCacheRatio,
		createCache1hRatio:   createCache1hRatio,
		imageRatio:           pricingSnapshot.ImageRatio,
		audioRatio:           pricingSnapshot.AudioRatio,
		audioCompletionRatio: pricingSnapshot.AudioCompletionRatio,
		modelPrice:           pricingSnapshot.ModelPrice,
		billingMode:          pricingSnapshot.BillingMode,
		billingExpr:          pricingSnapshot.BillingExpr,
	}
	pricing, err := buildRelayPricingInputs(source)
	if err != nil {
		common.SysError("relay pricing contract is unavailable: " + err.Error())
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "pricing configuration is unavailable"})
		return
	}
	models, err := buildRelayPricingModels(source)
	if err != nil {
		common.SysError("relay pricing contract is unavailable: " + err.Error())
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "pricing configuration is unavailable"})
		return
	}

	contract := dto.RelayPricingContract{
		ContractVersion: dto.RelayPricingContractV1,
		QuotaPerUnit:    strconv.FormatFloat(quotaPerUnit, 'f', -1, 64),
		Pricing:         pricing,
		Models:          models,
		Capabilities: map[string]dto.RelayPricingCapability{
			"ratio_token": {
				Status: "supported",
				Detail: "Model, completion, cache, image, and audio ratios are exported before the selected group ratio.",
			},
			"model_price": {
				Status: "supported",
				Detail: "Model prices are USD per request before the selected group ratio and request-specific multipliers.",
			},
			"cache_read": {
				Status: "supported",
				Detail: "Cache read ratios require usage with separately reported cache read tokens.",
			},
			"cache_creation": {
				Status: "supported",
				Detail: "Both 5 minute and derived 1 hour cache creation ratios are exported.",
			},
			"image": {
				Status: "requires_request_usage",
				Detail: "Image token ratios are exported; per-call image generation charges are not part of this contract version.",
			},
			"audio": {
				Status: "requires_endpoint_specific_accounting",
				Detail: "Audio ratios are exported, but some relay formats use endpoint-specific audio prices that are not represented by generic ratios.",
			},
			"tool_calls": {
				Status: "unsupported",
				Detail: "Request tool-call surcharges are not exported by this contract version.",
			},
			"other_ratios": {
				Status: "unsupported",
				Detail: "Request-specific OtherRatios are not exported by this contract version.",
			},
			"tiered_expr": {
				Status: "requires_compatible_expression_engine",
				Detail: "Billing expressions are exported, but exact evaluation requires New API billingexpr v1-compatible token normalization and request rules.",
			},
			"upstream_group_binding": {
				Status: "caller_supplied_and_validated",
				Detail: "The caller must bind each upstream relay key to an explicit group; this endpoint validates only the supplied group and never infers a key group.",
			},
			"token_user_group_overrides": {
				Status: "unsupported",
				Detail: "Per-user group overrides cannot be inferred without using the relay key and are outside this management contract.",
			},
		},
	}
	if upstreamGroup == "" {
		contract.GroupRatios, err = formatRelayPricingDecimalMap("group_ratio", groupRatios)
		if err != nil {
			common.SysError("relay pricing contract is unavailable: " + err.Error())
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "pricing configuration is unavailable"})
			return
		}
	} else {
		groupRatio, ok := groupRatios[upstreamGroup]
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "group is not configured"})
			return
		}
		contract.UpstreamGroup = upstreamGroup
		contract.GroupRatio, err = formatRelayPricingDecimal(groupRatio)
		if err != nil {
			common.SysError("relay pricing contract is unavailable because the selected group ratio is invalid")
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "pricing configuration is unavailable"})
			return
		}
	}

	revisionPayload, err := common.Marshal(contract)
	if err != nil {
		common.SysError("failed to build relay pricing snapshot revision: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to build pricing snapshot"})
		return
	}
	revisionDigest := common.Sha256Raw(revisionPayload)
	contract.SnapshotRevision = int64(binary.BigEndian.Uint64(revisionDigest[:8]) & ((uint64(1) << 63) - 1))
	etagPayload, err := common.Marshal(contract)
	if err != nil {
		common.SysError("failed to build relay pricing snapshot ETag: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to build pricing snapshot"})
		return
	}
	etag := `"` + hex.EncodeToString(common.Sha256Raw(etagPayload)) + `"`

	c.Header("Cache-Control", "no-store")
	c.Header("ETag", etag)
	if aetherETagMatches(c.GetHeader("If-None-Match"), etag) {
		c.Status(http.StatusNotModified)
		c.Writer.WriteHeaderNow()
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": contract})
}

func buildRelayPricingModels(source relayPricingSource) (map[string]dto.RelayPricingModel, error) {
	modelNames := make(map[string]struct{})
	for modelName := range source.modelRatio {
		modelNames[modelName] = struct{}{}
	}
	for modelName := range source.modelPrice {
		modelNames[modelName] = struct{}{}
	}
	for modelName := range source.completionRatio {
		modelNames[modelName] = struct{}{}
	}
	for modelName := range source.cacheRatio {
		modelNames[modelName] = struct{}{}
	}
	for modelName := range source.createCacheRatio {
		modelNames[modelName] = struct{}{}
	}
	for modelName := range source.imageRatio {
		modelNames[modelName] = struct{}{}
	}
	for modelName := range source.audioRatio {
		modelNames[modelName] = struct{}{}
	}
	for modelName := range source.audioCompletionRatio {
		modelNames[modelName] = struct{}{}
	}
	for modelName := range source.billingMode {
		modelNames[modelName] = struct{}{}
	}
	for modelName := range source.billingExpr {
		modelNames[modelName] = struct{}{}
	}

	models := make(map[string]dto.RelayPricingModel, len(modelNames))
	for modelName := range modelNames {
		billingMode := source.billingMode[modelName]
		if billingMode == "" {
			billingMode = billing_setting.BillingModeRatio
		}
		model := dto.RelayPricingModel{BillingMode: billingMode}

		switch billingMode {
		case billing_setting.BillingModeTieredExpr:
			model.ChargeMode = "tiered_expr"
			model.CostEstimationStatus = "requires_compatible_expression_engine"
			if expr, ok := source.billingExpr[modelName]; ok && strings.TrimSpace(expr) != "" {
				model.BillingExpr = common.GetPointer(expr)
				model.BillingExprVersion = common.GetPointer(billingexpr.ExprVersion(expr))
			} else {
				model.CostEstimationStatus = "invalid_configuration"
			}
		case billing_setting.BillingModeRatio:
			if modelPrice, ok := source.modelPrice[modelName]; ok {
				formatted, err := formatRelayPricingDecimal(modelPrice)
				if err != nil {
					return nil, fmt.Errorf("model_price[%s]: %w", modelName, err)
				}
				model.ChargeMode = "fixed_price"
				model.CostEstimationStatus = "supported"
				model.ModelPrice = common.GetPointer(formatted)
				break
			}

			modelRatio, ok := source.modelRatio[modelName]
			if !ok {
				model.ChargeMode = "unresolved"
				model.CostEstimationStatus = "unsupported_missing_base_price"
				break
			}

			completionRatio, ok := source.completionRatio[modelName]
			if !ok {
				completionRatio = 1
			}
			cacheRatio, ok := source.cacheRatio[modelName]
			if !ok {
				cacheRatio = 1
			}
			createCacheRatio, ok := source.createCache5mRatio[modelName]
			if !ok {
				createCacheRatio = 1.25
			}
			createCache1hRatio, ok := source.createCache1hRatio[modelName]
			if !ok {
				createCache1hRatio = createCacheRatio * relayPricingCacheCreation1hMultiplier
			}
			imageRatio, ok := source.imageRatio[modelName]
			if !ok {
				imageRatio = 1
			}
			audioRatio, ok := source.audioRatio[modelName]
			if !ok {
				audioRatio = 1
			}
			audioCompletionRatio, ok := source.audioCompletionRatio[modelName]
			if !ok {
				audioCompletionRatio = 1
			}
			modelRatioText, err := formatRelayPricingDecimal(modelRatio)
			if err != nil {
				return nil, fmt.Errorf("model_ratio[%s]: %w", modelName, err)
			}
			completionRatioText, err := formatRelayPricingDecimal(completionRatio)
			if err != nil {
				return nil, fmt.Errorf("completion_ratio[%s]: %w", modelName, err)
			}
			cacheRatioText, err := formatRelayPricingDecimal(cacheRatio)
			if err != nil {
				return nil, fmt.Errorf("cache_ratio[%s]: %w", modelName, err)
			}
			createCache5mRatioText, err := formatRelayPricingDecimal(createCacheRatio)
			if err != nil {
				return nil, fmt.Errorf("create_cache_5m_ratio[%s]: %w", modelName, err)
			}
			createCache1hRatioText, err := formatRelayPricingDecimal(createCache1hRatio)
			if err != nil {
				return nil, fmt.Errorf("create_cache_1h_ratio[%s]: %w", modelName, err)
			}
			imageRatioText, err := formatRelayPricingDecimal(imageRatio)
			if err != nil {
				return nil, fmt.Errorf("image_ratio[%s]: %w", modelName, err)
			}
			audioRatioText, err := formatRelayPricingDecimal(audioRatio)
			if err != nil {
				return nil, fmt.Errorf("audio_ratio[%s]: %w", modelName, err)
			}
			audioCompletionRatioText, err := formatRelayPricingDecimal(audioCompletionRatio)
			if err != nil {
				return nil, fmt.Errorf("audio_completion_ratio[%s]: %w", modelName, err)
			}
			model.ChargeMode = "token_ratio"
			model.CostEstimationStatus = "supported"
			model.ModelRatio = common.GetPointer(modelRatioText)
			model.CompletionRatio = common.GetPointer(completionRatioText)
			model.CacheRatio = common.GetPointer(cacheRatioText)
			model.CreateCache5mRatio = common.GetPointer(createCache5mRatioText)
			model.CreateCache1hRatio = common.GetPointer(createCache1hRatioText)
			model.ImageRatio = common.GetPointer(imageRatioText)
			model.AudioRatio = common.GetPointer(audioRatioText)
			model.AudioCompletionRatio = common.GetPointer(audioCompletionRatioText)
		default:
			model.ChargeMode = "unresolved"
			model.CostEstimationStatus = "unsupported_billing_mode"
		}

		models[modelName] = model
	}
	return models, nil
}

func buildRelayPricingInputs(source relayPricingSource) (dto.RelayPricingInputs, error) {
	modelRatio, err := formatRelayPricingDecimalMap("model_ratio", source.modelRatio)
	if err != nil {
		return dto.RelayPricingInputs{}, err
	}
	completionRatio, err := formatRelayPricingDecimalMap("completion_ratio", source.completionRatio)
	if err != nil {
		return dto.RelayPricingInputs{}, err
	}
	cacheRatio, err := formatRelayPricingDecimalMap("cache_ratio", source.cacheRatio)
	if err != nil {
		return dto.RelayPricingInputs{}, err
	}
	createCacheRatio, err := formatRelayPricingDecimalMap("create_cache_ratio", source.createCacheRatio)
	if err != nil {
		return dto.RelayPricingInputs{}, err
	}
	createCache5mRatio, err := formatRelayPricingDecimalMap("create_cache_5m_ratio", source.createCache5mRatio)
	if err != nil {
		return dto.RelayPricingInputs{}, err
	}
	createCache1hRatio, err := formatRelayPricingDecimalMap("create_cache_1h_ratio", source.createCache1hRatio)
	if err != nil {
		return dto.RelayPricingInputs{}, err
	}
	imageRatio, err := formatRelayPricingDecimalMap("image_ratio", source.imageRatio)
	if err != nil {
		return dto.RelayPricingInputs{}, err
	}
	audioRatio, err := formatRelayPricingDecimalMap("audio_ratio", source.audioRatio)
	if err != nil {
		return dto.RelayPricingInputs{}, err
	}
	audioCompletionRatio, err := formatRelayPricingDecimalMap("audio_completion_ratio", source.audioCompletionRatio)
	if err != nil {
		return dto.RelayPricingInputs{}, err
	}
	modelPrice, err := formatRelayPricingDecimalMap("model_price", source.modelPrice)
	if err != nil {
		return dto.RelayPricingInputs{}, err
	}
	return dto.RelayPricingInputs{
		ModelRatio:           modelRatio,
		CompletionRatio:      completionRatio,
		CacheRatio:           cacheRatio,
		CreateCacheRatio:     createCacheRatio,
		CreateCache5mRatio:   createCache5mRatio,
		CreateCache1hRatio:   createCache1hRatio,
		ImageRatio:           imageRatio,
		AudioRatio:           audioRatio,
		AudioCompletionRatio: audioCompletionRatio,
		ModelPrice:           modelPrice,
		BillingMode:          source.billingMode,
		BillingExpr:          source.billingExpr,
	}, nil
}

func formatRelayPricingDecimalMap(name string, values map[string]float64) (map[string]string, error) {
	formatted := make(map[string]string, len(values))
	for key, value := range values {
		text, err := formatRelayPricingDecimal(value)
		if err != nil {
			return nil, fmt.Errorf("%s[%s]: %w", name, key, err)
		}
		formatted[key] = text
	}
	return formatted, nil
}

func formatRelayPricingDecimal(value float64) (string, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return "", fmt.Errorf("invalid decimal value %v", value)
	}
	return strconv.FormatFloat(value, 'f', -1, 64), nil
}

func aetherETagMatches(rawHeader string, etag string) bool {
	for _, candidate := range strings.Split(rawHeader, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == etag || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}
