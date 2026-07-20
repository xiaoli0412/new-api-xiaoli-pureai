package service

import (
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var billingRequestStateTestSequence uint64

func openBillingRequestStateDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousRedisEnabled := common.RedisEnabled
	previousBatchUpdateEnabled := common.BatchUpdateEnabled

	dsn := fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", name, atomic.AddUint64(&billingRequestStateTestSequence, 1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	t.Cleanup(func() {
		common.BatchUpdateEnabled = previousBatchUpdateEnabled
		common.RedisEnabled = previousRedisEnabled
		model.DB = previousDB
	})

	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.Token{},
		&model.UserSubscription{},
		&model.SubscriptionPreConsumeRecord{},
		&model.AetherIntegration{},
		&model.AetherLedgerEvent{},
		&model.BillingRefundClaim{},
		&model.BillingRequestState{},
	))
	return db
}

func newWalletBillingRelayInfo(userID int, tokenID int, tokenKey string, requestID string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         tokenID,
		TokenKey:        tokenKey,
		RequestId:       requestID,
		ForcePreConsume: true,
		UserSetting: dto.UserSetting{
			BillingPreference: "wallet_only",
		},
	}
}

func TestBillingSessionPreConsumeDeduplicatesWalletAndTokenByRequest(t *testing.T) {
	db := openBillingRequestStateDB(t, "billing_request_state_wallet_duplicate")
	require.NoError(t, db.Create(&model.User{Id: 6101, Username: "billing-state-wallet", Quota: 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6101, UserId: 6101, Key: "billing-state-wallet-token", RemainQuota: 1000}).Error)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	first, firstErr := NewBillingSession(context, newWalletBillingRelayInfo(6101, 6101, "billing-state-wallet-token", "req_billing_state_wallet"), 100)
	second, secondErr := NewBillingSession(context, newWalletBillingRelayInfo(6101, 6101, "billing-state-wallet-token", "req_billing_state_wallet"), 100)

	require.Nil(t, firstErr)
	require.Nil(t, secondErr)
	require.NotNil(t, first)
	require.NotNil(t, second)

	var user model.User
	var token model.Token
	require.NoError(t, db.First(&user, 6101).Error)
	require.NoError(t, db.First(&token, 6101).Error)
	assert.Equal(t, 900, user.Quota)
	assert.Equal(t, 900, token.RemainQuota)
	assert.Equal(t, 100, token.UsedQuota)
}

func TestBillingSessionReservePersistsWalletAndTokenTogether(t *testing.T) {
	db := openBillingRequestStateDB(t, "billing_request_state_wallet_reserve")
	common.BatchUpdateEnabled = true
	require.NoError(t, db.Create(&model.User{Id: 6102, Username: "billing-state-reserve", Quota: 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6102, UserId: 6102, Key: "billing-state-reserve-token", RemainQuota: 1000}).Error)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(context, newWalletBillingRelayInfo(6102, 6102, "billing-state-reserve-token", "req_billing_state_reserve"), 100)
	require.Nil(t, apiErr)
	require.NoError(t, session.Reserve(150))

	var user model.User
	var token model.Token
	var state model.BillingRequestState
	require.NoError(t, db.First(&user, 6102).Error)
	require.NoError(t, db.First(&token, 6102).Error)
	require.NoError(t, db.Where("user_id = ? AND token_id = ?", 6102, 6102).First(&state).Error)
	assert.Equal(t, 850, user.Quota)
	assert.Equal(t, 850, token.RemainQuota)
	assert.Equal(t, 150, state.PreConsumedQuota)
	assert.Equal(t, 150, state.FundingConsumedQuota)
	assert.Equal(t, 150, state.TokenConsumedQuota)
	assert.Equal(t, 50, state.ExtraReservedQuota)
	assert.Equal(t, model.BillingRequestStatePreconsumed, state.State)
}

