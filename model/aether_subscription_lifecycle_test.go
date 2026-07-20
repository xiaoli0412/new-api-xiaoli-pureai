package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCompleteSubscriptionOrderRecordsAetherLifecycleEvents(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_subscription_lifecycle_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(
		&User{},
		&SubscriptionPlan{},
		&SubscriptionOrder{},
		&UserSubscription{},
		&TopUp{},
		&AetherIntegration{},
		&AetherLedgerEvent{},
	))

	integration := &AetherIntegration{ChannelID: 45, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&User{Id: 17, Username: "sensitive-user", Group: "default"}).Error)
	plan := &SubscriptionPlan{
		Id:              901,
		Title:           "Sensitive plan title",
		PriceAmount:     12.34,
		Currency:        "USD",
		DurationUnit:    SubscriptionDurationCustom,
		CustomSeconds:   3600,
		Enabled:         true,
		TotalAmount:     500_000,
		AllowBalancePay: common.GetPointer(true),
	}
	require.NoError(t, db.Create(plan).Error)
	order := &SubscriptionOrder{
		UserId:          17,
		PlanId:          plan.Id,
		Money:           plan.PriceAmount,
		TradeNo:         "sensitive-external-trade-number",
		PaymentMethod:   "card",
		PaymentProvider: PaymentProviderStripe,
		Status:          common.TopUpStatusPending,
		CreateTime:      1_784_073_600,
	}
	require.NoError(t, db.Create(order).Error)

	require.NoError(t, CompleteSubscriptionOrder(order.TradeNo, "", PaymentProviderStripe, "card"))

	var events []AetherLedgerEvent
	require.NoError(t, db.Order("id asc").Find(&events).Error)
	require.Len(t, events, 2)
	assert.Equal(t, AetherLedgerEventSubscriptionChanged, events[0].EventType)
	assert.Equal(t, AetherLedgerEventFinancial, events[1].EventType)
	assert.Contains(t, events[0].Payload, `"action":"activated"`)
	assert.Contains(t, events[1].Payload, `"source_type":"subscription_payment"`)
	assert.NotContains(t, events[0].Payload, "sensitive-user")
	assert.NotContains(t, events[0].Payload, "Sensitive plan title")
	assert.NotContains(t, events[1].Payload, order.TradeNo)
}

func TestPurchaseSubscriptionWithBalanceRecordsAetherLifecycleEvents(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_subscription_balance_lifecycle_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(
		&User{},
		&SubscriptionPlan{},
		&SubscriptionOrder{},
		&UserSubscription{},
		&AetherIntegration{},
		&AetherLedgerEvent{},
	))

	integration := &AetherIntegration{ChannelID: 46, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&User{Id: 18, Username: "sensitive-user", Group: "default", Quota: 1_000_000}).Error)
	plan := &SubscriptionPlan{
		Id:              902,
		Title:           "Sensitive balance plan",
		PriceAmount:     1,
		Currency:        "USD",
		DurationUnit:    SubscriptionDurationCustom,
		CustomSeconds:   3600,
		Enabled:         true,
		TotalAmount:     500_000,
		AllowBalancePay: common.GetPointer(true),
	}
	require.NoError(t, db.Create(plan).Error)

	require.NoError(t, PurchaseSubscriptionWithBalance(18, plan.Id))

	var events []AetherLedgerEvent
	require.NoError(t, db.Order("id asc").Find(&events).Error)
	require.Len(t, events, 2)
	assert.Equal(t, AetherLedgerEventSubscriptionChanged, events[0].EventType)
	assert.Equal(t, AetherLedgerEventFinancial, events[1].EventType)
	assert.Contains(t, events[0].Payload, `"action":"activated"`)
	assert.Contains(t, events[1].Payload, `"source_type":"subscription_balance_purchase"`)
	assert.Contains(t, events[1].Payload, `"quota_delta":"-500000"`)
	assert.NotContains(t, events[1].Payload, "SUBBALUSR")
}

func TestAdminInvalidateUserSubscriptionRecordsAetherLifecycleEvent(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_subscription_cancel_lifecycle_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&User{}, &UserSubscription{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 47, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&User{Id: 19, Username: "sensitive-user", Group: "default"}).Error)
	subscription := &UserSubscription{
		UserId:      19,
		PlanId:      903,
		AmountTotal: 500_000,
		AmountUsed:  1_000,
		StartTime:   1_784_073_600,
		EndTime:     1_786_665_600,
		Status:      "active",
	}
	require.NoError(t, db.Create(subscription).Error)

	_, err = AdminInvalidateUserSubscription(subscription.Id)
	require.NoError(t, err)

	var event AetherLedgerEvent
	require.NoError(t, db.First(&event).Error)
	assert.Equal(t, AetherLedgerEventSubscriptionChanged, event.EventType)
	assert.Contains(t, event.Payload, `"status":"cancelled"`)
	assert.Contains(t, event.Payload, `"action":"cancelled"`)
	assert.NotContains(t, event.Payload, "sensitive-user")
}

func TestAdminDeleteUserSubscriptionRecordsAetherLifecycleEvent(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_subscription_delete_lifecycle_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&User{}, &UserSubscription{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 48, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&User{Id: 20, Username: "sensitive-user", Group: "default"}).Error)
	subscription := &UserSubscription{
		UserId:      20,
		PlanId:      904,
		AmountTotal: 500_000,
		AmountUsed:  1_000,
		StartTime:   1_784_073_600,
		EndTime:     1_786_665_600,
		Status:      "active",
	}
	require.NoError(t, db.Create(subscription).Error)

	_, err = AdminDeleteUserSubscription(subscription.Id)
	require.NoError(t, err)

	var event AetherLedgerEvent
	require.NoError(t, db.First(&event).Error)
	assert.Equal(t, AetherLedgerEventSubscriptionChanged, event.EventType)
	assert.Contains(t, event.Payload, `"status":"deleted"`)
	assert.Contains(t, event.Payload, `"action":"deleted"`)
	assert.NotContains(t, event.Payload, "sensitive-user")
}

func TestExpireDueSubscriptionsRecordsAetherLifecycleEvent(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_subscription_expiry_lifecycle_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&User{}, &UserSubscription{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 49, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&User{Id: 22, Username: "sensitive-user", Group: "default"}).Error)
	subscription := &UserSubscription{
		UserId:      22,
		PlanId:      905,
		AmountTotal: 500_000,
		AmountUsed:  1_000,
		StartTime:   1_784_073_600,
		EndTime:     time.Now().Add(-time.Minute).Unix(),
		Status:      "active",
	}
	require.NoError(t, db.Create(subscription).Error)

	expired, err := ExpireDueSubscriptions(1)
	require.NoError(t, err)
	assert.Equal(t, 1, expired)

	var event AetherLedgerEvent
	require.NoError(t, db.First(&event).Error)
	assert.Equal(t, AetherLedgerEventSubscriptionChanged, event.EventType)
	assert.Contains(t, event.Payload, `"status":"expired"`)
	assert.Contains(t, event.Payload, `"action":"expired"`)
	assert.NotContains(t, event.Payload, "sensitive-user")
}
