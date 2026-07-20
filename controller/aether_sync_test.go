package controller

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetAetherEventsReturnsOnlyBoundInstanceEvents(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_sync_events_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.AetherLedgerEvent{}))
	require.NoError(t, db.Create(&model.AetherLedgerEvent{InstanceID: "aether-primary", EventType: model.AetherLedgerEventUsageSettled, DedupeKey: "primary-1", OccurredAt: 10, Payload: `{}`, CreatedTime: 10}).Error)
	require.NoError(t, db.Create(&model.AetherLedgerEvent{InstanceID: "aether-other", EventType: model.AetherLedgerEventUsageSettled, DedupeKey: "other-1", OccurredAt: 11, Payload: `{}`, CreatedTime: 11}).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/aether/v1/events?after=0&limit=10", nil)
	ctx.Set("aether_instance_id", "aether-primary")

	GetAetherEvents(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			ContractVersion string `json:"contract_version"`
			Events          []struct {
				Id           string         `json:"id"`
				InstanceID   string         `json:"instance_id"`
				Payload      map[string]any `json:"payload"`
				OccurredAt   int64          `json:"occurred_at"`
				QuotaPerUnit string         `json:"quota_per_unit"`
			} `json:"events"`
			NextCursor string `json:"next_cursor"`
			HasMore    bool   `json:"has_more"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.Equal(t, "aether-newapi-events/v1", response.Data.ContractVersion)
	require.Len(t, response.Data.Events, 1)
	assert.Equal(t, "aether-primary", response.Data.Events[0].InstanceID)
	assert.Equal(t, "1", response.Data.Events[0].Id)
	assert.NotNil(t, response.Data.Events[0].Payload)
	assert.Equal(t, int64(10), response.Data.Events[0].OccurredAt)
	assert.NotEmpty(t, response.Data.Events[0].QuotaPerUnit)
	assert.Equal(t, response.Data.Events[0].Id, response.Data.NextCursor)
	assert.False(t, response.Data.HasMore)
}

func TestGetAetherSnapshotAggregatesFinancialAndSubscriptionEvents(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_sync_snapshot_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.AetherLedgerEvent{}, &model.Channel{}))
	now := time.Now().Unix()
	require.NoError(t, db.Create([]model.AetherLedgerEvent{
		{InstanceID: "aether-primary", EventType: model.AetherLedgerEventUsageSettled, DedupeKey: "snapshot-usage", OccurredAt: now, Payload: `{"charged_quota":"100","prompt_tokens":"20","completion_tokens":"5"}`, CreatedTime: now},
		{InstanceID: "aether-primary", EventType: model.AetherLedgerEventFinancial, DedupeKey: "snapshot-topup", OccurredAt: now, Payload: `{"quota_delta":"200","money_amount":"2.50","source_type":"topup"}`, CreatedTime: now},
		{InstanceID: "aether-primary", EventType: model.AetherLedgerEventFinancial, DedupeKey: "snapshot-subscription", OccurredAt: now, Payload: `{"quota_delta":"-500000","money_amount":"1.25","source_type":"subscription_balance_purchase"}`, CreatedTime: now},
		{InstanceID: "aether-primary", EventType: model.AetherLedgerEventSubscriptionChanged, DedupeKey: "snapshot-activation", OccurredAt: now, Payload: `{"action":"activated"}`, CreatedTime: now},
	}).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/aether/v1/snapshot?from="+strconv.FormatInt(now-1, 10)+"&to="+strconv.FormatInt(now+1, 10), nil)
	ctx.Set("aether_instance_id", "aether-primary")

	GetAetherSnapshot(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Usage struct {
				Requests     int64  `json:"requests"`
				ChargedQuota string `json:"charged_quota"`
			} `json:"usage"`
			Financial struct {
				Events      int64  `json:"events"`
				QuotaDelta  string `json:"quota_delta"`
				MoneyAmount string `json:"money_amount"`
			} `json:"financial"`
			Subscriptions struct {
				Changes   int64 `json:"changes"`
				Activated int64 `json:"activated"`
			} `json:"subscriptions"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.Equal(t, int64(1), response.Data.Usage.Requests)
	assert.Equal(t, "100", response.Data.Usage.ChargedQuota)
	assert.Equal(t, int64(2), response.Data.Financial.Events)
	assert.Equal(t, "-499800", response.Data.Financial.QuotaDelta)
	assert.Equal(t, "3.75", response.Data.Financial.MoneyAmount)
	assert.Equal(t, int64(1), response.Data.Subscriptions.Changes)
	assert.Equal(t, int64(1), response.Data.Subscriptions.Activated)
}

func TestGetAetherSnapshotPaginatesEventsAndUsesDecimalStrings(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_sync_snapshot_page_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.AetherLedgerEvent{}, &model.Channel{}))
	now := time.Now().Unix()
	require.NoError(t, db.Create([]model.AetherLedgerEvent{
		{InstanceID: "aether-primary", EventType: model.AetherLedgerEventUsageSettled, DedupeKey: "snapshot-page-1", OccurredAt: now, Payload: `{"charged_quota":"100","prompt_tokens":"20","completion_tokens":"5"}`, QuotaPerUnitSnapshot: 678_901.25, CreatedTime: now},
		{InstanceID: "aether-primary", EventType: model.AetherLedgerEventFinancial, DedupeKey: "snapshot-page-2", OccurredAt: now, Payload: `{"quota_delta":"200","money_amount":"2.50","source_type":"topup"}`, QuotaPerUnitSnapshot: 0, CreatedTime: now},
	}).Error)
	require.NoError(t, db.Create(&model.Channel{Name: "temporary-upstream", Type: 1, Status: common.ChannelStatusEnabled, Balance: 12.5, UsedQuota: 99}).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/aether/v1/snapshot?from="+strconv.FormatInt(now-1, 10)+"&to="+strconv.FormatInt(now+1, 10)+"&cursor=0&limit=1", nil)
	ctx.Set("aether_instance_id", "aether-primary")

	GetAetherSnapshot(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			ContractVersion  string `json:"contract_version"`
			SnapshotRevision int64  `json:"snapshot_revision"`
			Events           []struct {
				Id           string         `json:"id"`
				Payload      map[string]any `json:"payload"`
				QuotaPerUnit string         `json:"quota_per_unit"`
			} `json:"events"`
			NextCursor   string `json:"next_cursor"`
			HasMore      bool   `json:"has_more"`
			QuotaPerUnit string `json:"quota_per_unit"`
			Financial    struct {
				Events      int64  `json:"events"`
				QuotaDelta  string `json:"quota_delta"`
				MoneyAmount string `json:"money_amount"`
			} `json:"financial"`
			HistoricalAnalysisState string `json:"historical_analysis_state"`
			Channels     []struct {
				Id        string `json:"id"`
				Balance   string `json:"balance"`
				UsedQuota string `json:"used_quota"`
			} `json:"channels"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.Equal(t, "aether-newapi-snapshot/v1", response.Data.ContractVersion)
	assert.GreaterOrEqual(t, response.Data.SnapshotRevision, int64(0))
	require.Len(t, response.Data.Events, 1)
	assert.NotEmpty(t, response.Data.Events[0].Id)
	assert.NotNil(t, response.Data.Events[0].Payload)
	assert.Equal(t, "678901.25", response.Data.Events[0].QuotaPerUnit)
	assert.Equal(t, response.Data.Events[0].Id, response.Data.NextCursor)
	assert.True(t, response.Data.HasMore)
	assert.NotEmpty(t, response.Data.QuotaPerUnit)
	assert.Equal(t, int64(1), response.Data.Financial.Events)
	assert.Equal(t, "200", response.Data.Financial.QuotaDelta)
	assert.Equal(t, "2.5", response.Data.Financial.MoneyAmount)
	assert.Equal(t, "unknown", response.Data.HistoricalAnalysisState)
	require.Len(t, response.Data.Channels, 1)
	assert.NotEmpty(t, response.Data.Channels[0].Id)
	assert.Equal(t, "12.5", response.Data.Channels[0].Balance)
	assert.Equal(t, "99", response.Data.Channels[0].UsedQuota)
}

func TestGetAetherSnapshotETagChangesWhenWindowTailChanges(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_sync_snapshot_window_etag_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.AetherLedgerEvent{}, &model.Channel{}))
	now := time.Now().Unix()
	require.NoError(t, db.Create([]model.AetherLedgerEvent{
		{InstanceID: "aether-primary", EventType: model.AetherLedgerEventUsageSettled, DedupeKey: "snapshot-window-etag-1", OccurredAt: now, Payload: `{"charged_quota":"100","prompt_tokens":"20","completion_tokens":"5"}`, QuotaPerUnitSnapshot: 678_901.25, CreatedTime: now},
		{InstanceID: "aether-primary", EventType: model.AetherLedgerEventFinancial, DedupeKey: "snapshot-window-etag-2", OccurredAt: now, Payload: `{"quota_delta":"200","money_amount":"2.50"}`, QuotaPerUnitSnapshot: 678_901.25, CreatedTime: now},
	}).Error)
	rawURL := "/api/aether/v1/snapshot?from=" + strconv.FormatInt(now-1, 10) + "&to=" + strconv.FormatInt(now+1, 10) + "&cursor=0&limit=1"

	firstRecorder := httptest.NewRecorder()
	firstContext, _ := gin.CreateTestContext(firstRecorder)
	firstContext.Request = httptest.NewRequest(http.MethodGet, rawURL, nil)
	firstContext.Set("aether_instance_id", "aether-primary")
	GetAetherSnapshot(firstContext)
	require.Equal(t, http.StatusOK, firstRecorder.Code)
	firstETag := firstRecorder.Header().Get("ETag")
	require.NotEmpty(t, firstETag)

	require.NoError(t, db.Create(&model.AetherLedgerEvent{
		InstanceID: "aether-primary", EventType: model.AetherLedgerEventPricingChanged, DedupeKey: "snapshot-window-etag-3", OccurredAt: now,
		Payload: `{"scope":"ModelRatio"}`, QuotaPerUnitSnapshot: 678_901.25, CreatedTime: now,
	}).Error)

	secondRecorder := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondRecorder)
	secondContext.Request = httptest.NewRequest(http.MethodGet, rawURL, nil)
	secondContext.Request.Header.Set("If-None-Match", firstETag)
	secondContext.Set("aether_instance_id", "aether-primary")
	GetAetherSnapshot(secondContext)

	assert.Equal(t, http.StatusOK, secondRecorder.Code)
	assert.NotEqual(t, firstETag, secondRecorder.Header().Get("ETag"))
}

func TestGetAetherSnapshotReturnsNotModifiedForMatchingETag(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_sync_snapshot_etag_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.AetherLedgerEvent{}, &model.Channel{}))
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.AetherLedgerEvent{
		InstanceID: "aether-primary", EventType: model.AetherLedgerEventUsageSettled, DedupeKey: "snapshot-etag-1", OccurredAt: now,
		Payload: `{"charged_quota":"100","prompt_tokens":"20","completion_tokens":"5"}`, QuotaPerUnitSnapshot: 678_901.25, CreatedTime: now,
	}).Error)
	rawURL := "/api/aether/v1/snapshot?from=" + strconv.FormatInt(now-1, 10) + "&to=" + strconv.FormatInt(now+1, 10)

	firstRecorder := httptest.NewRecorder()
	firstContext, _ := gin.CreateTestContext(firstRecorder)
	firstContext.Request = httptest.NewRequest(http.MethodGet, rawURL, nil)
	firstContext.Set("aether_instance_id", "aether-primary")
	GetAetherSnapshot(firstContext)
	require.Equal(t, http.StatusOK, firstRecorder.Code)
	etag := firstRecorder.Header().Get("ETag")
	require.NotEmpty(t, etag)

	secondRecorder := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondRecorder)
	secondContext.Request = httptest.NewRequest(http.MethodGet, rawURL, nil)
	secondContext.Request.Header.Set("If-None-Match", etag)
	secondContext.Set("aether_instance_id", "aether-primary")
	GetAetherSnapshot(secondContext)

	assert.Equal(t, etag, secondRecorder.Header().Get("ETag"))
	assert.Equal(t, http.StatusNotModified, secondRecorder.Code)
	assert.Empty(t, secondRecorder.Body.String())
}

func TestGetAetherSnapshotMarksMissingHistoricalPricingUnknown(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_sync_snapshot_unknown_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.AetherLedgerEvent{}, &model.Channel{}))
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.AetherLedgerEvent{
		InstanceID: "aether-primary", EventType: model.AetherLedgerEventUsageSettled, DedupeKey: "snapshot-unknown-1", OccurredAt: now,
		Payload: `{"charged_quota":"100","prompt_tokens":"20","completion_tokens":"5"}`, QuotaPerUnitSnapshot: 0, CreatedTime: now,
	}).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/aether/v1/snapshot?from="+strconv.FormatInt(now-1, 10)+"&to="+strconv.FormatInt(now+1, 10), nil)
	ctx.Set("aether_instance_id", "aether-primary")
	GetAetherSnapshot(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data struct {
			HistoricalAnalysisState string `json:"historical_analysis_state"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "unknown", response.Data.HistoricalAnalysisState)
}

