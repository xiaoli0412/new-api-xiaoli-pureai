package model

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecordAetherFinancialEventTxFollowsBusinessTransaction(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_financial_tx_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&AetherIntegration{}, &AetherLedgerEvent{}))

	integration := &AetherIntegration{ChannelID: 91, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	input := AetherFinancialEventInput{
		UserID:     17,
		SourceType: "topup",
		SourceID:   "rollback-23",
		QuotaDelta: 456,
		OccurredAt: 1_784_073_600,
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, RecordAetherFinancialEventTx(tx, input))
		return errors.New("rollback business transaction")
	})
	require.Error(t, err)

	var count int64
	require.NoError(t, db.Model(&AetherLedgerEvent{}).Count(&count).Error)
	assert.Zero(t, count)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RecordAetherFinancialEventTx(tx, input)
	}))
	require.NoError(t, db.Model(&AetherLedgerEvent{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestAetherLedgerDuplicateDoesNotAbortMainDatabaseTransaction(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_duplicate_main_transaction_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&User{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	require.NoError(t, db.Create(&User{Id: 17, Username: "user", Quota: 100}).Error)

	integration := &AetherIntegration{ChannelID: 91, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	input := AetherFinancialEventInput{
		UserID:     17,
		SourceType: "topup",
		SourceID:   "duplicate-main-transaction-23",
		QuotaDelta: 456,
		OccurredAt: 1_784_073_600,
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RecordAetherFinancialEventTx(tx, input)
	}))

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&User{}).Where("id = ?", input.UserID).Update("quota", 200).Error; err != nil {
			return err
		}
		return RecordAetherFinancialEventTx(tx, input)
	}))

	var user User
	require.NoError(t, db.First(&user, input.UserID).Error)
	assert.Equal(t, 200, user.Quota)
	var count int64
	require.NoError(t, db.Model(&AetherLedgerEvent{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestAetherLedgerEventTxFunctionsFollowBusinessTransaction(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_all_event_tx_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&AetherIntegration{}, &AetherLedgerEvent{}))

	integration := &AetherIntegration{ChannelID: 91, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	log := &Log{
		Type:             LogTypeConsume,
		UserId:           17,
		TokenId:          29,
		ModelName:        "gpt-5",
		ChannelId:        42,
		Group:            "pro",
		Quota:            321,
		PromptTokens:     100,
		CompletionTokens: 50,
		RequestId:        "req_all_tx",
		CreatedAt:        1_784_073_600,
	}
	subscription := AetherSubscriptionEventInput{
		UserID:         17,
		SubscriptionID: 31,
		PlanID:         9,
		Status:         "active",
		Action:         "activated",
		AmountTotal:    500,
		StartTime:      1_784_073_600,
		EndTime:        1_786_665_600,
		OccurredAt:     1_784_073_600,
	}
	channel := &Channel{Id: 42, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Models: "gpt-5", Group: "pro"}

	recordAll := func(tx *gorm.DB) error {
		if err := RecordAetherUsageEventTx(tx, log); err != nil {
			return err
		}
		if err := RecordAetherSubscriptionEventTx(tx, subscription); err != nil {
			return err
		}
		if err := RecordAetherChannelEventTx(tx, channel, "updated", 1_784_073_600); err != nil {
			return err
		}
		if err := RecordAetherChannelBalanceObservationTx(tx, channel.Id, 12.5, 1_784_073_600); err != nil {
			return err
		}
		return RecordAetherPricingEventTx(tx, "ModelRatio", 1_784_073_600)
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, recordAll(tx))
		return errors.New("rollback business transaction")
	})
	require.Error(t, err)

	var count int64
	require.NoError(t, db.Model(&AetherLedgerEvent{}).Count(&count).Error)
	assert.Zero(t, count)

	require.NoError(t, db.Transaction(recordAll))
	require.NoError(t, db.Model(&AetherLedgerEvent{}).Count(&count).Error)
	assert.Equal(t, int64(5), count)
}

