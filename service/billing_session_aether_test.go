package service

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBillingSessionRefundRecordsIdempotentAetherFinancialEvent(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.BillingRefundClaim{}))
	require.NoError(t, model.DB.Where("1 = 1").Delete(&model.AetherLedgerEvent{}).Error)
	require.NoError(t, model.DB.Where("1 = 1").Delete(&model.AetherIntegration{}).Error)
	require.NoError(t, model.DB.Where("1 = 1").Delete(&model.BillingRefundClaim{}).Error)
	require.NoError(t, model.DB.Unscoped().Where("id = ?", 9901).Delete(&model.User{}).Error)
	integration := &model.AetherIntegration{ChannelID: 9901, InstanceID: "aether-refund", ExecutionMode: model.AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, model.DB.Create(integration).Error)
	require.NoError(t, model.DB.Create(&model.User{Id: 9901, Username: "refund-user", Quota: 0}).Error)
	t.Cleanup(func() {
		model.DB.Where("1 = 1").Delete(&model.AetherLedgerEvent{})
		model.DB.Where("1 = 1").Delete(&model.AetherIntegration{})
		model.DB.Where("1 = 1").Delete(&model.BillingRefundClaim{})
		model.DB.Unscoped().Where("id = ?", 9901).Delete(&model.User{})
	})

	session := &BillingSession{
		relayInfo: &relaycommon.RelayInfo{
			UserId:       9901,
			RequestId:    "req_billing_refund_9901",
			IsPlayground: true,
		},
		funding:          &WalletFunding{userId: 9901, consumed: 100},
		preConsumedQuota: 100,
		tokenConsumed:    100,
	}
	context, _ := gin.CreateTestContext(httptest.NewRecorder())

	session.Refund(context)
	session.Refund(context)

	require.Eventually(t, func() bool {
		var count int64
		return model.DB.Model(&model.AetherLedgerEvent{}).
			Where("event_type = ?", model.AetherLedgerEventFinancial).
			Count(&count).Error == nil && count == 1
	}, time.Second, 10*time.Millisecond)
	var event model.AetherLedgerEvent
	require.NoError(t, model.DB.Where("event_type = ?", model.AetherLedgerEventFinancial).First(&event).Error)
	assert.Contains(t, event.Payload, `"source_type":"usage_refund"`)
	assert.Contains(t, event.Payload, `"quota_delta":"100"`)
	assert.NotContains(t, event.Payload, "req_billing_refund_9901")
}

func TestBillingSessionRefundFinancialRollsBackWhenAetherEventFails(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:billing_session_refund_outbox_failure?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.AetherIntegration{}, &model.AetherLedgerEvent{}, &model.BillingRefundClaim{}))
	require.NoError(t, db.Create(&model.User{Id: 9902, Username: "refund-rollback-user", Quota: 0}).Error)
	require.NoError(t, db.Create(&model.AetherIntegration{
		ChannelID:                   9902,
		InstanceID:                  "aether-refund-rollback",
		ExecutionMode:               model.AetherExecutionModeDirectChannel,
		Enabled:                     true,
		ConfigRevision:              1,
		ControlSecretEncrypted:      "invalid-secret",
		RelaySigningSecretEncrypted: "invalid-secret",
	}).Error)

	session := &BillingSession{
		relayInfo: &relaycommon.RelayInfo{
			UserId:    9902,
			RequestId: "req_billing_refund_rollback_9902",
		},
		funding:          &WalletFunding{userId: 9902, consumed: 100},
		preConsumedQuota: 100,
	}

	err = session.refundFinancial()
	require.Error(t, err)

	var user model.User
	require.NoError(t, db.First(&user, 9902).Error)
	assert.Zero(t, user.Quota)
	var eventCount int64
	require.NoError(t, db.Model(&model.AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Zero(t, eventCount)
	var claimCount int64
	require.NoError(t, db.Model(&model.BillingRefundClaim{}).Count(&claimCount).Error)
	assert.Zero(t, claimCount)
}

func TestBillingSessionRefundFinancialCreditsWalletOnlyOncePerRequest(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:billing_session_refund_wallet_idempotency?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.AetherIntegration{}, &model.AetherLedgerEvent{}, &model.BillingRefundClaim{}))
	integration := &model.AetherIntegration{ChannelID: 9903, InstanceID: "aether-refund-wallet-idempotency", ExecutionMode: model.AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&model.User{Id: 9903, Username: "refund-wallet-idempotency", Quota: 0}).Error)

	requestID := "req_billing_refund_wallet_idempotency_9903"
	for range 2 {
		session := &BillingSession{
			relayInfo:        &relaycommon.RelayInfo{UserId: 9903, RequestId: requestID},
			funding:          &WalletFunding{userId: 9903, consumed: 100},
			preConsumedQuota: 100,
		}
		require.NoError(t, session.refundFinancial())
	}

	var user model.User
	require.NoError(t, db.First(&user, 9903).Error)
	assert.Equal(t, 100, user.Quota)
	var eventCount int64
	require.NoError(t, db.Model(&model.AetherLedgerEvent{}).Where("event_type = ?", model.AetherLedgerEventFinancial).Count(&eventCount).Error)
	assert.Equal(t, int64(1), eventCount)
}

