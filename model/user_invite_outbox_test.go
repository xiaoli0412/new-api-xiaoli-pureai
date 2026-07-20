package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openInviteOutboxDB(t *testing.T, name string, validIntegrationSecrets bool) (*gorm.DB, *User) {
	t.Helper()
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &AetherIntegration{}, &AetherLedgerEvent{}))
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})

	integration := &AetherIntegration{
		ChannelID:      710,
		InstanceID:     "aether-invite-outbox",
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

	inviter := &User{Username: "invite-owner-" + name}
	require.NoError(t, db.Create(inviter).Error)
	return db, inviter
}

func enableInviteRewardsForTest(t *testing.T) {
	t.Helper()
	previousNewUserQuota := common.QuotaForNewUser
	previousInviteeQuota := common.QuotaForInvitee
	previousInviterQuota := common.QuotaForInviter
	common.QuotaForNewUser = 100
	common.QuotaForInvitee = 50
	common.QuotaForInviter = 70
	t.Cleanup(func() {
		common.QuotaForNewUser = previousNewUserQuota
		common.QuotaForInvitee = previousInviteeQuota
		common.QuotaForInviter = previousInviterQuota
	})

	payment := operation_setting.GetPaymentSetting()
	previousPayment := *payment
	payment.ComplianceConfirmed = true
	payment.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	t.Cleanup(func() {
		*payment = previousPayment
	})
}

func assertInviteRewardsAndEvents(t *testing.T, db *gorm.DB, invitee *User, inviterID int) {
	t.Helper()
	var storedInvitee User
	require.NoError(t, db.First(&storedInvitee, invitee.Id).Error)
	assert.Equal(t, 150, storedInvitee.Quota)

	var storedInviter User
	require.NoError(t, db.First(&storedInviter, inviterID).Error)
	assert.Equal(t, 1, storedInviter.AffCount)
	assert.Equal(t, 70, storedInviter.AffQuota)
	assert.Equal(t, 70, storedInviter.AffHistoryQuota)

	var events []AetherLedgerEvent
	require.NoError(t, db.Order("id asc").Find(&events).Error)
	require.Len(t, events, 2)
	serialized := fmt.Sprint(events)
	assert.Contains(t, serialized, `"source_type":"invitee_reward"`)
	assert.Contains(t, serialized, `"source_type":"inviter_referral_reward"`)
	assert.NotContains(t, serialized, storedInvitee.Username)
	assert.NotContains(t, serialized, storedInviter.Username)
}

func TestInsertWritesInviteRewardsAndAetherFinancialEventsAtomically(t *testing.T) {
	enableInviteRewardsForTest(t)
	db, inviter := openInviteOutboxDB(t, "invite_outbox_insert", true)
	invitee := &User{Username: "invitee-register", Password: "password"}

	require.NoError(t, invitee.Insert(inviter.Id))
	assert.Equal(t, 150, invitee.Quota)
	assertInviteRewardsAndEvents(t, db, invitee, inviter.Id)
}

func TestInsertWithTxWritesInviteRewardsBeforeOAuthFinalization(t *testing.T) {
	enableInviteRewardsForTest(t)
	db, inviter := openInviteOutboxDB(t, "invite_outbox_oauth", true)
	invitee := &User{Username: "invitee-oauth", Password: "password"}

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return invitee.InsertWithTx(tx, inviter.Id)
	}))
	invitee.FinalizeOAuthUserCreation(inviter.Id)

	assert.Equal(t, 150, invitee.Quota)
	assertInviteRewardsAndEvents(t, db, invitee, inviter.Id)
}

func TestInsertRollsBackInviteRewardsWhenAetherFinancialOutboxWriteFails(t *testing.T) {
	enableInviteRewardsForTest(t)
	db, inviter := openInviteOutboxDB(t, "invite_outbox_rollback", false)
	invitee := &User{Username: "invitee-rollback", Password: "password"}

	err := invitee.Insert(inviter.Id)
	require.Error(t, err)

	var inviteeCount int64
	require.NoError(t, db.Model(&User{}).Where("username = ?", invitee.Username).Count(&inviteeCount).Error)
	assert.Zero(t, inviteeCount)
	var storedInviter User
	require.NoError(t, db.First(&storedInviter, inviter.Id).Error)
	assert.Zero(t, storedInviter.AffCount)
	assert.Zero(t, storedInviter.AffQuota)
	assert.Zero(t, storedInviter.AffHistoryQuota)
	var eventCount int64
	require.NoError(t, db.Model(&AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Zero(t, eventCount)
}