func TestAetherLedgerEventTxUsesAtomicDedupeWrite(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_atomic_dedupe_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&AetherIntegration{}, &AetherLedgerEvent{}))

	integration := &AetherIntegration{
		ChannelID:      91,
		InstanceID:     "aether-primary",
		ExecutionMode:  AetherExecutionModeDirectChannel,
		Enabled:        true,
		ConfigRevision: 1,
	}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	// A preflight read leaves a race between the duplicate check and the insert.
	// The outbox must rely on its unique key and an atomic conflict-safe insert instead.
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(
		"aether_ledger_reject_preflight_dedupe_query",
		func(tx *gorm.DB) {
			if tx.Statement.Table == "aether_ledger_events" {
				tx.AddError(errors.New("aether ledger dedupe must not preflight-read"))
			}
		},
	))

	log := &Log{
		Type:             LogTypeConsume,
		UserId:           17,
		TokenId:          29,
		ModelName:        "gpt-5",
		ChannelId:        42,
		Group:            "pro",
		Quota:            321,
		PromptTokens:     100,
		CompletionTokens: 50,
		RequestId:        "req_atomic_dedupe",
		CreatedAt:        1_784_073_600,
	}
	subscription := AetherSubscriptionEventInput{
		UserID:         17,
		SubscriptionID: 31,
		PlanID:         9,
		Status:         "active",
		Action:         "activated",
		AmountTotal:    500,
		StartTime:      1_784_073_600,
		EndTime:        1_786_665_600,
		OccurredAt:     1_784_073_600,
	}
	channel := &Channel{
		Id:     42,
		Type:   constant.ChannelTypeOpenAI,
		Status: common.ChannelStatusEnabled,
		Models: "gpt-5",
		Group:  "pro",
	}

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := RecordAetherUsageEventTx(tx, log); err != nil {
			return err
		}
		if err := RecordAetherFinancialEventTx(tx, AetherFinancialEventInput{
			UserID:     17,
			SourceType: "topup",
			SourceID:   "atomic-dedupe-topup",
			QuotaDelta: 456,
			OccurredAt: 1_784_073_600,
		}); err != nil {
			return err
		}
		if err := RecordAetherSubscriptionEventTx(tx, subscription); err != nil {
			return err
		}
		if err := RecordAetherChannelBalanceObservationTxWithMutationID(
			tx,
			channel.Id,
			12.5,
			1_784_073_600,
			"atomic-dedupe-balance",
		); err != nil {
			return err
		}
		if err := RecordAetherChannelEventTxWithMutationID(
			tx,
			channel,
			"updated",
			1_784_073_600,
			"atomic-dedupe-channel",
		); err != nil {
			return err
		}
		return RecordAetherPricingEventTxWithMutationID(
			tx,
			"ModelRatio",
			1_784_073_600,
			"atomic-dedupe-pricing",
		)
	}))
	require.NoError(t, db.Callback().Query().Remove("aether_ledger_reject_preflight_dedupe_query"))

	var count int64
	require.NoError(t, db.Model(&AetherLedgerEvent{}).Count(&count).Error)
	assert.Equal(t, int64(6), count)
}

func TestRecordAetherUsageEventIsDeduplicatedAndAnonymous(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_ledger_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&AetherIntegration{}, &AetherLedgerEvent{}))

	integration := &AetherIntegration{
		ChannelID:      42,
		InstanceID:     "aether-primary",
		ExecutionMode:  AetherExecutionModeDirectChannel,
		Enabled:        true,
		ConfigRevision: 1,
	}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	log := &Log{
		Type:              LogTypeConsume,
		UserId:            17,
		Username:          "sensitive-user-name",
		TokenId:           29,
		TokenName:         "sensitive-token-name",
		ModelName:         "gpt-5",
		ChannelId:         42,
		Group:             "pro",
		Quota:             321,
		PromptTokens:      100,
		CompletionTokens:  50,
		RequestId:         "req_123",
		UpstreamRequestId: "upstream_456",
		CreatedAt:         1_784_073_600,
	}

	RecordAetherUsageEvent(log)
	RecordAetherUsageEvent(log)

	var events []AetherLedgerEvent
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, AetherLedgerEventUsageSettled, events[0].EventType)
	assert.Equal(t, "aether-primary", events[0].InstanceID)
	assert.Contains(t, events[0].Payload, `"subject_id":"u_`)
	assert.Contains(t, events[0].Payload, `"channel_id":"42"`)
	assert.Contains(t, events[0].Payload, `"charged_quota":"321"`)
	assert.NotContains(t, events[0].Payload, "sensitive-user-name")
	assert.NotContains(t, events[0].Payload, "sensitive-token-name")
	assert.NotContains(t, events[0].Payload, "control-secret")
	assert.NotContains(t, events[0].Payload, "relay-signing-secret")
	assert.Equal(t, common.QuotaPerUnit, events[0].QuotaPerUnitSnapshot)
}

