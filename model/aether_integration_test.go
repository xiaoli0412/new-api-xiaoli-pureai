package model

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAetherIntegrationEncryptsSecretsAndDetectsRevisionConflict(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_integration_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&AetherIntegration{}, &AetherIntegrationPendingCredentialRotation{}))

	integration := &AetherIntegration{
		ChannelID:      101,
		InstanceID:     "aether-primary",
		RouteProfile:   "balanced",
		ExecutionMode:  AetherExecutionModeDirectChannel,
		Enabled:        true,
		ConfigRevision: 1,
	}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	var stored AetherIntegration
	require.NoError(t, db.First(&stored, integration.Id).Error)
	assert.NotContains(t, stored.ControlSecretEncrypted, "control-secret")
	assert.NotContains(t, stored.RelaySigningSecretEncrypted, "relay-signing-secret")
	controlSecret, signingSecret, err := stored.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-secret", controlSecret)
	assert.Equal(t, "relay-signing-secret", signingSecret)

	updated, conflict, err := UpdateAetherIntegrationSharedConfig(&stored, 1, AetherIntegrationSharedConfig{
		RouteProfile:  "low-cost",
		ExecutionMode: AetherExecutionModeDirectChannel,
		Enabled:       true,
	})
	require.NoError(t, err)
	require.Nil(t, conflict)
	assert.Equal(t, int64(2), updated.ConfigRevision)

	_, conflict, err = UpdateAetherIntegrationSharedConfig(updated, 1, AetherIntegrationSharedConfig{
		RouteProfile:  "premium",
		ExecutionMode: AetherExecutionModeDirectChannel,
		Enabled:       true,
	})
	require.NoError(t, err)
	require.NotNil(t, conflict)
	assert.Equal(t, int64(2), conflict.CurrentRevision)
}

func TestAetherIntegrationRejectsFutureExecutionModes(t *testing.T) {
	assert.Error(t, ValidateAetherExecutionMode(AetherExecutionModeParallelShadow))
	assert.Error(t, ValidateAetherExecutionMode(AetherExecutionModeAetherDecision))
	assert.NoError(t, ValidateAetherExecutionMode(AetherExecutionModeDirectChannel))
	assert.NoError(t, ValidateAetherExecutionMode(AetherExecutionModeDisabled))

	originalSecret := common.CryptoSecret
	common.CryptoSecret = "aether-integration-test-crypto-secret"
	t.Cleanup(func() {
		common.CryptoSecret = originalSecret
	})
}

func TestAetherIntegrationRejectsCredentialDomainReuse(t *testing.T) {
	integration := &AetherIntegration{}
	require.ErrorContains(t, integration.SetSecrets("shared-secret", "shared-secret"), "must differ")

	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_integration_secret_domain_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&AetherIntegration{}, &AetherIntegrationPendingCredentialRotation{}))

	integration = &AetherIntegration{
		ChannelID:      106,
		InstanceID:     "aether-secret-domain",
		ExecutionMode:  AetherExecutionModeDirectChannel,
		Enabled:        true,
		ConfigRevision: 1,
	}
	require.NoError(t, integration.SetSecrets("control-v1", "relay-v1"))
	require.NoError(t, db.Create(integration).Error)

	updated, conflict, err := UpdateAetherIntegrationSharedConfigWithSecretRotation(
		integration,
		1,
		AetherIntegrationSharedConfig{
			ExecutionMode: AetherExecutionModeDirectChannel,
			Enabled:       true,
		},
		&AetherIntegrationSecretRotation{
			ControlSecret:       "relay-v1",
			RelaySigningSecret:  "relay-v2",
			TransitionExpiresAt: time.Now().UTC().Add(time.Minute),
		},
	)
	require.Nil(t, updated)
	require.Nil(t, conflict)
	require.ErrorContains(t, err, "must not overlap")
}

