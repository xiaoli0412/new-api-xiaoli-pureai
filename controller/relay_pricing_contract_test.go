package controller

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayPricingContractRequiresAdminManagementAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("relay-pricing-contract-test"))))
	route := router.Group("/api/relay/pricing")
	route.Use(middleware.AdminAuth())
	route.GET("/v1", GetRelayPricingContractV1)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/relay/pricing/v1?group=default", nil))

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestRelayPricingContractReturnsSelectedGroupSnapshot(t *testing.T) {
	originalQuotaPerUnit := common.QuotaPerUnit
	ratio_setting.UpdateQuotaPerUnit(678_901.25)
	t.Cleanup(func() {
		ratio_setting.UpdateQuotaPerUnit(originalQuotaPerUnit)
	})

	originalGroups := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"relay-standard":1.25}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalGroups))
	})

	originalModelRatios := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"relay-model":2.5}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(originalModelRatios))
	})

	originalCompletionRatios := ratio_setting.CompletionRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"relay-model":4}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(originalCompletionRatios))
	})

	originalModelPrices := ratio_setting.ModelPrice2JSONString()
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"relay-fixed":0.42}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(originalModelPrices))
	})

	originalCacheRatios := ratio_setting.CacheRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(`{"relay-model":0.1}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(originalCacheRatios))
	})

	originalCreateCacheRatios := ratio_setting.CreateCacheRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateCreateCacheRatioByJSONString(`{"relay-model":1.25}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateCreateCacheRatioByJSONString(originalCreateCacheRatios))
	})

	originalImageRatios := ratio_setting.ImageRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateImageRatioByJSONString(`{"relay-model":1.5}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateImageRatioByJSONString(originalImageRatios))
	})

	originalAudioRatios := ratio_setting.AudioRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"relay-model":0.5}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(originalAudioRatios))
	})

	originalAudioCompletionRatios := ratio_setting.AudioCompletionRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(`{"relay-model":3}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(originalAudioCompletionRatios))
	})

	configuredBilling, ok := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	require.True(t, ok)
	originalBillingMode, originalBillingExpr := configuredBilling.BillingMode, configuredBilling.BillingExpr
	configuredBilling.BillingMode = map[string]string{"relay-tiered": billing_setting.BillingModeTieredExpr}
	configuredBilling.BillingExpr = map[string]string{"relay-tiered": `v1:tier("base", p * 2 + c * 4)`}
	t.Cleanup(func() {
		configuredBilling.BillingMode = originalBillingMode
		configuredBilling.BillingExpr = originalBillingExpr
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/relay/pricing/v1?group=relay-standard", nil)

	GetRelayPricingContractV1(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			ContractVersion string `json:"contract_version"`
			QuotaPerUnit    string `json:"quota_per_unit"`
			UpstreamGroup   string `json:"upstream_group"`
			GroupRatio      string `json:"group_ratio"`
			Pricing         struct {
				ModelRatio           map[string]string `json:"model_ratio"`
				CompletionRatio      map[string]string `json:"completion_ratio"`
				CacheRatio           map[string]string `json:"cache_ratio"`
				CreateCacheRatio     map[string]string `json:"create_cache_ratio"`
				CreateCache5mRatio   map[string]string `json:"create_cache_5m_ratio"`
				CreateCache1hRatio   map[string]string `json:"create_cache_1h_ratio"`
				ImageRatio           map[string]string `json:"image_ratio"`
				AudioRatio           map[string]string `json:"audio_ratio"`
				AudioCompletionRatio map[string]string `json:"audio_completion_ratio"`
				ModelPrice           map[string]string `json:"model_price"`
				BillingMode          map[string]string `json:"billing_mode"`
				BillingExpr          map[string]string `json:"billing_expr"`
			} `json:"pricing"`
			Capabilities map[string]struct {
				Status string `json:"status"`
			} `json:"capabilities"`
			Models map[string]struct {
				BillingMode          string  `json:"billing_mode"`
				ChargeMode           string  `json:"charge_mode"`
				CostEstimationStatus string  `json:"cost_estimation_status"`
				ModelRatio           *string `json:"model_ratio"`
				CompletionRatio      *string `json:"completion_ratio"`
				CacheRatio           *string `json:"cache_ratio"`
				CreateCache5mRatio   *string `json:"create_cache_5m_ratio"`
				CreateCache1hRatio   *string `json:"create_cache_1h_ratio"`
				ImageRatio           *string `json:"image_ratio"`
				AudioRatio           *string `json:"audio_ratio"`
				AudioCompletionRatio *string `json:"audio_completion_ratio"`
				ModelPrice           *string `json:"model_price"`
				BillingExpr          *string `json:"billing_expr"`
				BillingExprVersion   *int    `json:"billing_expr_version"`
			} `json:"models"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))

	assert.True(t, response.Success)
	assert.Equal(t, "v1", response.Data.ContractVersion)
	assert.Equal(t, "678901.25", response.Data.QuotaPerUnit)
	assert.Equal(t, "relay-standard", response.Data.UpstreamGroup)
	assert.Equal(t, "1.25", response.Data.GroupRatio)
	assert.Equal(t, "2.5", response.Data.Pricing.ModelRatio["relay-model"])
	assert.Equal(t, "4", response.Data.Pricing.CompletionRatio["relay-model"])
	assert.Equal(t, "0.1", response.Data.Pricing.CacheRatio["relay-model"])
	assert.Equal(t, "1.25", response.Data.Pricing.CreateCacheRatio["relay-model"])
	assert.Equal(t, "1.25", response.Data.Pricing.CreateCache5mRatio["relay-model"])
	assert.Equal(t, "2", response.Data.Pricing.CreateCache1hRatio["relay-model"])
	assert.Equal(t, "1.5", response.Data.Pricing.ImageRatio["relay-model"])
	assert.Equal(t, "0.5", response.Data.Pricing.AudioRatio["relay-model"])
	assert.Equal(t, "3", response.Data.Pricing.AudioCompletionRatio["relay-model"])
	assert.Equal(t, "0.42", response.Data.Pricing.ModelPrice["relay-fixed"])

	ratioModel := response.Data.Models["relay-model"]
	require.NotNil(t, ratioModel.ModelRatio)
	require.NotNil(t, ratioModel.CompletionRatio)
	require.NotNil(t, ratioModel.CacheRatio)
	require.NotNil(t, ratioModel.CreateCache5mRatio)
	require.NotNil(t, ratioModel.CreateCache1hRatio)
	require.NotNil(t, ratioModel.ImageRatio)
	require.NotNil(t, ratioModel.AudioRatio)
	require.NotNil(t, ratioModel.AudioCompletionRatio)
	assert.Equal(t, "ratio", ratioModel.BillingMode)
	assert.Equal(t, "token_ratio", ratioModel.ChargeMode)
	assert.Equal(t, "supported", ratioModel.CostEstimationStatus)
	assert.Equal(t, "2.5", *ratioModel.ModelRatio)
	assert.Equal(t, "4", *ratioModel.CompletionRatio)
	assert.Equal(t, "0.1", *ratioModel.CacheRatio)
	assert.Equal(t, "1.25", *ratioModel.CreateCache5mRatio)
	assert.Equal(t, "2", *ratioModel.CreateCache1hRatio)
	assert.Equal(t, "1.5", *ratioModel.ImageRatio)
	assert.Equal(t, "0.5", *ratioModel.AudioRatio)
	assert.Equal(t, "3", *ratioModel.AudioCompletionRatio)

	fixedModel := response.Data.Models["relay-fixed"]
	require.NotNil(t, fixedModel.ModelPrice)
	assert.Equal(t, "ratio", fixedModel.BillingMode)
	assert.Equal(t, "fixed_price", fixedModel.ChargeMode)
	assert.Equal(t, "supported", fixedModel.CostEstimationStatus)
	assert.Equal(t, "0.42", *fixedModel.ModelPrice)

	tieredModel := response.Data.Models["relay-tiered"]
	require.NotNil(t, tieredModel.BillingExpr)
	require.NotNil(t, tieredModel.BillingExprVersion)
	assert.Equal(t, "tiered_expr", tieredModel.BillingMode)
	assert.Equal(t, "tiered_expr", tieredModel.ChargeMode)
	assert.Equal(t, "requires_compatible_expression_engine", tieredModel.CostEstimationStatus)
	assert.Equal(t, `v1:tier("base", p * 2 + c * 4)`, *tieredModel.BillingExpr)
	assert.Equal(t, 1, *tieredModel.BillingExprVersion)
	assert.Equal(t, "unsupported", response.Data.Capabilities["tool_calls"].Status)
	assert.Equal(t, "unsupported", response.Data.Capabilities["other_ratios"].Status)
	assert.Equal(t, "requires_compatible_expression_engine", response.Data.Capabilities["tiered_expr"].Status)
	assert.Equal(t, "requires_request_usage", response.Data.Capabilities["image"].Status)
	assert.Equal(t, "requires_endpoint_specific_accounting", response.Data.Capabilities["audio"].Status)
	assert.Equal(t, "caller_supplied_and_validated", response.Data.Capabilities["upstream_group_binding"].Status)
}

func TestRelayPricingContractRejectsInvalidQuotaPerUnit(t *testing.T) {
	originalQuotaPerUnit := common.QuotaPerUnit
	ratio_setting.UpdateQuotaPerUnit(math.Inf(1))
	t.Cleanup(func() {
		ratio_setting.UpdateQuotaPerUnit(originalQuotaPerUnit)
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/relay/pricing/v1?group=default", nil)

	GetRelayPricingContractV1(ctx)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestRelayPricingContractRejectsMissingOrUnknownGroup(t *testing.T) {
	for _, rawURL := range []string{
		"/api/relay/pricing/v1?group=not-configured",
	} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, rawURL, nil)

		GetRelayPricingContractV1(ctx)

		require.Equal(t, http.StatusBadRequest, recorder.Code, rawURL)
	}
}

func TestRelayPricingContractExportsAllGroupsAsDecimalStringsAndETag(t *testing.T) {
	originalQuotaPerUnit := common.QuotaPerUnit
	ratio_setting.UpdateQuotaPerUnit(678_901.25)
	t.Cleanup(func() {
		ratio_setting.UpdateQuotaPerUnit(originalQuotaPerUnit)
	})

	originalGroups := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"relay-standard":1.25,"relay-premium":2}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalGroups))
	})
	originalModelRatios := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"relay-model":2.5}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(originalModelRatios))
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/relay/pricing/v1", nil)

	GetRelayPricingContractV1(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.NotEmpty(t, recorder.Header().Get("ETag"))
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			SnapshotRevision int64             `json:"snapshot_revision"`
			QuotaPerUnit     string            `json:"quota_per_unit"`
			GroupRatios      map[string]string `json:"group_ratios"`
			Pricing          struct {
				ModelRatio map[string]string `json:"model_ratio"`
			} `json:"pricing"`
			Models map[string]struct {
				ModelRatio *string `json:"model_ratio"`
			} `json:"models"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.GreaterOrEqual(t, response.Data.SnapshotRevision, int64(0))
	assert.Equal(t, "678901.25", response.Data.QuotaPerUnit)
	assert.Equal(t, "1.25", response.Data.GroupRatios["relay-standard"])
	assert.Equal(t, "2", response.Data.GroupRatios["relay-premium"])
	assert.Equal(t, "2.5", response.Data.Pricing.ModelRatio["relay-model"])
	require.NotNil(t, response.Data.Models["relay-model"].ModelRatio)
	assert.Equal(t, "2.5", *response.Data.Models["relay-model"].ModelRatio)
}

func TestRelayPricingContractReturnsNotModifiedForMatchingETag(t *testing.T) {
	originalGroups := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"relay-standard":1.25}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalGroups))
	})

	firstRecorder := httptest.NewRecorder()
	firstContext, _ := gin.CreateTestContext(firstRecorder)
	firstContext.Request = httptest.NewRequest(http.MethodGet, "/api/relay/pricing/v1?group=relay-standard", nil)
	GetRelayPricingContractV1(firstContext)
	require.Equal(t, http.StatusOK, firstRecorder.Code)
	etag := firstRecorder.Header().Get("ETag")
	require.NotEmpty(t, etag)

	secondRecorder := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondRecorder)
	secondContext.Request = httptest.NewRequest(http.MethodGet, "/api/relay/pricing/v1?group=relay-standard", nil)
	secondContext.Request.Header.Set("If-None-Match", etag)
	require.Equal(t, etag, secondContext.GetHeader("If-None-Match"))
	GetRelayPricingContractV1(secondContext)

	assert.Equal(t, etag, secondRecorder.Header().Get("ETag"))
	assert.Equal(t, http.StatusNotModified, secondRecorder.Code)
	assert.Empty(t, secondRecorder.Body.String())
}