func TestBillingSessionSettlePersistsFinalWalletAndTokenAmounts(t *testing.T) {
	db := openBillingRequestStateDB(t, "billing_request_state_wallet_settle")
	common.BatchUpdateEnabled = true
	require.NoError(t, db.Create(&model.User{Id: 6103, Username: "billing-state-settle", Quota: 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6103, UserId: 6103, Key: "billing-state-settle-token", RemainQuota: 1000}).Error)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(context, newWalletBillingRelayInfo(6103, 6103, "billing-state-settle-token", "req_billing_state_settle"), 100)
	require.Nil(t, apiErr)
	require.NoError(t, session.Settle(60))

	var user model.User
	var token model.Token
	var state model.BillingRequestState
	require.NoError(t, db.First(&user, 6103).Error)
	require.NoError(t, db.First(&token, 6103).Error)
	require.NoError(t, db.Where("user_id = ? AND token_id = ?", 6103, 6103).First(&state).Error)
	assert.Equal(t, 940, user.Quota)
	assert.Equal(t, 940, token.RemainQuota)
	assert.Equal(t, 60, state.FundingConsumedQuota)
	assert.Equal(t, 60, state.TokenConsumedQuota)
	assert.Equal(t, 60, state.SettledQuota)
	assert.Equal(t, model.BillingRequestStateSettled, state.State)
	assert.False(t, session.NeedsRefund())
}

func TestBillingSessionSettleRollsBackWhenUsageOutboxWriteFails(t *testing.T) {
	db := openBillingRequestStateDB(t, "billing_request_state_settle_usage_outbox_failure")
	integration := &model.AetherIntegration{
		ChannelID:                   6113,
		InstanceID:                  "billing-state-settle-usage-outbox-failure",
		ExecutionMode:               model.AetherExecutionModeDirectChannel,
		Enabled:                     true,
		ConfigRevision:              1,
		ControlSecretEncrypted:      "invalid-secret",
		RelaySigningSecretEncrypted: "invalid-secret",
	}
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&model.User{Id: 6113, Username: "billing-state-settle-usage-outbox-failure", Quota: 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6113, UserId: 6113, Key: "billing-state-settle-usage-outbox-failure-token", RemainQuota: 1000}).Error)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(context, newWalletBillingRelayInfo(6113, 6113, "billing-state-settle-usage-outbox-failure-token", "req_billing_state_settle_usage_outbox_failure"), 100)
	require.Nil(t, apiErr)
	require.Error(t, session.Settle(60))

	var user model.User
	var token model.Token
	var state model.BillingRequestState
	var eventCount int64
	require.NoError(t, db.First(&user, 6113).Error)
	require.NoError(t, db.First(&token, 6113).Error)
	require.NoError(t, db.Where("user_id = ? AND token_id = ?", 6113, 6113).First(&state).Error)
	require.NoError(t, db.Model(&model.AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Equal(t, 900, user.Quota)
	assert.Equal(t, 900, token.RemainQuota)
	assert.Equal(t, model.BillingRequestStatePreconsumed, state.State)
	assert.Zero(t, eventCount)

	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Save(integration).Error)
	require.NoError(t, session.Settle(60))
	require.NoError(t, session.Settle(60))
	require.NoError(t, db.First(&user, 6113).Error)
	require.NoError(t, db.First(&token, 6113).Error)
	require.NoError(t, db.Where("user_id = ? AND token_id = ?", 6113, 6113).First(&state).Error)
	require.NoError(t, db.Model(&model.AetherLedgerEvent{}).Where("event_type = ?", model.AetherLedgerEventUsageSettled).Count(&eventCount).Error)
	assert.Equal(t, 940, user.Quota)
	assert.Equal(t, 940, token.RemainQuota)
	assert.Equal(t, model.BillingRequestStateSettled, state.State)
	assert.Equal(t, int64(1), eventCount)
}

func TestBillingSessionSettleRejectsConflictingFinalQuotaForSameRequest(t *testing.T) {
	db := openBillingRequestStateDB(t, "billing_request_state_settle_conflict")
	require.NoError(t, db.Create(&model.User{Id: 6112, Username: "billing-state-settle-conflict", Quota: 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6112, UserId: 6112, Key: "billing-state-settle-conflict-token", RemainQuota: 1000}).Error)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(context, newWalletBillingRelayInfo(6112, 6112, "billing-state-settle-conflict-token", "req_billing_state_settle_conflict"), 100)
	require.Nil(t, apiErr)
	require.NoError(t, session.Settle(60))
	require.Error(t, session.Settle(80))

	var user model.User
	var token model.Token
	var state model.BillingRequestState
	require.NoError(t, db.First(&user, 6112).Error)
	require.NoError(t, db.First(&token, 6112).Error)
	require.NoError(t, db.Where("user_id = ? AND token_id = ?", 6112, 6112).First(&state).Error)
	assert.Equal(t, 940, user.Quota)
	assert.Equal(t, 940, token.RemainQuota)
	assert.Equal(t, 60, state.SettledQuota)
}