func TestRecordAetherFinancialEventExcludesPaymentIdentifiers(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_financial_ledger_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 43, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	RecordAetherFinancialEvent(AetherFinancialEventInput{
		UserID:          17,
		SourceType:      "topup",
		SourceID:        "topup:23",
		QuotaDelta:      456,
		MoneyAmount:     "12.34",
		PaymentCategory: "stripe",
		OccurredAt:      1_784_073_600,
	})

	var event AetherLedgerEvent
	require.NoError(t, db.First(&event).Error)
	assert.Equal(t, AetherLedgerEventFinancial, event.EventType)
	assert.Contains(t, event.Payload, `"subject_id":"u_`)
	assert.Contains(t, event.Payload, `"quota_delta":"456"`)
	assert.NotContains(t, event.Payload, "control-secret")
	assert.NotContains(t, event.Payload, "trade_no")
}

func TestRecordAetherSubscriptionEventIsDeduplicatedAndAnonymous(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_subscription_ledger_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 44, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	input := AetherSubscriptionEventInput{
		UserID:         17,
		SubscriptionID: 71,
		PlanID:         8,
		Status:         "active",
		Action:         "activated",
		AmountTotal:    500_000,
		AmountUsed:     0,
		StartTime:      1_784_073_600,
		EndTime:        1_786_665_600,
		OccurredAt:     1_784_073_600,
	}
	RecordAetherSubscriptionEvent(input)
	RecordAetherSubscriptionEvent(input)

	var events []AetherLedgerEvent
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, AetherLedgerEventSubscriptionChanged, events[0].EventType)
	assert.Contains(t, events[0].Payload, `"subject_id":"u_`)
	assert.Contains(t, events[0].Payload, `"action":"activated"`)
	assert.Contains(t, events[0].Payload, `"amount_total":"500000"`)
	assert.NotContains(t, events[0].Payload, `"user_id"`)
	assert.NotContains(t, events[0].Payload, "control-secret")
	assert.NotContains(t, events[0].Payload, "relay-signing-secret")
}

func TestChannelBalanceObservationRecordsSafeAetherEvent(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_channel_balance_ledger_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&Channel{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 82, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	channel := &Channel{Id: 81, Type: constant.ChannelTypeOpenAI, Name: "temporary-channel", Key: "upstream-secret", Group: "default", Balance: 2}
	require.NoError(t, db.Create(channel).Error)

	require.NoError(t, channel.UpdateBalance(12.34))

	var event AetherLedgerEvent
	require.NoError(t, db.First(&event).Error)
	assert.Equal(t, AetherLedgerEventChannelBalanceObserved, event.EventType)
	assert.Contains(t, event.Payload, `"channel_id":"81"`)
	assert.Contains(t, event.Payload, `"balance":"12.34"`)
	assert.NotContains(t, event.Payload, "upstream-secret")
}

func TestChannelUpdateRecordsSafeAetherMetadataEvent(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_channel_change_ledger_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 84, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	channel := &Channel{Id: 83, Type: constant.ChannelTypeOpenAI, Name: "temporary-channel", Key: "upstream-secret", Group: "default", Models: "gpt-5", Status: 1}
	require.NoError(t, db.Create(channel).Error)
	channel.Status = 2
	require.NoError(t, channel.Update())

	var event AetherLedgerEvent
	require.NoError(t, db.First(&event).Error)
	assert.Equal(t, AetherLedgerEventChannel, event.EventType)
	assert.Contains(t, event.Payload, `"channel_id":"83"`)
	assert.Contains(t, event.Payload, `"action":"updated"`)
	assert.Contains(t, event.Payload, `"models":"gpt-5"`)
	assert.NotContains(t, event.Payload, "upstream-secret")
	assert.NotContains(t, event.Payload, "temporary-channel")
}

func TestBatchChannelInsertRecordsAetherMetadataEvents(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_batch_channel_change_ledger_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 85, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	require.NoError(t, BatchInsertChannels([]Channel{
		{Type: constant.ChannelTypeOpenAI, Name: "temporary-one", Key: "secret-one", Group: "default", Models: "gpt-5", Status: 1},
		{Type: constant.ChannelTypeOpenAI, Name: "temporary-two", Key: "secret-two", Group: "default", Models: "gpt-5-mini", Status: 1},
	}))

	var events []AetherLedgerEvent
	require.NoError(t, db.Order("id asc").Find(&events).Error)
	require.Len(t, events, 2)
	for _, event := range events {
		assert.Equal(t, AetherLedgerEventChannel, event.EventType)
		assert.Contains(t, event.Payload, `"action":"created"`)
		assert.NotContains(t, event.Payload, "secret-")
	}
}

func TestBatchChannelDeleteRecordsAetherMetadataEvents(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_batch_channel_delete_ledger_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 88, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create([]Channel{
		{Id: 86, Type: constant.ChannelTypeOpenAI, Name: "temporary-one", Key: "secret-one", Group: "default", Models: "gpt-5", Status: 1},
		{Id: 87, Type: constant.ChannelTypeOpenAI, Name: "temporary-two", Key: "secret-two", Group: "default", Models: "gpt-5-mini", Status: 1},
	}).Error)

	require.NoError(t, BatchDeleteChannels([]int{86, 87}))

	var events []AetherLedgerEvent
	require.NoError(t, db.Order("id asc").Find(&events).Error)
	require.Len(t, events, 2)
	for _, event := range events {
		assert.Equal(t, AetherLedgerEventChannel, event.EventType)
		assert.Contains(t, event.Payload, `"action":"deleted"`)
		assert.NotContains(t, event.Payload, "secret-")
	}
}

