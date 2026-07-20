package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type aetherDiagnosticError interface {
	error
	ControlStage() string
	ControlCode() string
}

var aetherControlSyncFixtureSequence atomic.Uint64

type aetherControlRoundTripper func(*http.Request) (*http.Response, error)

func (roundTripper aetherControlRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTripper(request)
}

func TestAetherControlV2CanonicalPayloadMatchesContractVector(t *testing.T) {
	rawBody := []byte(`{"base_revision":7,"credential_rotation":{"id":"rotation_01JZ8K2A9Y","control_secret":"test-only-control-secret-v3","relay_signing_secret":"test-only-relay-secret-v3","transition_expires_at":1784077200,"revoke_previous":false}}`)
	digest := sha256.Sum256(rawBody)
	bodySHA256 := hex.EncodeToString(digest[:])
	require.Equal(t, "ed3912c274f42d73d47de42f64632bc09cce1e430b97cc76294ff80f8103cb47", bodySHA256)
	requestURL, err := url.Parse("https://aether.example/api/integrations/new-api/v1/instances/aether-primary")
	require.NoError(t, err)
	canonicalPayload := aetherControlV2CanonicalPayload(
		http.MethodPut,
		requestURL,
		"aether-primary",
		"1784073600",
		"nonce-control-v2-123456",
		bodySHA256,
	)
	assert.Equal(t,
		"AETHER-CONTROL-V2\nPUT\n/api/integrations/new-api/v1/instances/aether-primary\n\naether-primary\n1784073600\nnonce-control-v2-123456\ned3912c274f42d73d47de42f64632bc09cce1e430b97cc76294ff80f8103cb47",
		canonicalPayload,
	)
	assert.Equal(t,
		"d6a3ca51c6aaaee2f0f960710c7e6fc431f7e3c219b6420816b5e1c6c3afd80f",
		common.GenerateHMACWithKey([]byte("test-only-control-secret-v2"), canonicalPayload),
	)
}

