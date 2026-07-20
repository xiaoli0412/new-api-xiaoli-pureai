package model

import (
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAetherPendingCredentialRotationTest(t *testing.T, databaseName string) (*gorm.DB, *AetherIntegration) {
	t.Helper()
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:"+databaseName+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&AetherIntegration{}, &AetherIntegrationPendingCredentialRotation{}))

	integration := &AetherIntegration{
		ChannelID:      901,
		InstanceID:     "aether-pending-rotation",
		ExecutionMode:  AetherExecutionModeDirectChannel,
		Enabled:        true,
		ConfigRevision: 1,
	}
	require.NoError(t, integration.SetSecrets("control-v1", "relay-v1"))
	require.NoError(t, db.Create(integration).Error)
	return db, integration
}

func pendingCredentialRotationRequest(rotationID string, controlSecret string, relaySecret string) AetherIntegrationPendingCredentialRotationRequest {
	return AetherIntegrationPendingCredentialRotationRequest{
		RotationID:          rotationID,
		ControlSecret:       controlSecret,
		RelaySigningSecret:  relaySecret,
		TransitionExpiresAt: time.Now().UTC().Add(time.Minute),
	}
}

func TestPrepareAetherIntegrationPendingCredentialRotationKeepsActiveSecretsUntouched(t *testing.T) {
	db, integration := setupAetherPendingCredentialRotationTest(t, "aether_pending_rotation_active_secrets")
	request := pendingCredentialRotationRequest("rotation-v2", "control-v2", "relay-v2")
	requestJSON, err := common.Marshal(request)
	require.NoError(t, err)
	assert.NotContains(t, string(requestJSON), "control-v2")
	assert.NotContains(t, string(requestJSON), "relay-v2")
	assert.NotContains(t, fmt.Sprint(request), "control-v2")
	assert.NotContains(t, fmt.Sprint(request), "relay-v2")
	assert.NotContains(t, fmt.Sprintf("%#v", request), "control-v2")
	assert.NotContains(t, fmt.Sprintf("%#v", request), "relay-v2")
	assert.Contains(t, string(requestJSON), `"revoke_previous":false`)

	pending, conflict, err := PrepareAetherIntegrationPendingCredentialRotation(
		integration,
		1,
		request,
	)
	require.NoError(t, err)
	require.Nil(t, conflict)
	require.NotNil(t, pending)
	assert.Equal(t, int64(1), pending.BaseConfigRevision)
	assert.Equal(t, int64(2), pending.ReservedConfigRevision)
	assert.Len(t, pending.PayloadSHA256, 64)
	assert.NotContains(t, pending.ControlSecretEncrypted, "control-v2")
	assert.NotContains(t, pending.RelaySigningSecretEncrypted, "relay-v2")

	serialized, err := common.Marshal(pending)
	require.NoError(t, err)
	assert.NotContains(t, string(serialized), "control-v2")
	assert.NotContains(t, string(serialized), "relay-v2")
	assert.NotContains(t, string(serialized), pending.ControlSecretEncrypted)
	assert.NotContains(t, string(serialized), pending.RelaySigningSecretEncrypted)
	assert.NotContains(t, fmt.Sprint(pending), "control-v2")
	assert.NotContains(t, fmt.Sprint(pending), "relay-v2")
	assert.NotContains(t, fmt.Sprint(pending), pending.ControlSecretEncrypted)
	assert.NotContains(t, fmt.Sprint(pending), pending.RelaySigningSecretEncrypted)
	assert.NotContains(t, fmt.Sprintf("%#v", pending), "control-v2")
	assert.NotContains(t, fmt.Sprintf("%#v", pending), "relay-v2")
	assert.NotContains(t, fmt.Sprintf("%#v", pending), pending.ControlSecretEncrypted)
	assert.NotContains(t, fmt.Sprintf("%#v", pending), pending.RelaySigningSecretEncrypted)

	var persistedIntegration AetherIntegration
	require.NoError(t, db.First(&persistedIntegration, integration.Id).Error)
	controlSecret, relaySecret, err := persistedIntegration.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-v1", controlSecret)
	assert.Equal(t, "relay-v1", relaySecret)
	assert.Empty(t, persistedIntegration.PreviousControlSecretEncrypted)
	assert.Empty(t, persistedIntegration.PreviousRelaySigningSecretEncrypted)
	assert.Zero(t, persistedIntegration.TransitionSecretsExpireAt)
	assert.Equal(t, int64(2), persistedIntegration.ConfigRevision)

	var persistedPending AetherIntegrationPendingCredentialRotation
	require.NoError(t, db.First(&persistedPending, pending.Id).Error)
	pendingControlSecret, pendingRelaySecret, err := persistedPending.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-v2", pendingControlSecret)
	assert.Equal(t, "relay-v2", pendingRelaySecret)
}

func TestPrepareAetherIntegrationPendingCredentialRotationIsIdempotentAndRejectsPayloadReuse(t *testing.T) {
	db, integration := setupAetherPendingCredentialRotationTest(t, "aether_pending_rotation_idempotency")
	request := pendingCredentialRotationRequest("rotation-v2", "control-v2", "relay-v2")
	revokeAndReplace := request
	revokeAndReplace.RevokePrevious = true
	pending, conflict, err := PrepareAetherIntegrationPendingCredentialRotation(integration, 1, revokeAndReplace)
	require.ErrorContains(t, err, "cannot revoke and replace secrets together")
	require.Nil(t, pending)
	require.Nil(t, conflict)

	first, conflict, err := PrepareAetherIntegrationPendingCredentialRotation(integration, 1, request)
	require.NoError(t, err)
	require.Nil(t, conflict)
	require.NotNil(t, first)

	retry, conflict, err := PrepareAetherIntegrationPendingCredentialRotation(integration, 1, request)
	require.NoError(t, err)
	require.Nil(t, conflict)
	require.NotNil(t, retry)
	assert.Equal(t, first.Id, retry.Id)
	assert.Equal(t, first.PayloadSHA256, retry.PayloadSHA256)

	_, conflict, err = PrepareAetherIntegrationPendingCredentialRotation(
		integration,
		1,
		pendingCredentialRotationRequest("rotation-v2", "control-v3", "relay-v3"),
	)
	require.ErrorContains(t, err, "already bound to a different payload")
	require.Nil(t, conflict)

	var pendingCount int64
	require.NoError(t, db.Model(&AetherIntegrationPendingCredentialRotation{}).Count(&pendingCount).Error)
	assert.Equal(t, int64(1), pendingCount)
	var persistedIntegration AetherIntegration
	require.NoError(t, db.First(&persistedIntegration, integration.Id).Error)
	assert.Equal(t, int64(2), persistedIntegration.ConfigRevision)
}

func TestAetherPendingCredentialRotationPayloadSHA256BindsRevokePrevious(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Minute).Unix()
	withoutRevoke := aetherPendingCredentialRotationPayloadSHA256(
		901,
		"aether-pending-rotation",
		1,
		"rotation-v2",
		"control-v2",
		"relay-v2",
		expiresAt,
		false,
	)
	withRevoke := aetherPendingCredentialRotationPayloadSHA256(
		901,
		"aether-pending-rotation",
		1,
		"rotation-v2",
		"control-v2",
		"relay-v2",
		expiresAt,
		true,
	)

	assert.Len(t, withoutRevoke, 64)
	assert.Len(t, withRevoke, 64)
	assert.NotEqual(t, withoutRevoke, withRevoke)
}

