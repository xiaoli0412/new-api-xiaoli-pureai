package model

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openAetherOutboxFailureDB(t *testing.T, name string, models ...interface{}) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	models = append(models, &AetherIntegration{}, &AetherLedgerEvent{})
	require.NoError(t, db.AutoMigrate(models...))
	require.NoError(t, db.Create(&AetherIntegration{
		ChannelID:                   91,
		InstanceID:                  "aether-primary",
		ExecutionMode:               AetherExecutionModeDirectChannel,
		Enabled:                     true,
		ConfigRevision:              1,
		ControlSecretEncrypted:      "invalid-secret",
		RelaySigningSecretEncrypted: "invalid-secret",
	}).Error)
	DB = db
	return db
}

func TestTopUpCompletionRollsBackWhenAetherOutboxWriteFails(t *testing.T) {
	previousDB := DB
	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() {
		DB = previousDB
		common.QuotaPerUnit = previousQuotaPerUnit
	})

	tests := []struct {
		name     string
		provider string
		amount   int64
		money    float64
		complete func(string) error
	}{
		{name: "stripe", provider: PaymentProviderStripe, money: 1, complete: func(tradeNo string) error {
			return Recharge(tradeNo, "customer", "127.0.0.1")
		}},
		{name: "manual", provider: PaymentProviderWaffo, amount: 1, money: 1, complete: func(tradeNo string) error {
			return ManualCompleteTopUp(tradeNo, "127.0.0.1")
		}},
		{name: "creem", provider: PaymentProviderCreem, amount: 500000, money: 1, complete: func(tradeNo string) error {
			return RechargeCreem(tradeNo, "customer@example.com", "Customer", "127.0.0.1")
		}},
		{name: "waffo", provider: PaymentProviderWaffo, amount: 1, money: 1, complete: func(tradeNo string) error {
			return RechargeWaffo(tradeNo, "127.0.0.1")
		}},
		{name: "waffo pancake", provider: PaymentProviderWaffoPancake, amount: 1, money: 1, complete: RechargeWaffoPancake},
		{name: "epay", provider: PaymentProviderEpay, amount: 1, money: 1, complete: func(tradeNo string) error {
			_, _, err := RechargeEpay(tradeNo, "alipay")
			return err
		}},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openAetherOutboxFailureDB(t, fmt.Sprintf("aether_topup_outbox_failure_%d", index), &User{}, &TopUp{})
			require.NoError(t, db.Create(&User{Id: 17, Username: "user", Quota: 100}).Error)
			tradeNo := fmt.Sprintf("topup-%d", index)
			require.NoError(t, db.Create(&TopUp{
				UserId:          17,
				Amount:          test.amount,
				Money:           test.money,
				TradeNo:         tradeNo,
				PaymentProvider: test.provider,
				Status:          common.TopUpStatusPending,
			}).Error)

			err := test.complete(tradeNo)
			require.Error(t, err)

			var topUp TopUp
			require.NoError(t, db.Where("trade_no = ?", tradeNo).First(&topUp).Error)
			assert.Equal(t, common.TopUpStatusPending, topUp.Status)
			assert.Zero(t, topUp.CompleteTime)
			var user User
			require.NoError(t, db.First(&user, 17).Error)
			assert.Equal(t, 100, user.Quota)
			var eventCount int64
			require.NoError(t, db.Model(&AetherLedgerEvent{}).Count(&eventCount).Error)
			assert.Zero(t, eventCount)
		})
	}
}