func TestRotateAetherIntegrationCredentialsRemoteFirstPromotesOnlyAfterVerifiedV2Ack(t *testing.T) {
	transitionExpiresAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	remoteRequestReceived := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/integrations/new-api/v1/capabilities":
			require.Equal(t, http.MethodGet, request.Method)
			require.Equal(t, "Bearer control-secret", request.Header.Get("Authorization"))
			require.Equal(t, "aether-primary", request.Header.Get(AetherInstanceIDHeader))
			require.Empty(t, request.Header.Get("X-Aether-Signature-Version"))
			_, _ = writer.Write([]byte(`{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":["credential_rotation_v1","control_hmac_v2"]}`))
		case "/api/integrations/new-api/v1/instances/aether-primary":
			require.Equal(t, http.MethodPut, request.Method)
			require.Empty(t, request.Header.Get("Authorization"))
			rawBody, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			digest := sha256.Sum256(rawBody)
			bodySHA256 := hex.EncodeToString(digest[:])
			assert.Equal(t, bodySHA256, request.Header.Get("X-Aether-Body-SHA256"))
			assert.Equal(t, "aether-primary", request.Header.Get("X-Aether-Instance-ID"))
			assert.Equal(t, "v2", request.Header.Get("X-Aether-Signature-Version"))
			timestamp := request.Header.Get("X-Aether-Timestamp")
			parsedTimestamp, err := strconv.ParseInt(timestamp, 10, 64)
			require.NoError(t, err)
			assert.Greater(t, parsedTimestamp, int64(0))
			nonce := request.Header.Get("X-Aether-Nonce")
			assert.NotEmpty(t, nonce)
			canonicalPayload := aetherControlV2CanonicalPayload(
				request.Method,
				request.URL,
				request.Header.Get("X-Aether-Instance-ID"),
				timestamp,
				nonce,
				bodySHA256,
			)
			assert.Equal(t, common.GenerateHMACWithKey([]byte("control-secret"), canonicalPayload), request.Header.Get("X-Aether-Signature"))
			var payload struct {
				BaseRevision       int64 `json:"base_revision"`
				CredentialRotation struct {
					ID                  string `json:"id"`
					ControlSecret       string `json:"control_secret"`
					RelaySigningSecret  string `json:"relay_signing_secret"`
					TransitionExpiresAt int64  `json:"transition_expires_at"`
					RevokePrevious      bool   `json:"revoke_previous"`
				} `json:"credential_rotation"`
			}
			require.NoError(t, common.Unmarshal(rawBody, &payload))
			assert.Equal(t, int64(7), payload.BaseRevision)
			assert.Equal(t, "rotation-v2", payload.CredentialRotation.ID)
			assert.Equal(t, "control-v2", payload.CredentialRotation.ControlSecret)
			assert.Equal(t, "relay-v2", payload.CredentialRotation.RelaySigningSecret)
			assert.Equal(t, transitionExpiresAt.Unix(), payload.CredentialRotation.TransitionExpiresAt)
			assert.False(t, payload.CredentialRotation.RevokePrevious)
			stored, err := model.GetAetherIntegrationByChannelID(121)
			require.NoError(t, err)
			controlSecret, relaySecret, err := stored.Secrets()
			require.NoError(t, err)
			assert.Equal(t, "control-secret", controlSecret)
			assert.Equal(t, "relay-signing-secret", relaySecret)
			remoteRequestReceived = true
			_, _ = writer.Write([]byte(`{"instance_id":"aether-primary","base_revision":8,"credential_rotation_ack":{"rotation_id":"rotation-v2","credential_revision":4,"transition_expires_at":` + strconv.FormatInt(transitionExpiresAt.Unix(), 10) + `,"state":"applied"}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	db, channelID := newAetherControlSyncFixture(t, server.URL, server.Client(), 121)
	require.NoError(t, db.Model(&model.AetherIntegration{}).Where("channel_id = ?", channelID).Updates(map[string]interface{}{
		"remote_config_revision": int64(7),
	}).Error)
	updated, err := RotateAetherIntegrationCredentialsRemoteFirst(
		channelID,
		1,
		model.AetherIntegrationPendingCredentialRotationRequest{
			RotationID:          "rotation-v2",
			ControlSecret:       "control-v2",
			RelaySigningSecret:  "relay-v2",
			TransitionExpiresAt: transitionExpiresAt,
		},
	)

	require.NoError(t, err)
	require.True(t, remoteRequestReceived)
	require.NotNil(t, updated)
	assert.Equal(t, int64(2), updated.ConfigRevision)
	assert.Equal(t, int64(8), updated.RemoteConfigRevision)
	controlSecrets, err := updated.ActiveControlSecrets(time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, []string{"control-v2", "control-secret"}, controlSecrets)
	var pendingCount int64
	require.NoError(t, db.Model(&model.AetherIntegrationPendingCredentialRotation{}).Count(&pendingCount).Error)
	assert.Zero(t, pendingCount)
}

func TestRotateAetherIntegrationCredentialsRemoteFirstRequiresBothCapabilitiesBeforePreparing(t *testing.T) {
	for index, features := range [][]string{{"credential_rotation_v1"}, {"control_hmac_v2"}} {
		t.Run(strings.Join(features, "_"), func(t *testing.T) {
			requests := make([]string, 0, 2)
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				require.Equal(t, "Bearer control-secret", request.Header.Get("Authorization"))
				requests = append(requests, request.Method+" "+request.URL.Path)
				_, _ = writer.Write([]byte(`{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":["` + features[0] + `"]}`))
			}))
			defer server.Close()

			db, channelID := newAetherControlSyncFixture(t, server.URL, server.Client(), 122+index)
			updated, err := RotateAetherIntegrationCredentialsRemoteFirst(
				channelID,
				1,
				model.AetherIntegrationPendingCredentialRotationRequest{
					RotationID:          "rotation-v2",
					ControlSecret:       "control-v2",
					RelaySigningSecret:  "relay-v2",
					TransitionExpiresAt: time.Now().UTC().Add(time.Minute),
				},
			)

			require.Nil(t, updated)
			var controlErr *AetherControlError
			require.ErrorAs(t, err, &controlErr)
			assert.Equal(t, "capabilities", controlErr.Stage)
			assert.Equal(t, "unsupported_feature", controlErr.Code)
			assert.Equal(t, []string{"GET /api/integrations/new-api/v1/capabilities"}, requests)
			var pendingCount int64
			require.NoError(t, db.Model(&model.AetherIntegrationPendingCredentialRotation{}).Count(&pendingCount).Error)
			assert.Zero(t, pendingCount)
			stored, err := model.GetAetherIntegrationByChannelID(channelID)
			require.NoError(t, err)
			controlSecret, relaySecret, err := stored.Secrets()
			require.NoError(t, err)
			assert.Equal(t, "control-secret", controlSecret)
			assert.Equal(t, "relay-signing-secret", relaySecret)
		})
	}
}

