package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAetherServiceAuthAcceptsValidRequestOnlyOnce(t *testing.T) {
	previousDB := model.DB
	previousClient := common.RDB
	previousRedisEnabled := common.RedisEnabled
	db, err := gorm.Open(sqlite.Open("file:aether_service_auth_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	common.RDB = nil
	common.RedisEnabled = false
	aetherServiceNonces = sync.Map{}
	t.Cleanup(func() {
		model.DB = previousDB
		common.RDB = previousClient
		common.RedisEnabled = previousRedisEnabled
		aetherServiceNonces = sync.Map{}
	})
	require.NoError(t, db.AutoMigrate(&model.AetherIntegration{}))

	integration := &model.AetherIntegration{
		ChannelID:      91,
		InstanceID:     "aether-primary",
		ExecutionMode:  model.AetherExecutionModeDirectChannel,
		Enabled:        true,
		ConfigRevision: 1,
	}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	router := gin.New()
	router.Use(AetherServiceAuth())
	router.GET("/api/aether/v1/events", func(c *gin.Context) {
		require.Equal(t, "aether-primary", c.GetString(AetherInstanceIDContextKey))
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/api/aether/v1/events?after=0", nil)
	setAetherServiceAuthHeaders(request, "aether-primary", "control-secret", "nonce-1234567890")
	firstRecorder := httptest.NewRecorder()
	router.ServeHTTP(firstRecorder, request)
	require.Equal(t, http.StatusNoContent, firstRecorder.Code)

	secondRecorder := httptest.NewRecorder()
	router.ServeHTTP(secondRecorder, request)
	require.Equal(t, http.StatusUnauthorized, secondRecorder.Code)
}

func TestAetherServiceAuthAcceptsTransitionControlSecretUntilItIsRevoked(t *testing.T) {
	previousDB := model.DB
	previousClient := common.RDB
	previousRedisEnabled := common.RedisEnabled
	db, err := gorm.Open(sqlite.Open("file:aether_service_auth_rotation_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	common.RDB = nil
	common.RedisEnabled = false
	aetherServiceNonces = sync.Map{}
	t.Cleanup(func() {
		model.DB = previousDB
		common.RDB = previousClient
		common.RedisEnabled = previousRedisEnabled
		aetherServiceNonces = sync.Map{}
	})
	require.NoError(t, db.AutoMigrate(&model.AetherIntegration{}, &model.AetherIntegrationPendingCredentialRotation{}))

	integration := &model.AetherIntegration{
		ChannelID:      92,
		InstanceID:     "aether-rotation",
		ExecutionMode:  model.AetherExecutionModeDirectChannel,
		Enabled:        true,
		ConfigRevision: 1,
	}
	require.NoError(t, integration.SetSecrets("control-v1", "relay-v1"))
	require.NoError(t, db.Create(integration).Error)
	rotated, conflict, err := model.UpdateAetherIntegrationSharedConfigWithSecretRotation(
		integration,
		1,
		model.AetherIntegrationSharedConfig{ExecutionMode: model.AetherExecutionModeDirectChannel, Enabled: true},
		&model.AetherIntegrationSecretRotation{
			ControlSecret:       "control-v2",
			RelaySigningSecret:  "relay-v2",
			TransitionExpiresAt: time.Now().UTC().Add(time.Minute),
		},
	)
	require.NoError(t, err)
	require.Nil(t, conflict)

	router := gin.New()
	router.Use(AetherServiceAuth())
	router.GET("/api/aether/v1/events", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	oldSecretRequest := httptest.NewRequest(http.MethodGet, "/api/aether/v1/events?after=0", nil)
	setAetherServiceAuthHeaders(oldSecretRequest, "aether-rotation", "control-v1", "nonce-transition-old")
	oldSecretRecorder := httptest.NewRecorder()
	router.ServeHTTP(oldSecretRecorder, oldSecretRequest)
	require.Equal(t, http.StatusNoContent, oldSecretRecorder.Code)

	revoked, conflict, err := model.RevokeAetherIntegrationTransitionSecrets(rotated, 2)
	require.NoError(t, err)
	require.Nil(t, conflict)
	require.Equal(t, int64(3), revoked.ConfigRevision)

	newSecretRequest := httptest.NewRequest(http.MethodGet, "/api/aether/v1/events?after=1", nil)
	setAetherServiceAuthHeaders(newSecretRequest, "aether-rotation", "control-v2", "nonce-transition-new")
	newSecretRecorder := httptest.NewRecorder()
	router.ServeHTTP(newSecretRecorder, newSecretRequest)
	require.Equal(t, http.StatusNoContent, newSecretRecorder.Code)

	revokedOldSecretRequest := httptest.NewRequest(http.MethodGet, "/api/aether/v1/events?after=2", nil)
	setAetherServiceAuthHeaders(revokedOldSecretRequest, "aether-rotation", "control-v1", "nonce-transition-revoked")
	revokedOldSecretRecorder := httptest.NewRecorder()
	router.ServeHTTP(revokedOldSecretRecorder, revokedOldSecretRequest)
	require.Equal(t, http.StatusUnauthorized, revokedOldSecretRecorder.Code)
}

func TestAetherServiceAuthExpiresTransitionSecretsAndRejectsOldControlSecret(t *testing.T) {
	previousDB := model.DB
	previousClient := common.RDB
	previousRedisEnabled := common.RedisEnabled
	db, err := gorm.Open(sqlite.Open("file:aether_service_auth_expired_transition_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	common.RDB = nil
	common.RedisEnabled = false
	aetherServiceNonces = sync.Map{}
	t.Cleanup(func() {
		model.DB = previousDB
		common.RDB = previousClient
		common.RedisEnabled = previousRedisEnabled
		aetherServiceNonces = sync.Map{}
	})
	require.NoError(t, db.AutoMigrate(&model.AetherIntegration{}))

	previousControlSecret, err := common.EncryptAetherSecret("control-v1")
	require.NoError(t, err)
	previousRelaySigningSecret, err := common.EncryptAetherSecret("relay-v1")
	require.NoError(t, err)
	integration := &model.AetherIntegration{
		ChannelID:                           93,
		InstanceID:                          "aether-expired-transition",
		ExecutionMode:                       model.AetherExecutionModeDirectChannel,
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

	router := gin.New()
	router.Use(AetherServiceAuth())
	router.GET("/api/aether/v1/events", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	oldSecretRequest := httptest.NewRequest(http.MethodGet, "/api/aether/v1/events?after=0", nil)
	setAetherServiceAuthHeaders(oldSecretRequest, "aether-expired-transition", "control-v1", "nonce-expired-transition")
	oldSecretRecorder := httptest.NewRecorder()
	router.ServeHTTP(oldSecretRecorder, oldSecretRequest)
	require.Equal(t, http.StatusUnauthorized, oldSecretRecorder.Code)

	var stored model.AetherIntegration
	require.NoError(t, db.First(&stored, integration.Id).Error)
	require.Empty(t, stored.PreviousControlSecretEncrypted)
	require.Empty(t, stored.PreviousRelaySigningSecretEncrypted)
	require.Zero(t, stored.TransitionSecretsExpireAt)
}

func TestAetherServiceNonceUsesRedisAcrossProcessMemory(t *testing.T) {
	server, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(server.Close)

	previousClient := common.RDB
	previousRedisEnabled := common.RedisEnabled
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RedisEnabled = true
	aetherServiceNonces = sync.Map{}
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB = previousClient
		common.RedisEnabled = previousRedisEnabled
		aetherServiceNonces = sync.Map{}
	})

	expiresAt := time.Now().Add(time.Minute)
	require.True(t, rememberAetherServiceNonce("aether-primary:nonce-1234567890", expiresAt))

	// A separate process has no local nonce memory but must still reject the replay.
	aetherServiceNonces = sync.Map{}
	require.False(t, rememberAetherServiceNonce("aether-primary:nonce-1234567890", expiresAt))
}

func TestAetherServiceNonceFailsClosedWhenRedisIsUnavailable(t *testing.T) {
	server, err := miniredis.Run()
	require.NoError(t, err)

	previousClient := common.RDB
	previousRedisEnabled := common.RedisEnabled
	common.RDB = redis.NewClient(&redis.Options{
		Addr:         server.Addr(),
		MaxRetries:   0,
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	})
	common.RedisEnabled = true
	aetherServiceNonces = sync.Map{}
	server.Close()
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB = previousClient
		common.RedisEnabled = previousRedisEnabled
		aetherServiceNonces = sync.Map{}
	})

	expiresAt := time.Now().Add(time.Minute)
	require.False(t, rememberAetherServiceNonce("aether-primary:nonce-redis-down", expiresAt))
}

func setAetherServiceAuthHeaders(request *http.Request, instanceID string, secret string, nonce string) {
	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	canonical := request.Method + "\n" + request.URL.EscapedPath() + "\n" + request.URL.RawQuery + "\n" + timestamp + "\n" + nonce
	request.Header.Set(AetherInstanceIDHeader, instanceID)
	request.Header.Set(AetherTimestampHeader, timestamp)
	request.Header.Set(AetherNonceHeader, nonce)
	request.Header.Set(AetherSignatureHeader, common.GenerateHMACWithKey([]byte(secret), canonical))
}