func TestGetAetherSnapshotKeepsHistoricalPricingUnknownWhenLaterEventsAreKnown(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_sync_snapshot_unknown_then_known_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.AetherLedgerEvent{}, &model.Channel{}))
	now := time.Now().Unix()
	require.NoError(t, db.Create([]model.AetherLedgerEvent{
		{
			InstanceID: "aether-primary", EventType: model.AetherLedgerEventUsageSettled, DedupeKey: "snapshot-unknown-then-known-1", OccurredAt: now,
			Payload: `{"charged_quota":"100","prompt_tokens":"20","completion_tokens":"5"}`, QuotaPerUnitSnapshot: 0, CreatedTime: now,
		},
		{
			InstanceID: "aether-primary", EventType: model.AetherLedgerEventUsageSettled, DedupeKey: "snapshot-unknown-then-known-2", OccurredAt: now,
			Payload: `{"charged_quota":"200","prompt_tokens":"40","completion_tokens":"10"}`, QuotaPerUnitSnapshot: 678_901.25, CreatedTime: now,
		},
	}).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/aether/v1/snapshot?from="+strconv.FormatInt(now-1, 10)+"&to="+strconv.FormatInt(now+1, 10), nil)
	ctx.Set("aether_instance_id", "aether-primary")
	GetAetherSnapshot(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data struct {
			HistoricalAnalysisState string `json:"historical_analysis_state"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "unknown", response.Data.HistoricalAnalysisState)
}

func TestGetAetherSnapshotRejectsRangesLongerThanThirtyOneDays(t *testing.T) {
	now := time.Now().Unix()
	_, _, err := parseAetherSnapshotRange(strconv.FormatInt(now-32*24*60*60, 10), strconv.FormatInt(now, 10))
	require.Error(t, err)
}

func TestGetAetherSnapshotOrdersChannelListByID(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_sync_snapshot_channel_order_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.AetherLedgerEvent{}, &model.Channel{}))
	require.NoError(t, db.Create([]model.Channel{
		{Id: 18, Name: "first-channel", Type: 1, Status: common.ChannelStatusEnabled},
		{Id: 41, Name: "second-channel", Type: 1, Status: common.ChannelStatusEnabled},
	}).Error)
	// SQLite reverses unordered SELECTs here, making the response ordering contract observable.
	require.NoError(t, db.Exec("PRAGMA reverse_unordered_selects = ON").Error)
	t.Cleanup(func() {
		require.NoError(t, db.Exec("PRAGMA reverse_unordered_selects = OFF").Error)
	})

	now := time.Now().Unix()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/aether/v1/snapshot?from="+strconv.FormatInt(now-1, 10)+"&to="+strconv.FormatInt(now+1, 10), nil)
	ctx.Set("aether_instance_id", "aether-primary")

	GetAetherSnapshot(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data struct {
			Channels []struct {
				Id string `json:"id"`
			} `json:"channels"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.Len(t, response.Data.Channels, 2)
	assert.Equal(t, []string{"18", "41"}, []string{response.Data.Channels[0].Id, response.Data.Channels[1].Id})
}