func TestRotateAetherIntegrationCredentialsRemoteFirstRetainsPendingOnRemoteFailure(t *testing.T) {
	for _, statusCode := range []int{http.StatusConflict, http.StatusBadGateway} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/api/integrations/new-api/v1/capabilities" {
					require.Equal(t, "Bearer control-secret", request.Header.Get("Authorization"))
					_, _ = writer.Write([]byte(`{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":["credential_rotation_v1","control_hmac_v2"]}`))
					return
				}
				require.Empty(t, request.Header.Get("Authorization"))
				writer.WriteHeader(statusCode)
				_, _ = writer.Write([]byte(`{"error":"remote failure"}`))
			}))
			defer server.Close()

			db, channelID := newAetherControlSyncFixture(t, server.URL, server.Client(), 123+statusCode)
			_, err := RotateAetherIntegrationCredentialsRemoteFirst(
				channelID,
				1,
				model.AetherIntegrationPendingCredentialRotationRequest{
					RotationID:          "rotation-v2",
					ControlSecret:       "control-v2",
					RelaySigningSecret:  "relay-v2",
					TransitionExpiresAt: time.Now().UTC().Add(time.Minute),
				},
			)
			var controlErr *AetherControlError
			require.ErrorAs(t, err, &controlErr)
			assert.Equal(t, "rotation", controlErr.Stage)
			assert.Equal(t, statusCode, controlErr.StatusCode)
			var pending model.AetherIntegrationPendingCredentialRotation
			require.NoError(t, db.Where("rotation_id = ?", "rotation-v2").First(&pending).Error)
			stored, err := model.GetAetherIntegrationByChannelID(channelID)
			require.NoError(t, err)
			controlSecret, relaySecret, err := stored.Secrets()
			require.NoError(t, err)
			assert.Equal(t, "control-secret", controlSecret)
			assert.Equal(t, "relay-signing-secret", relaySecret)
			assert.Equal(t, int64(2), stored.ConfigRevision)
		})
	}
}

func TestRotateAetherIntegrationCredentialsRemoteFirstRetainsPendingOnTransportTimeout(t *testing.T) {
	putAttempted := false
	client := &http.Client{Transport: aetherControlRoundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/integrations/new-api/v1/capabilities":
			require.Equal(t, "Bearer control-secret", request.Header.Get("Authorization"))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":["credential_rotation_v1","control_hmac_v2"]}`)),
				Request:    request,
			}, nil
		case "/api/integrations/new-api/v1/instances/aether-primary":
			putAttempted = true
			return nil, context.DeadlineExceeded
		default:
			return nil, fmt.Errorf("unexpected control path %s", request.URL.Path)
		}
	})}
	db, channelID := newAetherControlSyncFixture(t, "https://aether.example", client, 126)
	_, err := RotateAetherIntegrationCredentialsRemoteFirst(
		channelID,
		1,
		model.AetherIntegrationPendingCredentialRotationRequest{
			RotationID:          "rotation-v2",
			ControlSecret:       "control-v2",
			RelaySigningSecret:  "relay-v2",
			TransitionExpiresAt: time.Now().UTC().Add(time.Minute),
		},
	)

	require.True(t, putAttempted)
	var controlErr *AetherControlError
	require.ErrorAs(t, err, &controlErr)
	assert.Equal(t, "rotation", controlErr.Stage)
	assert.Equal(t, "transport_failed", controlErr.Code)
	var pending model.AetherIntegrationPendingCredentialRotation
	require.NoError(t, db.Where("rotation_id = ?", "rotation-v2").First(&pending).Error)
	stored, err := model.GetAetherIntegrationByChannelID(channelID)
	require.NoError(t, err)
	controlSecret, relaySecret, err := stored.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-secret", controlSecret)
	assert.Equal(t, "relay-signing-secret", relaySecret)
}

func TestRotateAetherIntegrationCredentialsRemoteFirstRetainsPendingOnMismatchedAck(t *testing.T) {
	transitionExpiresAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/integrations/new-api/v1/capabilities" {
			_, _ = writer.Write([]byte(`{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":["credential_rotation_v1","control_hmac_v2"]}`))
			return
		}
		_, _ = writer.Write([]byte(`{"instance_id":"aether-primary","base_revision":8,"credential_rotation_ack":{"rotation_id":"other-rotation","credential_revision":4,"transition_expires_at":` + strconv.FormatInt(transitionExpiresAt.Unix(), 10) + `,"state":"applied"}}`))
	}))
	defer server.Close()

	db, channelID := newAetherControlSyncFixture(t, server.URL, server.Client(), 127)
	require.NoError(t, db.Model(&model.AetherIntegration{}).Where("channel_id = ?", channelID).Update("remote_config_revision", int64(7)).Error)
	_, err := RotateAetherIntegrationCredentialsRemoteFirst(
		channelID,
		1,
		model.AetherIntegrationPendingCredentialRotationRequest{
			RotationID:          "rotation-v2",
			ControlSecret:       "control-v2",
			RelaySigningSecret:  "relay-v2",
			TransitionExpiresAt: transitionExpiresAt,
		},
	)
	var controlErr *AetherControlError
	require.ErrorAs(t, err, &controlErr)
	assert.Equal(t, "rotation", controlErr.Stage)
	assert.Equal(t, "invalid_response", controlErr.Code)
	var pending model.AetherIntegrationPendingCredentialRotation
	require.NoError(t, db.Where("rotation_id = ?", "rotation-v2").First(&pending).Error)
	stored, err := model.GetAetherIntegrationByChannelID(channelID)
	require.NoError(t, err)
	controlSecret, relaySecret, err := stored.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-secret", controlSecret)
	assert.Equal(t, "relay-signing-secret", relaySecret)
}