func TestBillingSessionRefundFinancialReturnsSubscriptionExtraReserveOnlyOncePerRequest(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:billing_session_refund_subscription_idempotency?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.UserSubscription{},
		&model.SubscriptionPreConsumeRecord{},
		&model.AetherIntegration{},
		&model.AetherLedgerEvent{},
		&model.BillingRefundClaim{},
	))
	integration := &model.AetherIntegration{ChannelID: 9904, InstanceID: "aether-refund-subscription-idempotency", ExecutionMode: model.AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&model.User{Id: 9904, Username: "refund-subscription-idempotency"}).Error)
	require.NoError(t, db.Create(&model.UserSubscription{Id: 9904, UserId: 9904, PlanId: 1, AmountTotal: 1000, AmountUsed: 300, Status: "active"}).Error)

	requestID := "req_billing_refund_subscription_idempotency_9904"
	require.NoError(t, db.Create(&model.SubscriptionPreConsumeRecord{
		RequestId:          requestID,
		UserId:             9904,
		UserSubscriptionId: 9904,
		PreConsumed:        100,
		Status:             "consumed",
	}).Error)
	for range 2 {
		session := &BillingSession{
			relayInfo: &relaycommon.RelayInfo{UserId: 9904, RequestId: requestID, SubscriptionId: 9904},
			funding: &SubscriptionFunding{
				requestId:      requestID,
				userId:         9904,
				subscriptionId: 9904,
				preConsumed:    100,
			},
			preConsumedQuota: 150,
			extraReserved:    50,
		}
		require.NoError(t, session.refundFinancial())
	}

	var subscription model.UserSubscription
	require.NoError(t, db.First(&subscription, 9904).Error)
	assert.Equal(t, int64(150), subscription.AmountUsed)
	var record model.SubscriptionPreConsumeRecord
	require.NoError(t, db.Where("request_id = ?", requestID).First(&record).Error)
	assert.Equal(t, "refunded", record.Status)
	var eventCount int64
	require.NoError(t, db.Model(&model.AetherLedgerEvent{}).Where("event_type = ?", model.AetherLedgerEventFinancial).Count(&eventCount).Error)
	assert.Equal(t, int64(1), eventCount)
}

func TestBillingSessionRefundFinancialDoesNotExposeRequestIDInAetherDedupeKey(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:billing_session_refund_private_dedupe_key?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.AetherIntegration{}, &model.AetherLedgerEvent{}, &model.BillingRefundClaim{}))
	integration := &model.AetherIntegration{ChannelID: 9905, InstanceID: "aether-refund-private-dedupe-key", ExecutionMode: model.AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&model.User{Id: 9905, Username: "refund-private-dedupe-key"}).Error)

	requestID := "req_billing_refund_private_dedupe_key_9905"
	session := &BillingSession{
		relayInfo:        &relaycommon.RelayInfo{UserId: 9905, RequestId: requestID},
		funding:          &WalletFunding{userId: 9905, consumed: 100},
		preConsumedQuota: 100,
	}
	require.NoError(t, session.refundFinancial())

	var event model.AetherLedgerEvent
	require.NoError(t, db.Where("event_type = ?", model.AetherLedgerEventFinancial).First(&event).Error)
	assert.NotContains(t, event.DedupeKey, requestID)
}

func TestBillingSessionRefundFinancialConcurrentWalletRequestsCreditOnlyOnce(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:billing_session_refund_wallet_concurrency?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.AetherIntegration{}, &model.AetherLedgerEvent{}, &model.BillingRefundClaim{}))
	integration := &model.AetherIntegration{ChannelID: 9906, InstanceID: "aether-refund-wallet-concurrency", ExecutionMode: model.AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&model.User{Id: 9906, Username: "refund-wallet-concurrency", Quota: 0}).Error)

	requestID := "req_billing_refund_wallet_concurrency_9906"
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			session := &BillingSession{
				relayInfo:        &relaycommon.RelayInfo{UserId: 9906, RequestId: requestID},
				funding:          &WalletFunding{userId: 9906, consumed: 100},
				preConsumedQuota: 100,
			}
			errs <- session.refundFinancial()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	var user model.User
	require.NoError(t, db.First(&user, 9906).Error)
	assert.Equal(t, 100, user.Quota)
	var eventCount int64
	require.NoError(t, db.Model(&model.AetherLedgerEvent{}).Where("event_type = ?", model.AetherLedgerEventFinancial).Count(&eventCount).Error)
	assert.Equal(t, int64(1), eventCount)
}