func TestSubscriptionMutationsRollBackWhenAetherOutboxWriteFails(t *testing.T) {
	previousDB := DB
	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() {
		DB = previousDB
		common.QuotaPerUnit = previousQuotaPerUnit
	})

	t.Run("paid purchase", func(t *testing.T) {
		db := openAetherOutboxFailureDB(t, "aether_subscription_paid_outbox_failure",
			&User{}, &SubscriptionPlan{}, &SubscriptionOrder{}, &UserSubscription{}, &TopUp{})
		require.NoError(t, db.Create(&User{Id: 21, Username: "user", Group: "default"}).Error)
		plan := &SubscriptionPlan{Id: 9201, Title: "Plan", PriceAmount: 1, DurationUnit: SubscriptionDurationCustom, CustomSeconds: 3600, Enabled: true, TotalAmount: 500000}
		require.NoError(t, db.Create(plan).Error)
		order := &SubscriptionOrder{UserId: 21, PlanId: plan.Id, Money: 1, TradeNo: "subscription-paid", PaymentMethod: "card", PaymentProvider: PaymentProviderStripe, Status: common.TopUpStatusPending}
		require.NoError(t, db.Create(order).Error)

		err := CompleteSubscriptionOrder(order.TradeNo, "", PaymentProviderStripe, "card")
		require.Error(t, err)

		require.NoError(t, db.First(order, order.Id).Error)
		assert.Equal(t, common.TopUpStatusPending, order.Status)
		var count int64
		require.NoError(t, db.Model(&UserSubscription{}).Count(&count).Error)
		assert.Zero(t, count)
		require.NoError(t, db.Model(&TopUp{}).Count(&count).Error)
		assert.Zero(t, count)
		require.NoError(t, db.Model(&AetherLedgerEvent{}).Count(&count).Error)
		assert.Zero(t, count)
	})

	t.Run("balance purchase", func(t *testing.T) {
		db := openAetherOutboxFailureDB(t, "aether_subscription_balance_outbox_failure",
			&User{}, &SubscriptionPlan{}, &SubscriptionOrder{}, &UserSubscription{})
		require.NoError(t, db.Create(&User{Id: 22, Username: "user", Group: "default", Quota: 1000000}).Error)
		plan := &SubscriptionPlan{Id: 9202, Title: "Plan", PriceAmount: 1, DurationUnit: SubscriptionDurationCustom, CustomSeconds: 3600, Enabled: true, TotalAmount: 500000, AllowBalancePay: common.GetPointer(true)}
		require.NoError(t, db.Create(plan).Error)

		err := PurchaseSubscriptionWithBalance(22, plan.Id)
		require.Error(t, err)

		var user User
		require.NoError(t, db.First(&user, 22).Error)
		assert.Equal(t, 1000000, user.Quota)
		var count int64
		require.NoError(t, db.Model(&UserSubscription{}).Count(&count).Error)
		assert.Zero(t, count)
		require.NoError(t, db.Model(&SubscriptionOrder{}).Count(&count).Error)
		assert.Zero(t, count)
		require.NoError(t, db.Model(&AetherLedgerEvent{}).Count(&count).Error)
		assert.Zero(t, count)
	})

	t.Run("cancel", func(t *testing.T) {
		db := openAetherOutboxFailureDB(t, "aether_subscription_cancel_outbox_failure", &User{}, &UserSubscription{})
		require.NoError(t, db.Create(&User{Id: 23, Username: "user", Group: "default"}).Error)
		subscription := &UserSubscription{UserId: 23, PlanId: 9203, AmountTotal: 500000, StartTime: 100, EndTime: time.Now().Add(time.Hour).Unix(), Status: "active"}
		require.NoError(t, db.Create(subscription).Error)

		_, err := AdminInvalidateUserSubscription(subscription.Id)
		require.Error(t, err)

		var stored UserSubscription
		require.NoError(t, db.First(&stored, subscription.Id).Error)
		assert.Equal(t, "active", stored.Status)
		assert.Equal(t, subscription.EndTime, stored.EndTime)
	})

	t.Run("delete", func(t *testing.T) {
		db := openAetherOutboxFailureDB(t, "aether_subscription_delete_outbox_failure", &User{}, &UserSubscription{})
		require.NoError(t, db.Create(&User{Id: 24, Username: "user", Group: "default"}).Error)
		subscription := &UserSubscription{UserId: 24, PlanId: 9204, AmountTotal: 500000, StartTime: 100, EndTime: time.Now().Add(time.Hour).Unix(), Status: "active"}
		require.NoError(t, db.Create(subscription).Error)

		_, err := AdminDeleteUserSubscription(subscription.Id)
		require.Error(t, err)

		var count int64
		require.NoError(t, db.Model(&UserSubscription{}).Where("id = ?", subscription.Id).Count(&count).Error)
		assert.Equal(t, int64(1), count)
	})

	t.Run("expiry", func(t *testing.T) {
		db := openAetherOutboxFailureDB(t, "aether_subscription_expiry_outbox_failure", &User{}, &UserSubscription{})
		require.NoError(t, db.Create(&User{Id: 25, Username: "user", Group: "default"}).Error)
		subscription := &UserSubscription{UserId: 25, PlanId: 9205, AmountTotal: 500000, StartTime: 100, EndTime: time.Now().Add(-time.Minute).Unix(), Status: "active"}
		require.NoError(t, db.Create(subscription).Error)

		_, err := ExpireDueSubscriptions(1)
		require.Error(t, err)

		var stored UserSubscription
		require.NoError(t, db.First(&stored, subscription.Id).Error)
		assert.Equal(t, "active", stored.Status)
	})

	t.Run("admin bind", func(t *testing.T) {
		db := openAetherOutboxFailureDB(t, "aether_subscription_admin_bind_outbox_failure", &User{}, &SubscriptionPlan{}, &UserSubscription{})
		require.NoError(t, db.Create(&User{Id: 26, Username: "user", Group: "default"}).Error)
		plan := &SubscriptionPlan{Id: 9206, Title: "Plan", DurationUnit: SubscriptionDurationCustom, CustomSeconds: 3600, Enabled: true, TotalAmount: 500000}
		require.NoError(t, db.Create(plan).Error)

		_, err := AdminBindSubscription(26, plan.Id, "manual")
		require.Error(t, err)

		var count int64
		require.NoError(t, db.Model(&UserSubscription{}).Count(&count).Error)
		assert.Zero(t, count)
	})
}