func TestRotateAetherIntegrationCredentialsRemoteFirstRetriesTheSamePendingRotation(t *testing.T) {
	transitionExpiresAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	putAttempts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/integrations/new-api/v1/capabilities" {
			_, _ = writer.Write([]byte(`{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":["credential_rotation_v1","control_hmac_v2"]}`))
			return
		}
		putAttempts++
		if putAttempts == 1 {
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = writer.Write([]byte(`{"instance_id":"aether-primary","base_revision":8,"credential_rotation_ack":{"rotation_id":"rotation-v2","credential_revision":4,"transition_expires_at":` + strconv.FormatInt(transitionExpiresAt.Unix(), 10) + `,"state":"applied"}}`))
	}))
	defer server.Close()

	db, channelID := newAetherControlSyncFixture(t, server.URL, server.Client(), 128)
	require.NoError(t, db.Model(&model.AetherIntegration{}).Where("channel_id = ?", channelID).Update("remote_config_revision", int64(7)).Error)
	rotation := model.AetherIntegrationPendingCredentialRotationRequest{
		RotationID:          "rotation-v2",
		ControlSecret:       "control-v2",
		RelaySigningSecret:  "relay-v2",
		TransitionExpiresAt: transitionExpiresAt,
	}
	_, err := RotateAetherIntegrationCredentialsRemoteFirst(channelID, 1, rotation)
	require.Error(t, err)
	updated, err := RotateAetherIntegrationCredentialsRemoteFirst(channelID, 1, rotation)

	require.NoError(t, err)
	assert.Equal(t, 2, putAttempts)
	controlSecret, relaySecret, err := updated.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-v2", controlSecret)
	assert.Equal(t, "relay-v2", relaySecret)
	var pendingCount int64
	require.NoError(t, db.Model(&model.AetherIntegrationPendingCredentialRotation{}).Count(&pendingCount).Error)
	assert.Zero(t, pendingCount)
}