func TestBillingSessionRefundTransitionsStateAndPostsFinancialEventOnce(t *testing.T) {
	db := openBillingRequestStateDB(t, "billing_request_state_wallet_refund")
	integration := &model.AetherIntegration{ChannelID: 6104, InstanceID: "billing-state-refund", ExecutionMode: model.AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&model.User{Id: 6104, Username: "billing-state-refund", Quota: 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6104, UserId: 6104, Key: "billing-state-refund-token", RemainQuota: 1000}).Error)

	requestID := "req_billing_state_refund"
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(context, newWalletBillingRelayInfo(6104, 6104, "billing-state-refund-token", requestID), 100)
	require.Nil(t, apiErr)
	require.NoError(t, session.refundFinancial())
	require.NoError(t, session.refundFinancial())

	var user model.User
	var token model.Token
	var state model.BillingRequestState
	var events []model.AetherLedgerEvent
	require.NoError(t, db.First(&user, 6104).Error)
	require.NoError(t, db.First(&token, 6104).Error)
	require.NoError(t, db.Where("user_id = ? AND token_id = ?", 6104, 6104).First(&state).Error)
	require.NoError(t, db.Where("event_type = ?", model.AetherLedgerEventFinancial).Find(&events).Error)
	assert.Equal(t, 1000, user.Quota)
	assert.Equal(t, 1000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.Equal(t, model.BillingRequestStateRefunded, state.State)
	assert.Len(t, events, 1)
	assert.NotContains(t, events[0].DedupeKey, requestID)
}

func TestBillingSessionRefundRollsBackStateWhenFinancialEventFailsThenRetries(t *testing.T) {
	db := openBillingRequestStateDB(t, "billing_request_state_refund_retry")
	integration := &model.AetherIntegration{
		ChannelID:                   6105,
		InstanceID:                  "billing-state-refund-retry",
		ExecutionMode:               model.AetherExecutionModeDirectChannel,
		Enabled:                     true,
		ConfigRevision:              1,
		ControlSecretEncrypted:      "invalid-secret",
		RelaySigningSecretEncrypted: "invalid-secret",
	}
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&model.User{Id: 6105, Username: "billing-state-retry", Quota: 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6105, UserId: 6105, Key: "billing-state-retry-token", RemainQuota: 1000}).Error)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(context, newWalletBillingRelayInfo(6105, 6105, "billing-state-retry-token", "req_billing_state_retry"), 100)
	require.Nil(t, apiErr)
	require.Error(t, session.refundFinancial())

	var user model.User
	var token model.Token
	var state model.BillingRequestState
	var eventCount int64
	require.NoError(t, db.First(&user, 6105).Error)
	require.NoError(t, db.First(&token, 6105).Error)
	require.NoError(t, db.Where("user_id = ? AND token_id = ?", 6105, 6105).First(&state).Error)
	require.NoError(t, db.Model(&model.AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Equal(t, 900, user.Quota)
	assert.Equal(t, 900, token.RemainQuota)
	assert.Equal(t, model.BillingRequestStatePreconsumed, state.State)
	assert.Zero(t, eventCount)

	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Save(integration).Error)
	require.NoError(t, session.refundFinancial())
	require.NoError(t, db.First(&user, 6105).Error)
	require.NoError(t, db.First(&token, 6105).Error)
	require.NoError(t, db.Where("user_id = ? AND token_id = ?", 6105, 6105).First(&state).Error)
	require.NoError(t, db.Model(&model.AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Equal(t, 1000, user.Quota)
	assert.Equal(t, 1000, token.RemainQuota)
	assert.Equal(t, model.BillingRequestStateRefunded, state.State)
	assert.Equal(t, int64(1), eventCount)
}

func TestBillingSessionUsesDirectStateTransactionsWhenBatchUpdatesAreEnabled(t *testing.T) {
	db := openBillingRequestStateDB(t, "billing_request_state_batch_max_quota")
	common.BatchUpdateEnabled = true
	integration := &model.AetherIntegration{ChannelID: 6106, InstanceID: "billing-state-batch", ExecutionMode: model.AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&model.User{Id: 6106, Username: "billing-state-batch", Quota: common.MaxQuota}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6106, UserId: 6106, Key: "billing-state-batch-token", RemainQuota: 1000}).Error)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(context, newWalletBillingRelayInfo(6106, 6106, "billing-state-batch-token", "req_billing_state_batch"), 100)
	require.Nil(t, apiErr)
	require.NoError(t, session.refundFinancial())

	var user model.User
	var token model.Token
	var state model.BillingRequestState
	require.NoError(t, db.First(&user, 6106).Error)
	require.NoError(t, db.First(&token, 6106).Error)
	require.NoError(t, db.Where("user_id = ? AND token_id = ?", 6106, 6106).First(&state).Error)
	assert.Equal(t, common.MaxQuota, user.Quota)
	assert.Equal(t, 1000, token.RemainQuota)
	assert.Equal(t, model.BillingRequestStateRefunded, state.State)
}

