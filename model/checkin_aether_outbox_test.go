package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openAetherCheckinDB(t *testing.T, name string, validIntegrationSecrets bool) *gorm.DB {
	t.Helper()
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Checkin{}, &AetherIntegration{}, &AetherLedgerEvent{}))

	integration := &AetherIntegration{
		ChannelID:      111,
		InstanceID:     "aether-checkin",
		ExecutionMode:  AetherExecutionModeDirectChannel,
		Enabled:        true,
		ConfigRevision: 1,
	}
	if validIntegrationSecrets {
		require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	} else {
		integration.ControlSecretEncrypted = "invalid-secret"
		integration.RelaySigningSecretEncrypted = "invalid-secret"
	}
	require.NoError(t, db.Create(integration).Error)

	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	return db
}

func configureCheckinForTest(t *testing.T, quota int) {
	t.Helper()
	setting := operation_setting.GetCheckinSetting()
	previous := *setting
	*setting = operation_setting.CheckinSetting{
		Enabled:  true,
		MinQuota: quota,
		MaxQuota: quota,
	}
	t.Cleanup(func() {
		*setting = previous
	})
}

func TestUserCheckinWritesAetherFinancialEventInBusinessTransaction(t *testing.T) {
	db := openAetherCheckinDB(t, "aether_checkin_outbox_success", true)
	configureCheckinForTest(t, 275)
	user := &User{Username: "checkin-aether-user", Password: "password", Quota: 100}
	require.NoError(t, db.Create(user).Error)

	checkin, err := UserCheckin(user.Id)
	require.NoError(t, err)
	require.NotZero(t, checkin.Id)
	assert.Equal(t, 275, checkin.QuotaAwarded)

	var refreshedUser User
	require.NoError(t, db.First(&refreshedUser, user.Id).Error)
	assert.Equal(t, 375, refreshedUser.Quota)

	var event AetherLedgerEvent
	require.NoError(t, db.Where("instance_id = ?", "aether-checkin").First(&event).Error)
	assert.Equal(t, AetherLedgerEventFinancial, event.EventType)
	var payload aetherFinancialLedgerPayload
	require.NoError(t, common.Unmarshal([]byte(event.Payload), &payload))
	assert.Equal(t, "checkin", payload.SourceType)
	assert.Equal(t, "275", payload.QuotaDelta)
	assert.Equal(t, "checkin", payload.PaymentCategory)
}

func TestUserCheckinRollsBackWhenAetherFinancialOutboxWriteFails(t *testing.T) {
	db := openAetherCheckinDB(t, "aether_checkin_outbox_failure", false)
	configureCheckinForTest(t, 325)
	user := &User{Username: "checkin-aether-rollback", Password: "password", Quota: 100}
	require.NoError(t, db.Create(user).Error)

	_, err := UserCheckin(user.Id)
	require.Error(t, err)

	var refreshedUser User
	require.NoError(t, db.First(&refreshedUser, user.Id).Error)
	assert.Equal(t, 100, refreshedUser.Quota)
	var checkinCount int64
	require.NoError(t, db.Model(&Checkin{}).Count(&checkinCount).Error)
	assert.Zero(t, checkinCount)
	var eventCount int64
	require.NoError(t, db.Model(&AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Zero(t, eventCount)
}

func TestUserCheckinDuplicateDoesNotCreditOrEnqueueTwice(t *testing.T) {
	db := openAetherCheckinDB(t, "aether_checkin_duplicate", true)
	configureCheckinForTest(t, 450)
	user := &User{Username: "checkin-aether-duplicate", Password: "password", Quota: 100}
	require.NoError(t, db.Create(user).Error)

	_, err := UserCheckin(user.Id)
	require.NoError(t, err)
	_, err = UserCheckin(user.Id)
	require.Error(t, err)

	var refreshedUser User
	require.NoError(t, db.First(&refreshedUser, user.Id).Error)
	assert.Equal(t, 550, refreshedUser.Quota)
	var checkinCount int64
	require.NoError(t, db.Model(&Checkin{}).Where("user_id = ?", user.Id).Count(&checkinCount).Error)
	assert.Equal(t, int64(1), checkinCount)
	var eventCount int64
	require.NoError(t, db.Model(&AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Equal(t, int64(1), eventCount)
}
