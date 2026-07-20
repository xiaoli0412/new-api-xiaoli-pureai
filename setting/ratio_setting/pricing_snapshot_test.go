package ratio_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetPricingSnapshotCopiesAllPricingInputs(t *testing.T) {
	originalQuotaPerUnit := GetPricingSnapshot(func() (map[string]string, map[string]string) {
		return nil, nil
	}).QuotaPerUnit
	UpdateQuotaPerUnit(678_901.25)
	t.Cleanup(func() {
		UpdateQuotaPerUnit(originalQuotaPerUnit)
	})

	originalGroups := GroupRatio2JSONString()
	originalModelRatios := ModelRatio2JSONString()
	originalCompletionRatios := CompletionRatio2JSONString()
	originalCacheRatios := CacheRatio2JSONString()
	originalCreateCacheRatios := CreateCacheRatio2JSONString()
	originalImageRatios := ImageRatio2JSONString()
	originalAudioRatios := AudioRatio2JSONString()
	originalAudioCompletionRatios := AudioCompletionRatio2JSONString()
	originalModelPrices := ModelPrice2JSONString()
	t.Cleanup(func() {
		require.NoError(t, UpdateGroupRatioByJSONString(originalGroups))
		require.NoError(t, UpdateModelRatioByJSONString(originalModelRatios))
		require.NoError(t, UpdateCompletionRatioByJSONString(originalCompletionRatios))
		require.NoError(t, UpdateCacheRatioByJSONString(originalCacheRatios))
		require.NoError(t, UpdateCreateCacheRatioByJSONString(originalCreateCacheRatios))
		require.NoError(t, UpdateImageRatioByJSONString(originalImageRatios))
		require.NoError(t, UpdateAudioRatioByJSONString(originalAudioRatios))
		require.NoError(t, UpdateAudioCompletionRatioByJSONString(originalAudioCompletionRatios))
		require.NoError(t, UpdateModelPriceByJSONString(originalModelPrices))
	})

	require.NoError(t, UpdateGroupRatioByJSONString(`{"snapshot-group":1.25}`))
	require.NoError(t, UpdateModelRatioByJSONString(`{"snapshot-model":2.5}`))
	require.NoError(t, UpdateCompletionRatioByJSONString(`{"snapshot-model":4}`))
	require.NoError(t, UpdateCacheRatioByJSONString(`{"snapshot-model":0.2}`))
	require.NoError(t, UpdateCreateCacheRatioByJSONString(`{"snapshot-model":1.5}`))
	require.NoError(t, UpdateImageRatioByJSONString(`{"snapshot-model":1.75}`))
	require.NoError(t, UpdateAudioRatioByJSONString(`{"snapshot-model":0.5}`))
	require.NoError(t, UpdateAudioCompletionRatioByJSONString(`{"snapshot-model":3}`))
	require.NoError(t, UpdateModelPriceByJSONString(`{"snapshot-fixed":0.42}`))

	snapshot := GetPricingSnapshot(func() (map[string]string, map[string]string) {
		return map[string]string{"snapshot-tiered": "tiered_expr"}, map[string]string{"snapshot-tiered": `v1:tier("base", p * 2 + c * 4)`}
	})

	assert.Equal(t, 678_901.25, snapshot.QuotaPerUnit)
	assert.Equal(t, 1.25, snapshot.GroupRatio["snapshot-group"])
	assert.Equal(t, 2.5, snapshot.ModelRatio["snapshot-model"])
	assert.Equal(t, 4.0, snapshot.CompletionRatio["snapshot-model"])
	assert.Equal(t, 0.2, snapshot.CacheRatio["snapshot-model"])
	assert.Equal(t, 1.5, snapshot.CreateCacheRatio["snapshot-model"])
	assert.Equal(t, 1.75, snapshot.ImageRatio["snapshot-model"])
	assert.Equal(t, 0.5, snapshot.AudioRatio["snapshot-model"])
	assert.Equal(t, 3.0, snapshot.AudioCompletionRatio["snapshot-model"])
	assert.Equal(t, 0.42, snapshot.ModelPrice["snapshot-fixed"])
	assert.Equal(t, "tiered_expr", snapshot.BillingMode["snapshot-tiered"])
	assert.Equal(t, `v1:tier("base", p * 2 + c * 4)`, snapshot.BillingExpr["snapshot-tiered"])

	snapshot.ModelRatio["snapshot-model"] = 99
	assert.Equal(t, 2.5, GetModelRatioCopy()["snapshot-model"])
}