func TestAetherIntegrationRotatesCredentialsWithCASAndCanRevokeTransitionKey(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_integration_rotation_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&AetherIntegration{}, &AetherIntegrationPendingCredentialRotation{}))

	integration := &AetherIntegration{
		ChannelID:      102,
		InstanceID:     "aether-rotation",
		RouteProfile:   "balanced",
		ExecutionMode:  AetherExecutionModeDirectChannel,
		Enabled:        true,
		ConfigRevision: 1,
	}
	require.NoError(t, integration.SetSecrets("control-v1", "relay-v1"))
	require.NoError(t, db.Create(integration).Error)

	transitionUntil := time.Now().UTC().Add(time.Minute)
	updated, conflict, err := UpdateAetherIntegrationSharedConfigWithSecretRotation(
		integration,
		1,
		AetherIntegrationSharedConfig{
			RouteProfile:  "low-cost",
			ExecutionMode: AetherExecutionModeDirectChannel,
			Enabled:       true,
		},
		&AetherIntegrationSecretRotation{
			ControlSecret:       "control-v2",
			RelaySigningSecret:  "relay-v2",
			TransitionExpiresAt: transitionUntil,
		},
	)
	require.NoError(t, err)
	require.Nil(t, conflict)
	require.Equal(t, int64(2), updated.ConfigRevision)
	controlSecrets, err := updated.ActiveControlSecrets(time.Now().UTC())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"control-v2", "control-v1"}, controlSecrets)
	relaySecrets, err := updated.ActiveRelaySigningSecrets(time.Now().UTC())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"relay-v2", "relay-v1"}, relaySecrets)

	_, conflict, err = UpdateAetherIntegrationSharedConfigWithSecretRotation(
		integration,
		1,
		AetherIntegrationSharedConfig{
			RouteProfile:  "premium",
			ExecutionMode: AetherExecutionModeDirectChannel,
			Enabled:       true,
		},
		&AetherIntegrationSecretRotation{
			ControlSecret:      "control-stale",
			RelaySigningSecret: "relay-stale",
		},
	)
	require.NoError(t, err)
	require.NotNil(t, conflict)
	require.Equal(t, int64(2), conflict.CurrentRevision)

	var stored AetherIntegration
	require.NoError(t, db.First(&stored, integration.Id).Error)
	currentControlSecret, currentRelaySecret, err := stored.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-v2", currentControlSecret)
	assert.Equal(t, "relay-v2", currentRelaySecret)

	revoked, conflict, err := RevokeAetherIntegrationTransitionSecrets(&stored, 2)
	require.NoError(t, err)
	require.Nil(t, conflict)
	require.Equal(t, int64(3), revoked.ConfigRevision)
	controlSecrets, err = revoked.ActiveControlSecrets(time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, []string{"control-v2"}, controlSecrets)
	relaySecrets, err = revoked.ActiveRelaySigningSecrets(time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, []string{"relay-v2"}, relaySecrets)
}

func TestAetherIntegrationExpiresTransitionSecretsFromPrimaryStore(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_integration_expired_transition_model_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&AetherIntegration{}))

	previousControlSecret, err := common.EncryptAetherSecret("control-v1")
	require.NoError(t, err)
	previousRelaySigningSecret, err := common.EncryptAetherSecret("relay-v1")
	require.NoError(t, err)
	integration := &AetherIntegration{
		ChannelID:                           103,
		InstanceID:                          "aether-expired-transition",
		ExecutionMode:                       AetherExecutionModeDirectChannel,
		Enabled:                             true,
		ConfigRevision:                      2,
		PreviousControlSecretEncrypted:      previousControlSecret,
		PreviousRelaySigningSecretEncrypted: previousRelaySigningSecret,
		TransitionSecretsExpireAt:           time.Now().UTC().Add(-time.Minute).Unix(),
	}
	require.NoError(t, integration.SetSecrets("control-v2", "relay-v2"))
	integration.PreviousControlSecretEncrypted = previousControlSecret
	integration.PreviousRelaySigningSecretEncrypted = previousRelaySigningSecret
	integration.TransitionSecretsExpireAt = time.Now().UTC().Add(-time.Minute).Unix()
	require.NoError(t, db.Create(integration).Error)

	var stored AetherIntegration
	require.NoError(t, db.First(&stored, integration.Id).Error)
	controlSecrets, err := stored.ActiveControlSecrets(time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, []string{"control-v2"}, controlSecrets)

	var persisted AetherIntegration
	require.NoError(t, db.First(&persisted, integration.Id).Error)
	assert.Empty(t, persisted.PreviousControlSecretEncrypted)
	assert.Empty(t, persisted.PreviousRelaySigningSecretEncrypted)
	assert.Zero(t, persisted.TransitionSecretsExpireAt)
}