func TestIndependentAetherPricingEventsDoNotExposeOptionValues(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_pricing_ledger_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 89, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	RecordAetherPricingEvent("ModelRatio", 1_784_073_600)
	RecordAetherPricingEvent("ModelRatio", 1_784_073_600)

	var events []AetherLedgerEvent
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 2)
	for _, event := range events {
		assert.Equal(t, AetherLedgerEventPricingChanged, event.EventType)
		assert.Contains(t, event.Payload, `"scope":"ModelRatio"`)
		assert.NotContains(t, event.Payload, "control-secret")
		assert.NotContains(t, event.Payload, "relay-signing-secret")
	}
}

func TestPricingOptionUpdateRecordsAetherPricingEvent(t *testing.T) {
	previousDB := DB
	previousQuotaPerUnit := common.QuotaPerUnit
	common.OptionMapRWMutex.Lock()
	previousOptionMap := common.OptionMap
	common.OptionMap = make(map[string]string)
	common.OptionMapRWMutex.Unlock()
	db, err := gorm.Open(sqlite.Open("file:aether_pricing_option_ledger_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		common.QuotaPerUnit = previousQuotaPerUnit
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})
	require.NoError(t, db.AutoMigrate(&Option{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 90, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	require.NoError(t, UpdateOption("QuotaPerUnit", "600000"))

	var event AetherLedgerEvent
	require.NoError(t, db.First(&event).Error)
	assert.Equal(t, AetherLedgerEventPricingChanged, event.EventType)
	assert.Contains(t, event.Payload, `"scope":"QuotaPerUnit"`)
	assert.NotContains(t, event.Payload, "600000")
}

func TestTaskRefundLogRecordsAetherFinancialEvent(t *testing.T) {
	previousDB := DB
	previousLogDB := LOG_DB
	db, err := gorm.Open(sqlite.Open("file:aether_task_refund_ledger_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	LOG_DB = db
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
	})
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Log{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 91, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&User{Id: 21, Username: "sensitive-user", Group: "default"}).Error)

	RecordTaskBillingLog(RecordTaskBillingLogParams{
		UserId:    21,
		LogType:   LogTypeRefund,
		SourceID:  "task:task-91:failure",
		ChannelId: 17,
		ModelName: "gpt-5",
		Quota:     456,
		TokenId:   31,
		Group:     "default",
	})

	var event AetherLedgerEvent
	require.NoError(t, db.First(&event).Error)
	assert.Equal(t, AetherLedgerEventFinancial, event.EventType)
	assert.Contains(t, event.Payload, `"source_type":"usage_refund"`)
	assert.Contains(t, event.Payload, `"quota_delta":"456"`)
	assert.NotContains(t, event.Payload, "sensitive-user")
}