func TestRotateAetherIntegrationCredentialsRemoteFirstRecoversAppliedPendingRotationAfterOldControlSecretExpires(t *testing.T) {
	transitionExpiresAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	oldSecretCapabilityAttempts := 0
	pendingSecretCapabilityAttempts := 0
	rotationAttempts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/integrations/new-api/v1/capabilities":
			switch request.Header.Get("Authorization") {
			case "Bearer control-secret":
				oldSecretCapabilityAttempts++
				writer.WriteHeader(http.StatusUnauthorized)
			case "Bearer control-v2":
				pendingSecretCapabilityAttempts++
				_, _ = writer.Write([]byte(`{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":["credential_rotation_v1","control_hmac_v2"]}`))
			default:
				require.Failf(t, "unexpected capability credentials", "authorization=%q", request.Header.Get("Authorization"))
			}
		case "/api/integrations/new-api/v1/instances/aether-primary":
			rotationAttempts++
			require.Empty(t, request.Header.Get("Authorization"))
			require.Equal(t, "v2", request.Header.Get("X-Aether-Signature-Version"))
			rawBody, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			bodySHA256 := aetherControlV2BodySHA256(rawBody)
			canonicalPayload := aetherControlV2CanonicalPayload(
				request.Method,
				request.URL,
				request.Header.Get(AetherInstanceIDHeader),
				request.Header.Get("X-Aether-Timestamp"),
				request.Header.Get("X-Aether-Nonce"),
				bodySHA256,
			)
			assert.Equal(t, common.GenerateHMACWithKey([]byte("control-v2"), canonicalPayload), request.Header.Get("X-Aether-Signature"))
			_, _ = writer.Write([]byte(`{"instance_id":"aether-primary","base_revision":8,"credential_rotation_ack":{"rotation_id":"rotation-v2","credential_revision":4,"transition_expires_at":` + strconv.FormatInt(transitionExpiresAt.Unix(), 10) + `,"state":"applied"}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	db, channelID := newAetherControlSyncFixture(t, server.URL, server.Client(), 129)
	require.NoError(t, db.Model(&model.AetherIntegration{}).Where("channel_id = ?", channelID).Update("remote_config_revision", int64(7)).Error)
	integration, err := model.GetAetherIntegrationByChannelID(channelID)
	require.NoError(t, err)
	rotation := model.AetherIntegrationPendingCredentialRotationRequest{
		RotationID:          "rotation-v2",
		ControlSecret:       "control-v2",
		RelaySigningSecret:  "relay-v2",
		TransitionExpiresAt: transitionExpiresAt,
	}
	pending, conflict, err := model.PrepareAetherIntegrationPendingCredentialRotation(integration, 1, rotation)
	require.NoError(t, err)
	require.Nil(t, conflict)
	require.NotNil(t, pending)

	updated, err := RotateAetherIntegrationCredentialsRemoteFirst(channelID, 1, rotation)

	require.NoError(t, err)
	assert.Equal(t, 1, oldSecretCapabilityAttempts)
	assert.Equal(t, 1, pendingSecretCapabilityAttempts)
	assert.Equal(t, 1, rotationAttempts)
	controlSecret, relaySecret, err := updated.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-v2", controlSecret)
	assert.Equal(t, "relay-v2", relaySecret)
	var pendingCount int64
	require.NoError(t, db.Model(&model.AetherIntegrationPendingCredentialRotation{}).Count(&pendingCount).Error)
	assert.Zero(t, pendingCount)
}

func TestSyncAetherIntegrationMirrorsConfigAndHealth(t *testing.T) {
	requests := make([]string, 0, 3)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "Bearer control-secret", request.Header.Get("Authorization"))
		require.Equal(t, "aether-primary", request.Header.Get(AetherInstanceIDHeader))
		requests = append(requests, request.Method+" "+request.URL.Path)
		switch request.URL.Path {
		case "/api/integrations/new-api/v1/capabilities":
			require.Equal(t, http.MethodGet, request.Method)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"supported_modes":["direct_channel","parallel_shadow"],"supported_formats":["openai"],"capability_version":"0.1.0","features":[]}`))
		case "/api/integrations/new-api/v1/instances/aether-primary":
			require.Equal(t, http.MethodPut, request.Method)
			var payload struct {
				RouteProfile      string `json:"route_profile"`
				ExecutionMode     string `json:"execution_mode"`
				CapabilityVersion string `json:"capability_version"`
				Enabled           bool   `json:"enabled"`
				BaseRevision      int64  `json:"base_revision"`
			}
			require.NoError(t, common.DecodeJson(request.Body, &payload))
			assert.Equal(t, "balanced", payload.RouteProfile)
			assert.Equal(t, model.AetherExecutionModeDirectChannel, payload.ExecutionMode)
			assert.Equal(t, "local-contract-v1", payload.CapabilityVersion)
			assert.True(t, payload.Enabled)
			assert.Zero(t, payload.BaseRevision)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"instance_id":"aether-primary","route_profile":"balanced","execution_mode":"direct_channel","enabled":true,"capability_version":"0.1.0","base_revision":1,"updated_at":"2026-07-15T00:00:00Z"}`))
		case "/api/integrations/new-api/v1/instances/aether-primary/status":
			require.Equal(t, http.MethodGet, request.Method)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"instance_id":"aether-primary","healthy":true,"last_sync_at":null,"capability_version":"0.1.0","base_revision":1,"uptime_secs":1,"active_channels":2,"routing_mode":"direct_channel"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	previousDB := model.DB
	previousClient := aetherControlHTTPClient
	db, err := gorm.Open(sqlite.Open("file:aether_control_sync_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	aetherControlHTTPClient = server.Client()
	t.Cleanup(func() {
		model.DB = previousDB
		aetherControlHTTPClient = previousClient
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}))
	baseURL := server.URL
	channel := &model.Channel{Id: 61, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default", BaseURL: &baseURL}
	require.NoError(t, db.Create(channel).Error)
	integration := &model.AetherIntegration{
		ChannelID:         channel.Id,
		InstanceID:        "aether-primary",
		RouteProfile:      "balanced",
		ExecutionMode:     model.AetherExecutionModeDirectChannel,
		CapabilityVersion: "local-contract-v1",
		Enabled:           true,
		ConfigRevision:    3,
	}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	updated, err := SyncAetherIntegration(channel.Id)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"GET /api/integrations/new-api/v1/capabilities",
		"PUT /api/integrations/new-api/v1/instances/aether-primary",
		"GET /api/integrations/new-api/v1/instances/aether-primary/status",
	}, requests)
	assert.Equal(t, "local-contract-v1", updated.CapabilityVersion)
	assert.Equal(t, int64(1), updated.RemoteConfigRevision)
	assert.Equal(t, "healthy", updated.LastHealthStatus)
	assert.NotZero(t, updated.LastSyncTime)
	assert.NotZero(t, updated.LastHealthTime)
}

func TestSyncAetherIntegrationReturnsRemoteRevisionConflict(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "Bearer control-secret", request.Header.Get("Authorization"))
		if request.URL.Path == "/api/integrations/new-api/v1/capabilities" {
			require.Equal(t, http.MethodGet, request.Method)
			_, _ = writer.Write([]byte(`{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.1","features":[]}`))
			return
		}
		require.Equal(t, http.MethodPut, request.Method)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusConflict)
		_, _ = writer.Write([]byte(`{"error":"revision conflict","current_revision":4,"current_config":{}}`))
	}))
	defer server.Close()

	previousDB := model.DB
	previousClient := aetherControlHTTPClient
	db, err := gorm.Open(sqlite.Open("file:aether_control_sync_conflict_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	aetherControlHTTPClient = server.Client()
	t.Cleanup(func() {
		model.DB = previousDB
		aetherControlHTTPClient = previousClient
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}))
	baseURL := server.URL
	require.NoError(t, db.Create(&model.Channel{Id: 62, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default", BaseURL: &baseURL}).Error)
	integration := &model.AetherIntegration{
		ChannelID:         62,
		InstanceID:        "aether-primary",
		RouteProfile:      "local-unsynced",
		ExecutionMode:     model.AetherExecutionModeDirectChannel,
		CapabilityVersion: "local-pending",
		Enabled:           true,
		ConfigRevision:    1,
	}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	_, err = SyncAetherIntegration(62)

	var conflict *AetherControlConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, int64(4), conflict.CurrentRevision)
	var stored model.AetherIntegration
	require.NoError(t, db.First(&stored, integration.Id).Error)
	assert.Equal(t, "local-pending", stored.CapabilityVersion)
	assert.Equal(t, "local-unsynced", stored.RouteProfile)
	assert.Equal(t, int64(1), stored.ConfigRevision)
	assert.Zero(t, stored.RemoteConfigRevision)
}

func TestSyncAetherIntegrationRejectsIncompatibleCapabilitiesBeforePut(t *testing.T) {
	tests := []struct {
		name         string
		capabilities string
		wantCode     string
	}{
		{
			name:         "missing direct channel",
			capabilities: `{"supported_modes":["parallel_shadow","aether_decision"],"supported_formats":["openai"],"capability_version":"0.1.0","features":[]}`,
			wantCode:     "unsupported_mode",
		},
		{
			name:         "missing openai format",
			capabilities: `{"supported_modes":["direct_channel"],"supported_formats":["anthropic"],"capability_version":"0.1.0","features":[]}`,
			wantCode:     "unsupported_format",
		},
		{
			name:         "empty capability version",
			capabilities: `{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"","features":[]}`,
			wantCode:     "incompatible_version",
		},
		{
			name:         "incompatible capability version",
			capabilities: `{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.2.0","features":[]}`,
			wantCode:     "incompatible_version",
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := make([]string, 0, 3)
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				require.Equal(t, "Bearer control-secret", request.Header.Get("Authorization"))
				requests = append(requests, request.Method+" "+request.URL.Path)
				if request.URL.Path == "/api/integrations/new-api/v1/capabilities" {
					_, _ = writer.Write([]byte(test.capabilities))
					return
				}
				if request.Method == http.MethodPut {
					_, _ = writer.Write([]byte(`{"instance_id":"aether-primary","capability_version":"0.1.0","base_revision":8}`))
					return
				}
				_, _ = writer.Write([]byte(`{"instance_id":"aether-primary","healthy":true,"base_revision":8}`))
			}))
			defer server.Close()

			previousDB := model.DB
			previousClient := aetherControlHTTPClient
			db, err := gorm.Open(sqlite.Open("file:aether_control_capabilities_"+string(rune('a'+index))+"?mode=memory&cache=shared"), &gorm.Config{})
			require.NoError(t, err)
			model.DB = db
			aetherControlHTTPClient = server.Client()
			t.Cleanup(func() {
				model.DB = previousDB
				aetherControlHTTPClient = previousClient
			})
			require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}))
			baseURL := server.URL
			channel := &model.Channel{Id: 70 + index, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default", BaseURL: &baseURL}
			require.NoError(t, db.Create(channel).Error)
			integration := &model.AetherIntegration{
				ChannelID:            channel.Id,
				InstanceID:           "aether-primary",
				RouteProfile:         "local-unsynced",
				ExecutionMode:        model.AetherExecutionModeDirectChannel,
				Enabled:              true,
				CapabilityVersion:    "local-pending",
				ConfigRevision:       4,
				RemoteConfigRevision: 7,
				LastSyncTime:         123,
			}
			require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
			require.NoError(t, db.Create(integration).Error)

			_, err = SyncAetherIntegration(channel.Id)

			var diagnostic aetherDiagnosticError
			require.True(t, errors.As(err, &diagnostic), "error must expose a control stage and code: %v", err)
			assert.Equal(t, "capabilities", diagnostic.ControlStage())
			assert.Equal(t, test.wantCode, diagnostic.ControlCode())
			assert.Equal(t, []string{"GET /api/integrations/new-api/v1/capabilities"}, requests)
			var stored model.AetherIntegration
			require.NoError(t, db.First(&stored, integration.Id).Error)
			assert.Equal(t, "local-pending", stored.CapabilityVersion)
			assert.Equal(t, "local-unsynced", stored.RouteProfile)
			assert.Equal(t, int64(4), stored.ConfigRevision)
			assert.Equal(t, int64(7), stored.RemoteConfigRevision)
			assert.Equal(t, int64(123), stored.LastSyncTime)
		})
	}
}