func TestPrepareAetherIntegrationPendingCredentialRotationRejectsStaleRevision(t *testing.T) {
	db, integration := setupAetherPendingCredentialRotationTest(t, "aether_pending_rotation_stale_revision")

	updated, conflict, err := UpdateAetherIntegrationSharedConfig(
		integration,
		1,
		AetherIntegrationSharedConfig{
			RouteProfile:  "balanced",
			ExecutionMode: AetherExecutionModeDirectChannel,
			Enabled:       true,
		},
	)
	require.NoError(t, err)
	require.Nil(t, conflict)
	require.Equal(t, int64(2), updated.ConfigRevision)

	pending, conflict, err := PrepareAetherIntegrationPendingCredentialRotation(
		integration,
		1,
		pendingCredentialRotationRequest("rotation-v2", "control-v2", "relay-v2"),
	)
	require.NoError(t, err)
	require.Nil(t, pending)
	require.NotNil(t, conflict)
	assert.Equal(t, int64(2), conflict.CurrentRevision)

	var pendingCount int64
	require.NoError(t, db.Model(&AetherIntegrationPendingCredentialRotation{}).Count(&pendingCount).Error)
	assert.Zero(t, pendingCount)
}

func TestPrepareAetherIntegrationPendingCredentialRotationRollsBackCASWhenPendingInsertFails(t *testing.T) {
	db, integration := setupAetherPendingCredentialRotationTest(t, "aether_pending_rotation_transaction_rollback")
	require.NoError(t, db.Exec(`
		CREATE TRIGGER fail_aether_pending_rotation_insert
		BEFORE INSERT ON aether_integration_pending_credential_rotations
		BEGIN
			SELECT RAISE(ABORT, 'forced pending rotation insert failure');
		END;
	`).Error)

	pending, conflict, err := PrepareAetherIntegrationPendingCredentialRotation(
		integration,
		1,
		pendingCredentialRotationRequest("rotation-v2", "control-v2", "relay-v2"),
	)
	require.ErrorContains(t, err, "forced pending rotation insert failure")
	require.Nil(t, pending)
	require.Nil(t, conflict)

	var persistedIntegration AetherIntegration
	require.NoError(t, db.First(&persistedIntegration, integration.Id).Error)
	assert.Equal(t, int64(1), persistedIntegration.ConfigRevision)
	controlSecret, relaySecret, err := persistedIntegration.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-v1", controlSecret)
	assert.Equal(t, "relay-v1", relaySecret)
}