func TestBuildRelayPricingModelsUsesCapturedSourceOnly(t *testing.T) {
	source := relayPricingSource{
		modelRatio: map[string]float64{
			"captured-ratio-model": 2.5,
		},
		completionRatio: map[string]float64{
			"captured-ratio-model": 4,
		},
		cacheRatio: map[string]float64{
			"captured-ratio-model": 0.2,
		},
		createCacheRatio: map[string]float64{
			"captured-ratio-model": 1.5,
		},
		createCache5mRatio: map[string]float64{
			"captured-ratio-model": 1.5,
		},
		createCache1hRatio: map[string]float64{
			"captured-ratio-model": 2.4,
		},
		imageRatio: map[string]float64{
			"captured-ratio-model": 1.75,
		},
		audioRatio: map[string]float64{
			"captured-ratio-model": 0.5,
		},
		audioCompletionRatio: map[string]float64{
			"captured-ratio-model": 3,
		},
		billingMode: map[string]string{
			"captured-ratio-model":  billing_setting.BillingModeRatio,
			"captured-tiered-model": billing_setting.BillingModeTieredExpr,
		},
		billingExpr: map[string]string{
			"captured-tiered-model": `v1:tier("base", p * 2 + c * 4)`,
		},
	}

	models, err := buildRelayPricingModels(source)
	require.NoError(t, err)

	ratioModel := models["captured-ratio-model"]
	require.NotNil(t, ratioModel.CompletionRatio)
	require.NotNil(t, ratioModel.CacheRatio)
	require.NotNil(t, ratioModel.CreateCache5mRatio)
	require.NotNil(t, ratioModel.CreateCache1hRatio)
	require.NotNil(t, ratioModel.ImageRatio)
	require.NotNil(t, ratioModel.AudioRatio)
	require.NotNil(t, ratioModel.AudioCompletionRatio)
	assert.Equal(t, "4", *ratioModel.CompletionRatio)
	assert.Equal(t, "0.2", *ratioModel.CacheRatio)
	assert.Equal(t, "1.5", *ratioModel.CreateCache5mRatio)
	assert.Equal(t, "2.4", *ratioModel.CreateCache1hRatio)
	assert.Equal(t, "1.75", *ratioModel.ImageRatio)
	assert.Equal(t, "0.5", *ratioModel.AudioRatio)
	assert.Equal(t, "3", *ratioModel.AudioCompletionRatio)

	tieredModel := models["captured-tiered-model"]
	assert.Equal(t, billing_setting.BillingModeTieredExpr, tieredModel.BillingMode)
	assert.Equal(t, "tiered_expr", tieredModel.ChargeMode)
	require.NotNil(t, tieredModel.BillingExpr)
	assert.Equal(t, source.billingExpr["captured-tiered-model"], *tieredModel.BillingExpr)
}