func TestSyncAetherIntegrationNeverActivatesReservedMode(t *testing.T) {
	requests := make([]string, 0, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "Bearer control-secret", request.Header.Get("Authorization"))
		requests = append(requests, request.Method+" "+request.URL.Path)
		if request.URL.Path == "/api/integrations/new-api/v1/capabilities" {
			_, _ = writer.Write([]byte(`{"supported_modes":["direct_channel","parallel_shadow","aether_decision"],"supported_formats":["openai"],"capability_version":"0.1.0","features":[]}`))
			return
		}
		_, _ = writer.Write([]byte(`{"instance_id":"aether-primary","capability_version":"0.1.0","base_revision":1}`))
	}))
	defer server.Close()

	db, channelID := newAetherControlSyncFixture(t, server.URL, server.Client(), 79)
	require.NoError(t, db.Model(&model.AetherIntegration{}).Where("channel_id = ?", channelID).Update("execution_mode", model.AetherExecutionModeParallelShadow).Error)

	_, err := SyncAetherIntegration(channelID)

	var controlErr *AetherControlError
	require.ErrorAs(t, err, &controlErr)
	assert.Equal(t, "configuration", controlErr.Stage)
	assert.Equal(t, "unsupported_mode", controlErr.Code)
	assert.Equal(t, []string{"GET /api/integrations/new-api/v1/capabilities"}, requests)
	var stored model.AetherIntegration
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&stored).Error)
	assert.Equal(t, model.AetherExecutionModeParallelShadow, stored.ExecutionMode)
	assert.Empty(t, stored.CapabilityVersion)
	assert.Zero(t, stored.RemoteConfigRevision)
	assert.Zero(t, stored.LastSyncTime)
}