func TestBillingSessionSubscriptionReserveAndRefundKeepsTokenAndSubscriptionConsistent(t *testing.T) {
	db := openBillingRequestStateDB(t, "billing_request_state_subscription_reserve_refund")
	require.NoError(t, db.AutoMigrate(&model.SubscriptionPlan{}))
	integration := &model.AetherIntegration{ChannelID: 6107, InstanceID: "billing-state-subscription", ExecutionMode: model.AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&model.User{Id: 6107, Username: "billing-state-subscription"}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6107, UserId: 6107, Key: "billing-state-subscription-token", RemainQuota: 1000}).Error)
	require.NoError(t, db.Create(&model.SubscriptionPlan{Id: 6107, Title: "billing-state-plan", QuotaResetPeriod: model.SubscriptionResetNever}).Error)
	require.NoError(t, db.Create(&model.UserSubscription{Id: 6107, UserId: 6107, PlanId: 6107, AmountTotal: 1000, Status: "active", EndTime: common.GetTimestamp() + 3600}).Error)

	info := newWalletBillingRelayInfo(6107, 6107, "billing-state-subscription-token", "req_billing_state_subscription")
	info.UserSetting.BillingPreference = "subscription_only"
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(context, info, 100)
	require.Nil(t, apiErr)
	require.NoError(t, session.Reserve(150))
	require.NoError(t, session.refundFinancial())

	var subscription model.UserSubscription
	var token model.Token
	var state model.BillingRequestState
	var preConsumeRecord model.SubscriptionPreConsumeRecord
	require.NoError(t, db.First(&subscription, 6107).Error)
	require.NoError(t, db.First(&token, 6107).Error)
	require.NoError(t, db.Where("user_id = ? AND token_id = ?", 6107, 6107).First(&state).Error)
	require.NoError(t, db.Where("user_subscription_id = ?", 6107).First(&preConsumeRecord).Error)
	assert.Zero(t, subscription.AmountUsed)
	assert.Equal(t, 1000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.Equal(t, 50, state.ExtraReservedQuota)
	assert.Equal(t, model.BillingRequestStateRefunded, state.State)
	assert.Equal(t, state.RequestKey, preConsumeRecord.RequestId)
	assert.NotContains(t, preConsumeRecord.RequestId, "req_billing_state_subscription")
}

func TestBillingSessionReserveRollsBackWalletWhenTokenCannotCoverDelta(t *testing.T) {
	db := openBillingRequestStateDB(t, "billing_request_state_reserve_rollback")
	require.NoError(t, db.Create(&model.User{Id: 6108, Username: "billing-state-reserve-rollback", Quota: 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6108, UserId: 6108, Key: "billing-state-reserve-rollback-token", RemainQuota: 100}).Error)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(context, newWalletBillingRelayInfo(6108, 6108, "billing-state-reserve-rollback-token", "req_billing_state_reserve_rollback"), 50)
	require.Nil(t, apiErr)
	require.Error(t, session.Reserve(120))

	var user model.User
	var token model.Token
	var state model.BillingRequestState
	require.NoError(t, db.First(&user, 6108).Error)
	require.NoError(t, db.First(&token, 6108).Error)
	require.NoError(t, db.Where("user_id = ? AND token_id = ?", 6108, 6108).First(&state).Error)
	assert.Equal(t, 950, user.Quota)
	assert.Equal(t, 50, token.RemainQuota)
	assert.Equal(t, 50, state.PreConsumedQuota)
	assert.Equal(t, 50, state.FundingConsumedQuota)
	assert.Equal(t, 50, state.TokenConsumedQuota)
}