func openAetherMissingOutboxDB(t *testing.T, name string, models ...interface{}) *gorm.DB {
	t.Helper()
	db := openAetherOutboxFailureDB(t, name, models...)
	require.NoError(t, db.Migrator().DropTable(&AetherLedgerEvent{}))
	return db
}

func TestChannelMutationsRollBackWhenAetherOutboxWriteFails(t *testing.T) {
	previousDB := DB
	t.Cleanup(func() {
		DB = previousDB
	})

	t.Run("batch insert", func(t *testing.T) {
		db := openAetherMissingOutboxDB(t, "aether_channel_batch_insert_outbox_failure", &Channel{}, &Ability{})
		err := BatchInsertChannels([]Channel{{Type: constant.ChannelTypeOpenAI, Name: "new", Models: "gpt-5", Group: "default", Status: common.ChannelStatusEnabled}})
		require.Error(t, err)

		var count int64
		require.NoError(t, db.Model(&Channel{}).Count(&count).Error)
		assert.Zero(t, count)
		require.NoError(t, db.Model(&Ability{}).Count(&count).Error)
		assert.Zero(t, count)
	})

	t.Run("batch delete", func(t *testing.T) {
		db := openAetherMissingOutboxDB(t, "aether_channel_batch_delete_outbox_failure", &Channel{}, &Ability{})
		channel := &Channel{Id: 31, Type: constant.ChannelTypeOpenAI, Name: "existing", Models: "gpt-5", Group: "default", Status: common.ChannelStatusEnabled}
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, channel.AddAbilities(db))

		err := BatchDeleteChannels([]int{channel.Id})
		require.Error(t, err)

		var count int64
		require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Count(&count).Error)
		assert.Equal(t, int64(1), count)
		require.NoError(t, db.Model(&Ability{}).Where("channel_id = ?", channel.Id).Count(&count).Error)
		assert.Equal(t, int64(1), count)
	})

	t.Run("insert", func(t *testing.T) {
		db := openAetherMissingOutboxDB(t, "aether_channel_insert_outbox_failure", &Channel{}, &Ability{})
		channel := &Channel{Type: constant.ChannelTypeOpenAI, Name: "new", Models: "gpt-5", Group: "default", Status: common.ChannelStatusEnabled}

		err := channel.Insert()
		require.Error(t, err)

		var count int64
		require.NoError(t, db.Model(&Channel{}).Count(&count).Error)
		assert.Zero(t, count)
		require.NoError(t, db.Model(&Ability{}).Count(&count).Error)
		assert.Zero(t, count)
	})

	t.Run("update", func(t *testing.T) {
		db := openAetherMissingOutboxDB(t, "aether_channel_update_outbox_failure", &Channel{}, &Ability{})
		stored := &Channel{Id: 32, Type: constant.ChannelTypeOpenAI, Name: "before", Models: "gpt-5", Group: "default", Status: common.ChannelStatusEnabled}
		require.NoError(t, db.Create(stored).Error)
		require.NoError(t, stored.AddAbilities(db))
		update := &Channel{Id: stored.Id, Type: constant.ChannelTypeOpenAI, Name: "after", Models: "gpt-5-mini", Group: "default", Status: common.ChannelStatusEnabled}

		err := update.Update()
		require.Error(t, err)

		var channel Channel
		require.NoError(t, db.First(&channel, stored.Id).Error)
		assert.Equal(t, "before", channel.Name)
		assert.Equal(t, "gpt-5", channel.Models)
		var ability Ability
		require.NoError(t, db.Where("channel_id = ?", stored.Id).First(&ability).Error)
		assert.Equal(t, "gpt-5", ability.Model)
	})

	t.Run("delete", func(t *testing.T) {
		db := openAetherMissingOutboxDB(t, "aether_channel_delete_outbox_failure", &Channel{}, &Ability{})
		channel := &Channel{Id: 33, Type: constant.ChannelTypeOpenAI, Name: "existing", Models: "gpt-5", Group: "default", Status: common.ChannelStatusEnabled}
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, channel.AddAbilities(db))

		err := channel.Delete()
		require.Error(t, err)

		var count int64
		require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Count(&count).Error)
		assert.Equal(t, int64(1), count)
		require.NoError(t, db.Model(&Ability{}).Where("channel_id = ?", channel.Id).Count(&count).Error)
		assert.Equal(t, int64(1), count)
	})

	t.Run("balance observation", func(t *testing.T) {
		db := openAetherMissingOutboxDB(t, "aether_channel_balance_outbox_failure", &Channel{}, &Ability{})
		channel := &Channel{Id: 34, Type: constant.ChannelTypeOpenAI, Name: "existing", Balance: 5, BalanceUpdatedTime: 100}
		require.NoError(t, db.Create(channel).Error)

		err := channel.UpdateBalance(12.5)
		require.Error(t, err)

		var stored Channel
		require.NoError(t, db.First(&stored, channel.Id).Error)
		assert.Equal(t, 5.0, stored.Balance)
		assert.Equal(t, int64(100), stored.BalanceUpdatedTime)
	})
}