func TestAetherIntegrationExpiredCleanupRefreshesNewerTransition(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:aether_integration_expired_cleanup_refresh_model_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&AetherIntegration{}))

	previousControlSecret, err := common.EncryptAetherSecret("control-v2")
	require.NoError(t, err)
	previousRelaySigningSecret, err := common.EncryptAetherSecret("relay-v2")
	require.NoError(t, err)
	current := &AetherIntegration{
		ChannelID:                           105,
		InstanceID:                          "aether-newer-transition",
		ExecutionMode:                       AetherExecutionModeDirectChannel,
		Enabled:                             true,
		ConfigRevision:                      3,
		PreviousControlSecretEncrypted:      previousControlSecret,
		PreviousRelaySigningSecretEncrypted: previousRelaySigningSecret,
		TransitionSecretsExpireAt:           time.Now().UTC().Add(time.Minute).Unix(),
	}
	require.NoError(t, current.SetSecrets("control-v3", "relay-v3"))
	current.PreviousControlSecretEncrypted = previousControlSecret
	current.PreviousRelaySigningSecretEncrypted = previousRelaySigningSecret
	current.TransitionSecretsExpireAt = time.Now().UTC().Add(time.Minute).Unix()
	require.NoError(t, db.Create(current).Error)

	stalePreviousControlSecret, err := common.EncryptAetherSecret("control-v1")
	require.NoError(t, err)
	stalePreviousRelaySigningSecret, err := common.EncryptAetherSecret("relay-v1")
	require.NoError(t, err)
	stale := &AetherIntegration{
		Id:                                  current.Id,
		PreviousControlSecretEncrypted:      stalePreviousControlSecret,
		PreviousRelaySigningSecretEncrypted: stalePreviousRelaySigningSecret,
		TransitionSecretsExpireAt:           time.Now().UTC().Add(-time.Minute).Unix(),
	}
	require.NoError(t, stale.SetSecrets("control-v2", "relay-v2"))
	stale.PreviousControlSecretEncrypted = stalePreviousControlSecret
	stale.PreviousRelaySigningSecretEncrypted = stalePreviousRelaySigningSecret
	stale.TransitionSecretsExpireAt = time.Now().UTC().Add(-time.Minute).Unix()

	controlSecrets, err := stale.ActiveControlSecrets(time.Now().UTC())

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"control-v3", "control-v2"}, controlSecrets)
	var persisted AetherIntegration
	require.NoError(t, db.First(&persisted, current.Id).Error)
	assert.Equal(t, current.PreviousControlSecretEncrypted, persisted.PreviousControlSecretEncrypted)
	assert.Equal(t, current.PreviousRelaySigningSecretEncrypted, persisted.PreviousRelaySigningSecretEncrypted)
	assert.Equal(t, current.TransitionSecretsExpireAt, persisted.TransitionSecretsExpireAt)
}

func TestAetherIntegrationCASConflictReturnsLatestConfigurationAfterStaleSnapshot(t *testing.T) {
	previousDB := DB
	state := &aetherCASConflictDriverState{}
	aetherCASConflictState = state
	sqlDB, err := sql.Open("aether-cas-conflict", "")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{
		Conn:                      sqlDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		aetherCASConflictState = nil
		require.NoError(t, sqlDB.Close())
	})

	updated, conflict, err := UpdateAetherIntegrationSharedConfig(
		&AetherIntegration{Id: 1},
		1,
		AetherIntegrationSharedConfig{
			RouteProfile:      "requested",
			ExecutionMode:     AetherExecutionModeDirectChannel,
			Enabled:           true,
			CapabilityVersion: "requested-v1",
		},
	)

	require.NoError(t, err)
	require.Nil(t, updated)
	require.NotNil(t, conflict)
	assert.Equal(t, int64(2), conflict.CurrentRevision)
	assert.Equal(t, AetherIntegrationSharedConfig{
		RouteProfile:      "latest",
		ExecutionMode:     AetherExecutionModeDirectChannel,
		Enabled:           true,
		CapabilityVersion: "latest-v2",
	}, conflict.Current)
	assert.Equal(t, map[string]AetherIntegrationConfigDiff{
		"route_profile": {
			Requested: "requested",
			Current:   "latest",
		},
		"capability_version": {
			Requested: "requested-v1",
			Current:   "latest-v2",
		},
	}, conflict.Diff)
}

const aetherCASConflictDriverName = "aether-cas-conflict"

var (
	registerAetherCASConflictDriver sync.Once
	aetherCASConflictState          *aetherCASConflictDriverState
)

type aetherCASConflictDriverState struct {
	mu            sync.Mutex
	inTransaction bool
}

type aetherCASConflictDriver struct{}

func (aetherCASConflictDriver) Open(string) (driver.Conn, error) {
	return &aetherCASConflictConn{state: aetherCASConflictState}, nil
}

type aetherCASConflictConn struct {
	state *aetherCASConflictDriverState
}

