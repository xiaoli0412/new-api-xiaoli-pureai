package model

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openAdminQuotaOutboxDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &AetherIntegration{}, &AetherLedgerEvent{}))

	integration := &AetherIntegration{
		ChannelID:      501,
		InstanceID:     "aether-admin-quota",
		ExecutionMode:  AetherExecutionModeDirectChannel,
		Enabled:        true,
		ConfigRevision: 1,
	}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	return db
}

func TestUpdateUserQuotaByAdminWithMutationIDRecordsAnonymousFinancialEvents(t *testing.T) {
	previousDB := DB
	t.Cleanup(func() {
		DB = previousDB
	})

	tests := []struct {
		mode          string
		startingQuota int
		value         int
		wantQuota     int
		wantDelta     int
	}{
		{mode: "add", startingQuota: 100, value: 40, wantQuota: 140, wantDelta: 40},
		{mode: "subtract", startingQuota: 100, value: 40, wantQuota: 60, wantDelta: -40},
		{mode: "override", startingQuota: 100, value: 40, wantQuota: 40, wantDelta: -60},
	}

	for index, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			db := openAdminQuotaOutboxDB(t, fmt.Sprintf("admin_quota_outbox_success_%d", index))
			DB = db
			user := &User{Id: index + 701, Username: "sensitive-user", Quota: test.startingQuota}
			require.NoError(t, db.Create(user).Error)

			mutationID := "admin-quota-mutation-" + test.mode
			oldQuota, err := UpdateUserQuotaByAdminWithMutationID(user.Id, test.mode, test.value, mutationID)
			require.NoError(t, err)
			assert.Equal(t, test.startingQuota, oldQuota)

			var stored User
			require.NoError(t, db.First(&stored, user.Id).Error)
			assert.Equal(t, test.wantQuota, stored.Quota)

			var event AetherLedgerEvent
			require.NoError(t, db.First(&event).Error)
			assert.Equal(t, AetherLedgerEventFinancial, event.EventType)
			assert.Contains(t, event.Payload, `"source_type":"admin_quota_adjustment"`)
			assert.Contains(t, event.Payload, fmt.Sprintf(`"quota_delta":"%d"`, test.wantDelta))
			assert.Contains(t, event.Payload, `"subject_id":"u_`)
			assert.Contains(t, event.DedupeKey, ":h_")
			assert.NotContains(t, event.Payload, user.Username)
			assert.NotContains(t, event.Payload, mutationID)
			assert.NotContains(t, event.DedupeKey, user.Username)
			assert.NotContains(t, event.DedupeKey, mutationID)
		})
	}
}

func TestUpdateUserQuotaByAdminWithMutationIDRollsBackWhenOutboxWriteFails(t *testing.T) {
	previousDB := DB
	t.Cleanup(func() {
		DB = previousDB
	})

	tests := []struct {
		mode          string
		startingQuota int
		value         int
	}{
		{mode: "add", startingQuota: 100, value: 40},
		{mode: "subtract", startingQuota: 100, value: 40},
		{mode: "override", startingQuota: 100, value: 40},
	}

	for index, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			db := openAdminQuotaOutboxDB(t, fmt.Sprintf("admin_quota_outbox_failure_%d", index))
			DB = db
			user := &User{Id: index + 801, Username: "sensitive-user", Quota: test.startingQuota}
			require.NoError(t, db.Create(user).Error)
			require.NoError(t, db.Migrator().DropTable(&AetherLedgerEvent{}))

			_, err := UpdateUserQuotaByAdminWithMutationID(user.Id, test.mode, test.value, "admin-quota-failure-"+test.mode)
			require.Error(t, err)

			var stored User
			require.NoError(t, db.First(&stored, user.Id).Error)
			assert.Equal(t, test.startingQuota, stored.Quota)
		})
	}
}