func TestAdditionalChannelMutationsRollBackWhenAetherOutboxWriteFails(t *testing.T) {
	previousDB := DB
	previousMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		DB = previousDB
		common.MemoryCacheEnabled = previousMemoryCacheEnabled
	})

	createChannel := func(t *testing.T, db *gorm.DB, id int, tag string, status int) *Channel {
		t.Helper()
		channel := &Channel{Id: id, Type: constant.ChannelTypeOpenAI, Name: fmt.Sprintf("channel-%d", id), Models: "gpt-5", Group: "default", Status: status}
		channel.SetTag(tag)
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, channel.AddAbilities(db))
		return channel
	}

	t.Run("single status", func(t *testing.T) {
		db := openAetherMissingOutboxDB(t, "aether_channel_status_outbox_failure", &Channel{}, &Ability{})
		channel := createChannel(t, db, 41, "status", common.ChannelStatusEnabled)

		changed, err := UpdateChannelStatusWithMutationID(channel.Id, "", common.ChannelStatusManuallyDisabled, "test", "status-mutation")
		require.Error(t, err)
		assert.False(t, changed)
		var stored Channel
		require.NoError(t, db.First(&stored, channel.Id).Error)
		assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	})

	t.Run("batch status", func(t *testing.T) {
		db := openAetherMissingOutboxDB(t, "aether_channel_batch_status_outbox_failure", &Channel{}, &Ability{})
		first := createChannel(t, db, 42, "batch-status", common.ChannelStatusEnabled)
		second := createChannel(t, db, 43, "batch-status", common.ChannelStatusEnabled)

		changed, err := UpdateChannelStatusesWithMutationID([]int{first.Id, second.Id}, common.ChannelStatusManuallyDisabled, "test", "batch-status-mutation")
		require.Error(t, err)
		assert.Zero(t, changed)
		var disabled int64
		require.NoError(t, db.Model(&Channel{}).Where("status = ?", common.ChannelStatusManuallyDisabled).Count(&disabled).Error)
		assert.Zero(t, disabled)
	})

	for _, test := range []struct {
		name   string
		status int
		mutate func(string) error
	}{
		{name: "enable tag", status: common.ChannelStatusManuallyDisabled, mutate: func(mutationID string) error {
			return EnableChannelByTagWithMutationID("tagged", mutationID)
		}},
		{name: "disable tag", status: common.ChannelStatusEnabled, mutate: func(mutationID string) error {
			return DisableChannelByTagWithMutationID("tagged", mutationID)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openAetherMissingOutboxDB(t, "aether_channel_"+strings.ReplaceAll(test.name, " ", "_")+"_outbox_failure", &Channel{}, &Ability{})
			channel := createChannel(t, db, 44, "tagged", test.status)

			err := test.mutate(test.name + "-mutation")
			require.Error(t, err)
			var stored Channel
			require.NoError(t, db.First(&stored, channel.Id).Error)
			assert.Equal(t, test.status, stored.Status)
		})
	}

	t.Run("edit tag", func(t *testing.T) {
		db := openAetherMissingOutboxDB(t, "aether_channel_edit_tag_outbox_failure", &Channel{}, &Ability{})
		channel := createChannel(t, db, 45, "old-tag", common.ChannelStatusEnabled)
		newTag := "new-tag"

		err := EditChannelByTagWithMutationID("old-tag", &newTag, nil, nil, nil, nil, nil, nil, nil, "edit-tag-mutation")
		require.Error(t, err)
		var stored Channel
		require.NoError(t, db.First(&stored, channel.Id).Error)
		assert.Equal(t, "old-tag", stored.GetTag())
	})

	t.Run("delete by status", func(t *testing.T) {
		db := openAetherMissingOutboxDB(t, "aether_channel_delete_status_outbox_failure", &Channel{}, &Ability{})
		channel := createChannel(t, db, 46, "delete-status", common.ChannelStatusManuallyDisabled)

		_, err := DeleteChannelByStatusWithMutationID(int64(common.ChannelStatusManuallyDisabled), "delete-status-mutation")
		require.Error(t, err)
		var count int64
		require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Count(&count).Error)
		assert.Equal(t, int64(1), count)
	})

	t.Run("delete disabled", func(t *testing.T) {
		db := openAetherMissingOutboxDB(t, "aether_channel_delete_disabled_outbox_failure", &Channel{}, &Ability{})
		channel := createChannel(t, db, 47, "delete-disabled", common.ChannelStatusAutoDisabled)

		_, err := DeleteDisabledChannelWithMutationID("delete-disabled-mutation")
		require.Error(t, err)
		var count int64
		require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Count(&count).Error)
		assert.Equal(t, int64(1), count)
	})

	t.Run("batch tag", func(t *testing.T) {
		db := openAetherMissingOutboxDB(t, "aether_channel_batch_tag_outbox_failure", &Channel{}, &Ability{})
		channel := createChannel(t, db, 48, "before", common.ChannelStatusEnabled)
		tag := "after"

		err := BatchSetChannelTagWithMutationID([]int{channel.Id}, &tag, "batch-tag-mutation")
		require.Error(t, err)
		var stored Channel
		require.NoError(t, db.First(&stored, channel.Id).Error)
		assert.Equal(t, "before", stored.GetTag())
	})
}