func TestSyncAetherIntegrationReturnsTypedHTTPErrorForEveryControlStage(t *testing.T) {
	tests := []struct {
		name      string
		failStage string
	}{
		{name: "capabilities", failStage: "capabilities"},
		{name: "configuration", failStage: "configuration"},
		{name: "status", failStage: "status"},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				require.Equal(t, "Bearer control-secret", request.Header.Get("Authorization"))
				stage := "status"
				successBody := `{"instance_id":"aether-primary","healthy":true,"base_revision":1}`
				if request.URL.Path == "/api/integrations/new-api/v1/capabilities" {
					stage = "capabilities"
					successBody = `{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":[]}`
				} else if request.Method == http.MethodPut {
					stage = "configuration"
					successBody = `{"instance_id":"aether-primary","capability_version":"0.1.0","base_revision":1}`
				}
				if stage == test.failStage {
					writer.WriteHeader(http.StatusBadGateway)
					_, _ = writer.Write([]byte(`{"error":"body-secret-must-not-leak"}`))
					return
				}
				_, _ = writer.Write([]byte(successBody))
			}))
			defer server.Close()

			db, channelID := newAetherControlSyncFixture(t, server.URL, server.Client(), 80+index)
			if test.failStage == "status" {
				require.NoError(t, db.Model(&model.AetherIntegration{}).Where("channel_id = ?", channelID).Updates(map[string]interface{}{
					"last_health_time":   int64(456),
					"last_health_status": "previously-healthy",
				}).Error)
			}
			_, err := SyncAetherIntegration(channelID)

			var controlErr *AetherControlError
			require.ErrorAs(t, err, &controlErr)
			assert.Equal(t, test.failStage, controlErr.Stage)
			assert.Equal(t, "unexpected_status", controlErr.Code)
			assert.Equal(t, http.StatusBadGateway, controlErr.StatusCode)
			assert.NotContains(t, err.Error(), "control-secret")
			assert.NotContains(t, err.Error(), "body-secret-must-not-leak")
			if test.failStage == "status" {
				var stored model.AetherIntegration
				require.NoError(t, db.Where("channel_id = ?", channelID).First(&stored).Error)
				assert.Equal(t, int64(1), stored.RemoteConfigRevision)
				assert.NotZero(t, stored.LastSyncTime)
				assert.Equal(t, int64(456), stored.LastHealthTime)
				assert.Equal(t, "previously-healthy", stored.LastHealthStatus)
			}
		})
	}
}