func (conn *aetherCASConflictConn) Prepare(query string) (driver.Stmt, error) {
	return &aetherCASConflictStmt{conn: conn, query: query}, nil
}

func (conn *aetherCASConflictConn) Close() error {
	return nil
}

func (conn *aetherCASConflictConn) Begin() (driver.Tx, error) {
	return conn.begin()
}

func (conn *aetherCASConflictConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return conn.begin()
}

func (conn *aetherCASConflictConn) Ping(context.Context) error {
	return nil
}

func (conn *aetherCASConflictConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	return conn.exec(query)
}

func (conn *aetherCASConflictConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	return conn.query(query)
}

func (conn *aetherCASConflictConn) begin() (driver.Tx, error) {
	conn.state.mu.Lock()
	conn.state.inTransaction = true
	conn.state.mu.Unlock()
	return &aetherCASConflictTx{state: conn.state}, nil
}

func (conn *aetherCASConflictConn) exec(query string) (driver.Result, error) {
	if strings.Contains(strings.ToUpper(query), "UPDATE") {
		return driver.RowsAffected(0), nil
	}
	return driver.RowsAffected(1), nil
}

func (conn *aetherCASConflictConn) query(query string) (driver.Rows, error) {
	conn.state.mu.Lock()
	defer conn.state.mu.Unlock()
	latest := !conn.state.inTransaction || strings.Contains(strings.ToUpper(query), "FOR UPDATE")
	if latest {
		return newAetherCASConflictRows(aetherCASConflictLatestRow()), nil
	}
	return newAetherCASConflictRows(aetherCASConflictStaleRow()), nil
}

type aetherCASConflictTx struct {
	state *aetherCASConflictDriverState
}

func (tx *aetherCASConflictTx) Commit() error {
	tx.complete()
	return nil
}

func (tx *aetherCASConflictTx) Rollback() error {
	tx.complete()
	return nil
}

func (tx *aetherCASConflictTx) complete() {
	tx.state.mu.Lock()
	tx.state.inTransaction = false
	tx.state.mu.Unlock()
}

type aetherCASConflictStmt struct {
	conn  *aetherCASConflictConn
	query string
}

func (stmt *aetherCASConflictStmt) Close() error {
	return nil
}

func (stmt *aetherCASConflictStmt) NumInput() int {
	return -1
}

func (stmt *aetherCASConflictStmt) Exec([]driver.Value) (driver.Result, error) {
	return stmt.conn.exec(stmt.query)
}

func (stmt *aetherCASConflictStmt) Query([]driver.Value) (driver.Rows, error) {
	return stmt.conn.query(stmt.query)
}

type aetherCASConflictRows struct {
	values [][]driver.Value
	index  int
}

func newAetherCASConflictRows(value []driver.Value) *aetherCASConflictRows {
	return &aetherCASConflictRows{values: [][]driver.Value{value}}
}

func (rows *aetherCASConflictRows) Columns() []string {
	return []string{
		"id",
		"channel_id",
		"instance_id",
		"route_profile",
		"execution_mode",
		"capability_version",
		"enabled",
		"config_revision",
		"remote_config_revision",
		"control_secret_encrypted",
		"relay_signing_secret_encrypted",
		"previous_control_secret_encrypted",
		"previous_relay_signing_secret_encrypted",
		"transition_secrets_expire_at",
		"last_sync_time",
		"last_health_time",
		"last_health_status",
		"created_time",
		"updated_time",
	}
}

func (rows *aetherCASConflictRows) Close() error {
	return nil
}

func (rows *aetherCASConflictRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}

func aetherCASConflictStaleRow() []driver.Value {
	return aetherCASConflictRow(1, "stale", "stale-v1")
}

func aetherCASConflictLatestRow() []driver.Value {
	return aetherCASConflictRow(2, "latest", "latest-v2")
}

func aetherCASConflictRow(revision int64, routeProfile string, capabilityVersion string) []driver.Value {
	return []driver.Value{
		int64(1),
		int64(104),
		"aether-cas-conflict",
		routeProfile,
		AetherExecutionModeDirectChannel,
		capabilityVersion,
		int64(1),
		revision,
		int64(0),
		"control",
		"relay",
		"",
		"",
		int64(0),
		int64(0),
		int64(0),
		"",
		int64(0),
		int64(0),
	}
}

func init() {
	registerAetherCASConflictDriver.Do(func() {
		sql.Register(aetherCASConflictDriverName, aetherCASConflictDriver{})
	})
}