func TestPricingOptionMutationsRollBackWhenAetherOutboxWriteFails(t *testing.T) {
	previousDB := DB
	common.OptionMapRWMutex.Lock()
	previousOptionMap := common.OptionMap
	common.OptionMap = map[string]string{"Price": "1", "MinTopUp": "5"}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	t.Run("single", func(t *testing.T) {
		db := openAetherMissingOutboxDB(t, "aether_pricing_single_outbox_failure", &Option{})
		require.NoError(t, db.Create(&Option{Key: "Price", Value: "1"}).Error)

		err := UpdateOption("Price", "2")
		require.Error(t, err)

		var option Option
		require.NoError(t, db.Where("key = ?", "Price").First(&option).Error)
		assert.Equal(t, "1", option.Value)
	})

	t.Run("bulk", func(t *testing.T) {
		db := openAetherMissingOutboxDB(t, "aether_pricing_bulk_outbox_failure", &Option{})
		require.NoError(t, db.Create([]Option{{Key: "Price", Value: "1"}, {Key: "MinTopUp", Value: "5"}}).Error)

		err := UpdateOptionsBulk(map[string]string{"Price": "2", "MinTopUp": "10"})
		require.Error(t, err)

		var options []Option
		require.NoError(t, db.Order("key asc").Find(&options).Error)
		require.Len(t, options, 2)
		assert.Equal(t, "5", options[0].Value)
		assert.Equal(t, "1", options[1].Value)
	})
}