func TestSyncAetherIntegrationRejectsMismatchedStatusRevisionWithoutRollingBackConfig(t *testing.T) {
	tests := []struct {
		name           string
		statusRevision int64
	}{
		{name: "status revision behind", statusRevision: 4},
		{name: "status revision ahead", statusRevision: 6},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				require.Equal(t, "Bearer control-secret", request.Header.Get("Authorization"))
				switch {
				case request.URL.Path == "/api/integrations/new-api/v1/capabilities":
					_, _ = writer.Write([]byte(`{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":[]}`))
				case request.Method == http.MethodPut:
					_, _ = writer.Write([]byte(`{"instance_id":"aether-primary","capability_version":"0.1.0","base_revision":5}`))
				default:
					_, _ = writer.Write([]byte(`{"instance_id":"aether-primary","healthy":true,"base_revision":` + string(rune('0'+test.statusRevision)) + `}`))
				}
			}))
			defer server.Close()

			db, channelID := newAetherControlSyncFixture(t, server.URL, server.Client(), 95+index)
			require.NoError(t, db.Model(&model.AetherIntegration{}).Where("channel_id = ?", channelID).Updates(map[string]interface{}{
				"remote_config_revision": int64(2),
				"last_sync_time":         int64(123),
				"last_health_time":       int64(456),
				"last_health_status":     "previously-healthy",
			}).Error)

			_, err := SyncAetherIntegration(channelID)

			var controlErr *AetherControlError
			require.ErrorAs(t, err, &controlErr)
			assert.Equal(t, "status", controlErr.Stage)
			assert.Equal(t, "invalid_response", controlErr.Code)
			var stored model.AetherIntegration
			require.NoError(t, db.Where("channel_id = ?", channelID).First(&stored).Error)
			assert.Equal(t, int64(5), stored.RemoteConfigRevision)
			assert.Greater(t, stored.LastSyncTime, int64(123))
			assert.Equal(t, int64(456), stored.LastHealthTime)
			assert.Equal(t, "previously-healthy", stored.LastHealthStatus)
		})
	}
}

func TestSyncAetherIntegrationRejectsOversizedControlResponse(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "Bearer control-secret", request.Header.Get("Authorization"))
		requests++
		_, _ = writer.Write([]byte(strings.Repeat("x", aetherControlResponseLimit+1)))
	}))
	defer server.Close()

	_, channelID := newAetherControlSyncFixture(t, server.URL, server.Client(), 90)
	_, err := SyncAetherIntegration(channelID)

	var controlErr *AetherControlError
	require.ErrorAs(t, err, &controlErr)
	assert.Equal(t, "capabilities", controlErr.Stage)
	assert.Equal(t, "response_too_large", controlErr.Code)
	assert.Equal(t, 1, requests)
}

func TestSyncAetherIntegrationDoesNotFollowControlRedirect(t *testing.T) {
	redirectedRequests := 0
	redirectTarget := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirectedRequests++
		_, _ = writer.Write([]byte(`{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":[]}`))
	}))
	defer redirectTarget.Close()

	origin := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "Bearer control-secret", request.Header.Get("Authorization"))
		if request.URL.Path == "/api/integrations/new-api/v1/capabilities" {
			http.Redirect(writer, request, redirectTarget.URL+request.URL.Path, http.StatusTemporaryRedirect)
			return
		}
		http.Error(writer, "unexpected request after redirect", http.StatusInternalServerError)
	}))
	defer origin.Close()

	_, channelID := newAetherControlSyncFixture(t, origin.URL, origin.Client(), 91)
	_, err := SyncAetherIntegration(channelID)

	var controlErr *AetherControlError
	require.ErrorAs(t, err, &controlErr)
	assert.Equal(t, "capabilities", controlErr.Stage)
	assert.Equal(t, "unexpected_status", controlErr.Code)
	assert.Equal(t, http.StatusTemporaryRedirect, controlErr.StatusCode)
	assert.Zero(t, redirectedRequests)
}

func TestAetherControlURLRequiresHTTPSWithoutCredentials(t *testing.T) {
	for _, baseURL := range []string{"http://aether.example", "https://user:password@aether.example"} {
		channel := &model.Channel{BaseURL: &baseURL}
		_, err := aetherControlURL(channel, "/api/integrations/new-api/v1/capabilities")
		assert.ErrorContains(t, err, "HTTPS")
	}
}

func newAetherControlSyncFixture(t *testing.T, baseURL string, client *http.Client, channelID int) (*gorm.DB, int) {
	t.Helper()
	previousDB := model.DB
	previousClient := aetherControlHTTPClient
	testName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := fmt.Sprintf("file:aether_control_fixture_%s_%d?mode=memory&cache=shared", testName, aetherControlSyncFixtureSequence.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	aetherControlHTTPClient = client
	t.Cleanup(func() {
		model.DB = previousDB
		aetherControlHTTPClient = previousClient
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}, &model.AetherIntegrationPendingCredentialRotation{}))
	channel := &model.Channel{Id: channelID, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default", BaseURL: &baseURL}
	require.NoError(t, db.Create(channel).Error)
	integration := &model.AetherIntegration{
		ChannelID:      channelID,
		InstanceID:     "aether-primary",
		RouteProfile:   "balanced",
		ExecutionMode:  model.AetherExecutionModeDirectChannel,
		Enabled:        true,
		ConfigRevision: 1,
	}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	return db, channelID
}