func TestPromoteAetherIntegrationPendingCredentialRotationAtomicallyActivatesAcknowledgedSecrets(t *testing.T) {
	db, integration := setupAetherPendingCredentialRotationTest(t, "aether_pending_rotation_promote")
	request := pendingCredentialRotationRequest("rotation-v2", "control-v2", "relay-v2")
	pending, conflict, err := PrepareAetherIntegrationPendingCredentialRotation(integration, 1, request)
	require.NoError(t, err)
	require.Nil(t, conflict)
	require.NotNil(t, pending)

	promoted, err := PromoteAetherIntegrationPendingCredentialRotation(
		integration.Id,
		"rotation-v2",
		8,
	)
	require.NoError(t, err)
	require.NotNil(t, promoted)
	assert.Equal(t, int64(2), promoted.ConfigRevision)
	assert.Equal(t, int64(8), promoted.RemoteConfigRevision)
	assert.Equal(t, request.TransitionExpiresAt.UTC().Unix(), promoted.TransitionSecretsExpireAt)
	controlSecrets, err := promoted.ActiveControlSecrets(time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, []string{"control-v2", "control-v1"}, controlSecrets)
	relaySecrets, err := promoted.ActiveRelaySigningSecrets(time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, []string{"relay-v2", "relay-v1"}, relaySecrets)

	var pendingCount int64
	require.NoError(t, db.Model(&AetherIntegrationPendingCredentialRotation{}).Count(&pendingCount).Error)
	assert.Zero(t, pendingCount)
	var persisted AetherIntegration
	require.NoError(t, db.First(&persisted, integration.Id).Error)
	controlSecret, relaySecret, err := persisted.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-v2", controlSecret)
	assert.Equal(t, "relay-v2", relaySecret)
	assert.NotContains(t, persisted.ControlSecretEncrypted, "control-v2")
	assert.NotContains(t, persisted.PreviousControlSecretEncrypted, "control-v1")
}