func TestConsumeLogRollsBackWhenAetherOutboxWriteFails(t *testing.T) {
	previousDB := DB
	previousLogDB := LOG_DB
	previousLogConsumeEnabled := common.LogConsumeEnabled
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
		common.LogConsumeEnabled = previousLogConsumeEnabled
	})

	db := openAetherMissingOutboxDB(t, "aether_usage_log_outbox_failure", &Log{})
	LOG_DB = db
	common.LogConsumeEnabled = true
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Set(common.RequestIdKey, "req_usage_rollback")

	err := RecordConsumeLog(context, 17, RecordConsumeLogParams{
		ChannelId:        42,
		PromptTokens:     100,
		CompletionTokens: 50,
		ModelName:        "gpt-5",
		Quota:            321,
		TokenId:          29,
		Group:            "pro",
	})
	require.Error(t, err)

	var count int64
	require.NoError(t, db.Model(&Log{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestTaskRefundLogRollsBackWhenAetherOutboxWriteFails(t *testing.T) {
	previousDB := DB
	previousLogDB := LOG_DB
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
	})

	db := openAetherMissingOutboxDB(t, "aether_task_refund_log_outbox_failure", &Log{})
	LOG_DB = db
	err := RecordTaskBillingLog(RecordTaskBillingLogParams{
		UserId:    17,
		LogType:   LogTypeRefund,
		SourceID:  "task:task-rollback:failure",
		ChannelId: 42,
		ModelName: "gpt-5",
		Quota:     321,
		TokenId:   29,
		Group:     "pro",
	})
	require.Error(t, err)

	var count int64
	require.NoError(t, db.Model(&Log{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestUsageOutboxUsesMainDatabaseWhenLogDatabaseIsSeparate(t *testing.T) {
	previousDB := DB
	previousLogDB := LOG_DB
	previousLogConsumeEnabled := common.LogConsumeEnabled
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
		common.LogConsumeEnabled = previousLogConsumeEnabled
	})

	mainDB, err := gorm.Open(sqlite.Open("file:aether_separate_main_db?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	logDB, err := gorm.Open(sqlite.Open("file:aether_separate_log_db?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, mainDB.AutoMigrate(&User{}, &Token{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	require.NoError(t, logDB.AutoMigrate(&Log{}))
	integration := &AetherIntegration{ChannelID: 91, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, mainDB.Create(integration).Error)
	DB = mainDB
	LOG_DB = logDB
	common.LogConsumeEnabled = true

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Set(common.RequestIdKey, "req_separate_log_db")
	require.NoError(t, RecordConsumeLog(context, 17, RecordConsumeLogParams{
		ChannelId:        42,
		PromptTokens:     100,
		CompletionTokens: 50,
		ModelName:        "gpt-5",
		Quota:            321,
		TokenId:          29,
		Group:            "pro",
	}))
	require.NoError(t, RecordTaskBillingLog(RecordTaskBillingLogParams{
		UserId:    17,
		LogType:   LogTypeRefund,
		SourceID:  "task:task-separate:failure",
		ChannelId: 42,
		ModelName: "gpt-5",
		Quota:     100,
		TokenId:   29,
		Group:     "pro",
	}))

	var events []AetherLedgerEvent
	require.NoError(t, mainDB.Order("id asc").Find(&events).Error)
	require.Len(t, events, 2)
	assert.Equal(t, AetherLedgerEventUsageSettled, events[0].EventType)
	assert.Equal(t, AetherLedgerEventFinancial, events[1].EventType)
	var logCount int64
	require.NoError(t, logDB.Model(&Log{}).Count(&logCount).Error)
	assert.Equal(t, int64(2), logCount)
}

func TestSeparateLogDatabaseFailureReturnsErrorAfterAuthoritativeOutboxCommit(t *testing.T) {
	previousDB := DB
	previousLogDB := LOG_DB
	previousLogConsumeEnabled := common.LogConsumeEnabled
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
		common.LogConsumeEnabled = previousLogConsumeEnabled
	})

	mainDB, err := gorm.Open(sqlite.Open("file:aether_log_failure_main_db?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	logDB, err := gorm.Open(sqlite.Open("file:aether_log_failure_aux_db?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, mainDB.AutoMigrate(&User{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 91, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, mainDB.Create(integration).Error)
	DB = mainDB
	LOG_DB = logDB
	common.LogConsumeEnabled = true
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Set(common.RequestIdKey, "req_aux_log_failure")

	err = RecordConsumeLog(context, 17, RecordConsumeLogParams{ChannelId: 42, ModelName: "gpt-5", Quota: 321, TokenId: 29, Group: "pro"})
	require.Error(t, err)

	var eventCount int64
	require.NoError(t, mainDB.Model(&AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Equal(t, int64(1), eventCount)
}

func TestConsumeLoggingDisabledStillRecordsUsageOutbox(t *testing.T) {
	previousDB := DB
	previousLogDB := LOG_DB
	previousLogConsumeEnabled := common.LogConsumeEnabled
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
		common.LogConsumeEnabled = previousLogConsumeEnabled
	})

	db, err := gorm.Open(sqlite.Open("file:aether_usage_without_log?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Log{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 91, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	DB = db
	LOG_DB = nil
	common.LogConsumeEnabled = false
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Set(common.RequestIdKey, "req_without_aux_log")

	require.NoError(t, RecordConsumeLog(context, 17, RecordConsumeLogParams{
		ChannelId:        42,
		PromptTokens:     100,
		CompletionTokens: 50,
		ModelName:        "gpt-5",
		Quota:            321,
		TokenId:          29,
		Group:            "pro",
	}))

	var logCount int64
	require.NoError(t, db.Model(&Log{}).Count(&logCount).Error)
	assert.Zero(t, logCount)
	var event AetherLedgerEvent
	require.NoError(t, db.First(&event).Error)
	assert.Equal(t, AetherLedgerEventUsageSettled, event.EventType)
}

func TestConsumeLogUsageOutboxDoesNotRequireAuxiliaryLogDatabase(t *testing.T) {
	previousDB := DB
	previousLogDB := LOG_DB
	previousLogConsumeEnabled := common.LogConsumeEnabled
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
		common.LogConsumeEnabled = previousLogConsumeEnabled
	})

	db, err := gorm.Open(sqlite.Open("file:aether_usage_without_auxiliary_log?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 93, InstanceID: "aether-without-auxiliary-log", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	DB = db
	LOG_DB = nil
	common.LogConsumeEnabled = true
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Set(common.RequestIdKey, "req_without_auxiliary_log")

	require.NoError(t, RecordConsumeLog(context, 17, RecordConsumeLogParams{
		ChannelId: 42,
		ModelName: "gpt-5",
		Quota:     321,
		TokenId:   29,
		Group:     "pro",
	}))

	var eventCount int64
	require.NoError(t, db.Model(&AetherLedgerEvent{}).Where("event_type = ?", AetherLedgerEventUsageSettled).Count(&eventCount).Error)
	assert.Equal(t, int64(1), eventCount)
}

func TestAetherMutationIDsDedupeRetriesButNotIndependentChanges(t *testing.T) {
	previousDB := DB
	t.Cleanup(func() {
		DB = previousDB
	})
	db, err := gorm.Open(sqlite.Open("file:aether_mutation_identity?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 91, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	channel := &Channel{Id: 42, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Models: "gpt-5", Group: "pro"}

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := RecordAetherChannelEventTxWithMutationID(tx, channel, "updated", 1_784_073_600, "channel-mutation-1"); err != nil {
			return err
		}
		return RecordAetherChannelEventTxWithMutationID(tx, channel, "updated", 1_784_073_600, "channel-mutation-1")
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RecordAetherChannelEventTxWithMutationID(tx, channel, "updated", 1_784_073_600, "channel-mutation-2")
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := RecordAetherPricingEventTxWithMutationID(tx, "ModelRatio", 1_784_073_600, "pricing-mutation-1"); err != nil {
			return err
		}
		return RecordAetherPricingEventTxWithMutationID(tx, "ModelRatio", 1_784_073_600, "pricing-mutation-1")
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RecordAetherPricingEventTxWithMutationID(tx, "ModelRatio", 1_784_073_600, "pricing-mutation-2")
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := RecordAetherChannelBalanceObservationTxWithMutationID(tx, channel.Id, 12.5, 1_784_073_600, "balance-mutation-1"); err != nil {
			return err
		}
		return RecordAetherChannelBalanceObservationTxWithMutationID(tx, channel.Id, 12.5, 1_784_073_600, "balance-mutation-1")
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RecordAetherChannelBalanceObservationTxWithMutationID(tx, channel.Id, 12.5, 1_784_073_600, "balance-mutation-2")
	}))

	var channelCount int64
	require.NoError(t, db.Model(&AetherLedgerEvent{}).Where("event_type = ?", AetherLedgerEventChannel).Count(&channelCount).Error)
	assert.Equal(t, int64(2), channelCount)
	var pricingCount int64
	require.NoError(t, db.Model(&AetherLedgerEvent{}).Where("event_type = ?", AetherLedgerEventPricingChanged).Count(&pricingCount).Error)
	assert.Equal(t, int64(2), pricingCount)
	var balanceCount int64
	require.NoError(t, db.Model(&AetherLedgerEvent{}).Where("event_type = ?", AetherLedgerEventChannelBalanceObserved).Count(&balanceCount).Error)
	assert.Equal(t, int64(2), balanceCount)
}

func TestTaskRefundSourceIDDeduplicatesFinancialEvent(t *testing.T) {
	previousDB := DB
	previousLogDB := LOG_DB
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
	})
	db, err := gorm.Open(sqlite.Open("file:aether_task_refund_dedupe?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	LOG_DB = db
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Log{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 91, InstanceID: "aether-primary", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	params := RecordTaskBillingLogParams{
		UserId:    17,
		LogType:   LogTypeRefund,
		SourceID:  "task:task-91:failure",
		ChannelId: 42,
		ModelName: "gpt-5",
		Quota:     321,
		TokenId:   29,
		Group:     "pro",
	}

	require.NoError(t, RecordTaskBillingLog(params))
	require.NoError(t, RecordTaskBillingLog(params))

	var eventCount int64
	require.NoError(t, db.Model(&AetherLedgerEvent{}).Where("event_type = ?", AetherLedgerEventFinancial).Count(&eventCount).Error)
	assert.Equal(t, int64(1), eventCount)
	var event AetherLedgerEvent
	require.NoError(t, db.Where("event_type = ?", AetherLedgerEventFinancial).First(&event).Error)
	assert.NotContains(t, event.DedupeKey, params.SourceID)
	assert.Contains(t, event.DedupeKey, ":h_")
}

func TestTaskBillingFinancialOutboxDoesNotRequireLogDatabase(t *testing.T) {
	previousDB := DB
	previousLogDB := LOG_DB
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
	})
	db, err := gorm.Open(sqlite.Open("file:aether_task_financial_without_log_db?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	LOG_DB = nil
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Log{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	integration := &AetherIntegration{ChannelID: 92, InstanceID: "aether-without-log-db", ExecutionMode: AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	require.NoError(t, RecordTaskBillingLog(RecordTaskBillingLogParams{
		UserId:    18,
		LogType:   LogTypeRefund,
		SourceID:  "task:task-without-log-db:failure",
		ChannelId: 42,
		ModelName: "gpt-5",
		Quota:     321,
		TokenId:   30,
		Group:     "pro",
	}))

	var eventCount int64
	require.NoError(t, db.Model(&AetherLedgerEvent{}).Where("event_type = ?", AetherLedgerEventFinancial).Count(&eventCount).Error)
	assert.Equal(t, int64(1), eventCount)
	var logCount int64
	require.NoError(t, db.Model(&Log{}).Count(&logCount).Error)
	assert.Zero(t, logCount)
}