func TestBillingSessionPreConsumeRollsBackStateWhenTokenCannotCoverRequest(t *testing.T) {
	db := openBillingRequestStateDB(t, "billing_request_state_preconsume_rollback")
	require.NoError(t, db.Create(&model.User{Id: 6110, Username: "billing-state-preconsume-rollback", Quota: 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6110, UserId: 6110, Key: "billing-state-preconsume-rollback-token", RemainQuota: 50}).Error)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(context, newWalletBillingRelayInfo(6110, 6110, "billing-state-preconsume-rollback-token", "req_billing_state_preconsume_rollback"), 100)
	require.Nil(t, session)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodePreConsumeTokenQuotaFailed, apiErr.GetErrorCode())

	var user model.User
	var token model.Token
	var stateCount int64
	require.NoError(t, db.First(&user, 6110).Error)
	require.NoError(t, db.First(&token, 6110).Error)
	require.NoError(t, db.Model(&model.BillingRequestState{}).Count(&stateCount).Error)
	assert.Equal(t, 1000, user.Quota)
	assert.Equal(t, 50, token.RemainQuota)
	assert.Zero(t, stateCount)
}

func TestBillingSessionRefundTerminatesTrustedZeroPreconsumeState(t *testing.T) {
	db := openBillingRequestStateDB(t, "billing_request_state_trusted_refund")
	trustQuota := common.GetTrustQuota()
	require.NoError(t, db.Create(&model.User{Id: 6111, Username: "billing-state-trusted-refund", Quota: trustQuota + 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6111, UserId: 6111, Key: "billing-state-trusted-refund-token", RemainQuota: 1000}).Error)

	info := newWalletBillingRelayInfo(6111, 6111, "billing-state-trusted-refund-token", "req_billing_state_trusted_refund")
	info.ForcePreConsume = false
	info.TokenUnlimited = true
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(context, info, 100)
	require.Nil(t, apiErr)
	assert.Zero(t, session.GetPreConsumedQuota())

	session.Refund(context)
	require.Eventually(t, func() bool {
		var state model.BillingRequestState
		return db.Where("user_id = ? AND token_id = ?", 6111, 6111).First(&state).Error == nil && state.State == model.BillingRequestStateRefunded
	}, time.Second, 10*time.Millisecond)
}

func TestBillingSessionConcurrentPreConsumeClaimsOneState(t *testing.T) {
	db := openBillingRequestStateDB(t, "billing_request_state_concurrent")
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(2)
	require.NoError(t, db.Create(&model.User{Id: 6109, Username: "billing-state-concurrent", Quota: 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6109, UserId: 6109, Key: "billing-state-concurrent-token", RemainQuota: 1000}).Error)

	start := make(chan struct{})
	errors := make(chan *types.NewAPIError, 2)
	var waitGroup sync.WaitGroup
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			_, apiErr := NewBillingSession(context, newWalletBillingRelayInfo(6109, 6109, "billing-state-concurrent-token", "req_billing_state_concurrent"), 100)
			errors <- apiErr
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errors)
	for apiErr := range errors {
		assert.Nil(t, apiErr)
	}

	var user model.User
	var token model.Token
	var stateCount int64
	require.NoError(t, db.First(&user, 6109).Error)
	require.NoError(t, db.First(&token, 6109).Error)
	require.NoError(t, db.Model(&model.BillingRequestState{}).Where("user_id = ? AND token_id = ?", 6109, 6109).Count(&stateCount).Error)
	assert.Equal(t, 900, user.Quota)
	assert.Equal(t, 900, token.RemainQuota)
	assert.Equal(t, int64(1), stateCount)
}