func TestBillingSessionRefundCreditsTokenOnlyOnceForDuplicateRequest(t *testing.T) {
	previousDB := model.DB
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	db, err := gorm.Open(sqlite.Open("file:billing_session_refund_token_idempotency?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.AetherIntegration{}, &model.AetherLedgerEvent{}, &model.BillingRefundClaim{}))
	integration := &model.AetherIntegration{ChannelID: 9907, InstanceID: "aether-refund-token-idempotency", ExecutionMode: model.AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&model.User{Id: 9907, Username: "refund-token-idempotency", Quota: 0}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 9907, UserId: 9907, Key: "refund-token-idempotency-key", RemainQuota: 0, UsedQuota: 100}).Error)

	requestID := "req_billing_refund_token_idempotency_9907"
	first := &BillingSession{
		relayInfo:        &relaycommon.RelayInfo{UserId: 9907, RequestId: requestID, TokenId: 9907, TokenKey: "refund-token-idempotency-key"},
		funding:          &WalletFunding{userId: 9907, consumed: 100},
		preConsumedQuota: 100,
		tokenConsumed:    100,
	}
	require.NoError(t, first.refundFinancial())

	second := &BillingSession{
		relayInfo:        &relaycommon.RelayInfo{UserId: 9907, RequestId: requestID, TokenId: 9907, TokenKey: "refund-token-idempotency-key"},
		funding:          &WalletFunding{userId: 9907, consumed: 100},
		preConsumedQuota: 100,
		tokenConsumed:    100,
	}
	require.NoError(t, second.refundFinancial())

	var user model.User
	var token model.Token
	require.NoError(t, db.First(&user, 9907).Error)
	require.NoError(t, db.First(&token, 9907).Error)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 100, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	var eventCount, claimCount int64
	require.NoError(t, db.Model(&model.AetherLedgerEvent{}).Where("event_type = ?", model.AetherLedgerEventFinancial).Count(&eventCount).Error)
	require.NoError(t, db.Model(&model.BillingRefundClaim{}).Count(&claimCount).Error)
	assert.Equal(t, int64(1), eventCount)
	assert.Equal(t, int64(1), claimCount)
}

func TestBillingSessionRefundRetriesAfterAetherEventFailure(t *testing.T) {
	previousDB := model.DB
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	db, err := gorm.Open(sqlite.Open("file:billing_session_refund_retry_after_event_failure?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.AetherIntegration{}, &model.AetherLedgerEvent{}, &model.BillingRefundClaim{}))
	integration := &model.AetherIntegration{
		ChannelID:                   9908,
		InstanceID:                  "aether-refund-retry-after-event-failure",
		ExecutionMode:               model.AetherExecutionModeDirectChannel,
		Enabled:                     true,
		ConfigRevision:              1,
		ControlSecretEncrypted:      "invalid-secret",
		RelaySigningSecretEncrypted: "invalid-secret",
	}
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&model.User{Id: 9908, Username: "refund-retry-after-event-failure", Quota: 0}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 9908, UserId: 9908, Key: "refund-retry-after-event-failure-key", RemainQuota: 0, UsedQuota: 100}).Error)

	session := &BillingSession{
		relayInfo:        &relaycommon.RelayInfo{UserId: 9908, RequestId: "req_billing_refund_retry_after_event_failure_9908", TokenId: 9908, TokenKey: "refund-retry-after-event-failure-key"},
		funding:          &WalletFunding{userId: 9908, consumed: 100},
		preConsumedQuota: 100,
		tokenConsumed:    100,
	}
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	session.Refund(context)
	require.Eventually(t, session.NeedsRefund, time.Second, 10*time.Millisecond)

	var user model.User
	var token model.Token
	require.NoError(t, db.First(&user, 9908).Error)
	require.NoError(t, db.First(&token, 9908).Error)
	assert.Zero(t, user.Quota)
	assert.Zero(t, token.RemainQuota)
	assert.Equal(t, 100, token.UsedQuota)
	var eventCount, claimCount int64
	require.NoError(t, db.Model(&model.AetherLedgerEvent{}).Count(&eventCount).Error)
	require.NoError(t, db.Model(&model.BillingRefundClaim{}).Count(&claimCount).Error)
	assert.Zero(t, eventCount)
	assert.Zero(t, claimCount)

	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Save(integration).Error)
	session.Refund(context)
	require.Eventually(t, func() bool {
		var updatedUser model.User
		var updatedToken model.Token
		var updatedEventCount, updatedClaimCount int64
		return db.First(&updatedUser, 9908).Error == nil &&
			db.First(&updatedToken, 9908).Error == nil &&
			db.Model(&model.AetherLedgerEvent{}).Where("event_type = ?", model.AetherLedgerEventFinancial).Count(&updatedEventCount).Error == nil &&
			db.Model(&model.BillingRefundClaim{}).Count(&updatedClaimCount).Error == nil &&
			updatedUser.Quota == 100 && updatedToken.RemainQuota == 100 && updatedToken.UsedQuota == 0 && updatedEventCount == 1 && updatedClaimCount == 1
	}, time.Second, 10*time.Millisecond)
}