func TestCancelAetherIntegrationPendingCredentialRotationRestoresOnlyItsReservedRevision(t *testing.T) {
	db, integration := setupAetherPendingCredentialRotationTest(t, "aether_pending_rotation_cancel")
	pending, conflict, err := PrepareAetherIntegrationPendingCredentialRotation(
		integration,
		1,
		pendingCredentialRotationRequest("rotation-v2", "control-v2", "relay-v2"),
	)
	require.NoError(t, err)
	require.Nil(t, conflict)
	require.NotNil(t, pending)

	_, err = CancelAetherIntegrationPendingCredentialRotation(integration.Id, "rotation-v2", 3)
	require.ErrorContains(t, err, "reserved revision")
	var stillPending AetherIntegrationPendingCredentialRotation
	require.NoError(t, db.First(&stillPending, pending.Id).Error)

	canceled, err := CancelAetherIntegrationPendingCredentialRotation(integration.Id, "rotation-v2", 2)
	require.NoError(t, err)
	require.NotNil(t, canceled)
	assert.Equal(t, int64(1), canceled.ConfigRevision)
	controlSecret, relaySecret, err := canceled.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-v1", controlSecret)
	assert.Equal(t, "relay-v1", relaySecret)
	var pendingCount int64
	require.NoError(t, db.Model(&AetherIntegrationPendingCredentialRotation{}).Count(&pendingCount).Error)
	assert.Zero(t, pendingCount)
}

func TestPromoteAetherIntegrationPendingCredentialRotationRejectsStaleReservedRevision(t *testing.T) {
	db, integration := setupAetherPendingCredentialRotationTest(t, "aether_pending_rotation_stale_promotion")
	pending, conflict, err := PrepareAetherIntegrationPendingCredentialRotation(
		integration,
		1,
		pendingCredentialRotationRequest("rotation-v2", "control-v2", "relay-v2"),
	)
	require.NoError(t, err)
	require.Nil(t, conflict)
	require.NotNil(t, pending)
	require.NoError(t, db.Model(&AetherIntegration{}).Where("id = ?", integration.Id).Update("config_revision", int64(3)).Error)

	promoted, err := PromoteAetherIntegrationPendingCredentialRotation(integration.Id, "rotation-v2", 8)
	require.ErrorContains(t, err, "reserved revision")
	require.Nil(t, promoted)
	var stillPending AetherIntegrationPendingCredentialRotation
	require.NoError(t, db.First(&stillPending, pending.Id).Error)
	var stored AetherIntegration
	require.NoError(t, db.First(&stored, integration.Id).Error)
	controlSecret, relaySecret, err := stored.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-v1", controlSecret)
	assert.Equal(t, "relay-v1", relaySecret)
}

func TestAetherIntegrationRejectsLocalUpdatesWhileCredentialRotationIsPending(t *testing.T) {
	_, integration := setupAetherPendingCredentialRotationTest(t, "aether_pending_rotation_shared_config_gate")
	pending, conflict, err := PrepareAetherIntegrationPendingCredentialRotation(
		integration,
		1,
		pendingCredentialRotationRequest("rotation-v2", "control-v2", "relay-v2"),
	)
	require.NoError(t, err)
	require.Nil(t, conflict)
	require.NotNil(t, pending)

	updated, conflict, err := UpdateAetherIntegrationSharedConfig(
		integration,
		2,
		AetherIntegrationSharedConfig{
			RouteProfile:  "changed",
			ExecutionMode: AetherExecutionModeDirectChannel,
			Enabled:       false,
		},
	)
	require.ErrorContains(t, err, "credential rotation is pending")
	require.Nil(t, updated)
	require.Nil(t, conflict)

	updated, conflict, err = RevokeAetherIntegrationTransitionSecrets(integration, 2)
	require.ErrorContains(t, err, "credential rotation is pending")
	require.Nil(t, updated)
	require.Nil(t, conflict)
}
