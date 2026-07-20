package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type aetherIntegrationControllerRoundTripper func(*http.Request) (*http.Response, error)

func (roundTripper aetherIntegrationControllerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTripper(request)
}

func TestUpsertAetherIntegrationRejectsStaleRevisionWithoutLeakingSecrets(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_integration_controller_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}, &model.AetherIntegrationPendingCredentialRotation{}))
	require.NoError(t, db.Create(&model.Channel{Id: 51, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default"}).Error)

	createRecorder := httptest.NewRecorder()
	createContext, _ := gin.CreateTestContext(createRecorder)
	createContext.Params = gin.Params{{Key: "id", Value: "51"}}
	createContext.Request = httptest.NewRequest(http.MethodPut, "/api/channel/51/aether", strings.NewReader(`{"instance_id":"aether-primary","route_profile":"balanced","execution_mode":"direct_channel","enabled":true,"control_secret":"control-secret","relay_signing_secret":"relay-signing-secret"}`))
	createContext.Request.Header.Set("Content-Type", "application/json")

	UpsertAetherIntegration(createContext)

	require.Equal(t, http.StatusOK, createRecorder.Code)
	assert.NotContains(t, createRecorder.Body.String(), "control-secret")
	assert.NotContains(t, createRecorder.Body.String(), "relay-signing-secret")

	conflictRecorder := httptest.NewRecorder()
	conflictContext, _ := gin.CreateTestContext(conflictRecorder)
	conflictContext.Params = gin.Params{{Key: "id", Value: "51"}}
	conflictContext.Request = httptest.NewRequest(http.MethodPut, "/api/channel/51/aether", strings.NewReader(`{"base_revision":0,"route_profile":"low-cost","execution_mode":"direct_channel","enabled":true}`))
	conflictContext.Request.Header.Set("Content-Type", "application/json")

	UpsertAetherIntegration(conflictContext)

	require.Equal(t, http.StatusConflict, conflictRecorder.Code)
}

func TestUpsertAetherIntegrationRejectsBlankRouteProfileOnCreateAndUpdate(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_integration_blank_route_profile_controller_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}, &model.AetherIntegrationPendingCredentialRotation{}))
	require.NoError(t, db.Create(&model.Channel{Id: 58, Type: constant.ChannelTypeAether, Name: "aether-create", Key: "relay-key", Group: "default"}).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 59, Type: constant.ChannelTypeAether, Name: "aether-update", Key: "relay-key", Group: "default"}).Error)

	createRecorder := httptest.NewRecorder()
	createContext, _ := gin.CreateTestContext(createRecorder)
	createContext.Params = gin.Params{{Key: "id", Value: "58"}}
	createContext.Request = httptest.NewRequest(http.MethodPut, "/api/channel/58/aether", strings.NewReader(`{"instance_id":"aether-blank-create","route_profile":"   ","execution_mode":"direct_channel","enabled":true,"control_secret":"control-secret","relay_signing_secret":"relay-signing-secret"}`))
	createContext.Request.Header.Set("Content-Type", "application/json")

	UpsertAetherIntegration(createContext)

	require.Equal(t, http.StatusBadRequest, createRecorder.Code)
	assert.Contains(t, createRecorder.Body.String(), "aether route profile is required")
	var createCount int64
	require.NoError(t, db.Model(&model.AetherIntegration{}).Where("channel_id = ?", 58).Count(&createCount).Error)
	assert.Zero(t, createCount)

	integration := &model.AetherIntegration{
		ChannelID:      59,
		InstanceID:     "aether-blank-update",
		RouteProfile:   "balanced",
		ExecutionMode:  model.AetherExecutionModeDirectChannel,
		Enabled:        true,
		ConfigRevision: 1,
	}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	updateRecorder := httptest.NewRecorder()
	updateContext, _ := gin.CreateTestContext(updateRecorder)
	updateContext.Params = gin.Params{{Key: "id", Value: "59"}}
	updateContext.Request = httptest.NewRequest(http.MethodPut, "/api/channel/59/aether", strings.NewReader(`{"base_revision":1,"route_profile":"\t","execution_mode":"direct_channel","enabled":true}`))
	updateContext.Request.Header.Set("Content-Type", "application/json")

	UpsertAetherIntegration(updateContext)

	require.Equal(t, http.StatusBadRequest, updateRecorder.Code)
	assert.Contains(t, updateRecorder.Body.String(), "aether route profile is required")
	stored, err := model.GetAetherIntegrationByChannelID(59)
	require.NoError(t, err)
	assert.Equal(t, "balanced", stored.RouteProfile)
	assert.Equal(t, int64(1), stored.ConfigRevision)
}

func TestUpsertAetherIntegrationReturnsCurrentConfigDiffForStaleRevision(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_integration_conflict_diff_controller_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}, &model.AetherIntegrationPendingCredentialRotation{}))
	require.NoError(t, db.Create(&model.Channel{Id: 57, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default"}).Error)

	integration := &model.AetherIntegration{
		ChannelID:         57,
		InstanceID:        "aether-conflict-diff",
		RouteProfile:      "balanced",
		ExecutionMode:     model.AetherExecutionModeDirectChannel,
		Enabled:           true,
		CapabilityVersion: "0.1.0",
		ConfigRevision:    2,
	}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: "57"}}
	context.Request = httptest.NewRequest(http.MethodPut, "/api/channel/57/aether", strings.NewReader(`{"base_revision":1,"route_profile":"premium","execution_mode":"direct_channel","enabled":true,"capability_version":"0.1.0"}`))
	context.Request.Header.Set("Content-Type", "application/json")

	UpsertAetherIntegration(context)

	require.Equal(t, http.StatusConflict, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), "control-secret")
	assert.NotContains(t, recorder.Body.String(), "relay-signing-secret")
	var response struct {
		Data struct {
			CurrentRevision int64                               `json:"current_revision"`
			Current         model.AetherIntegrationSharedConfig `json:"current"`
			Diff            map[string]struct {
				Requested string `json:"requested"`
				Current   string `json:"current"`
			} `json:"diff"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, int64(2), response.Data.CurrentRevision)
	assert.Equal(t, "balanced", response.Data.Current.RouteProfile)
	require.Contains(t, response.Data.Diff, "route_profile")
	assert.Len(t, response.Data.Diff, 1)
	assert.Equal(t, "premium", response.Data.Diff["route_profile"].Requested)
	assert.Equal(t, "balanced", response.Data.Diff["route_profile"].Current)
}

func TestUpsertAetherIntegrationRotatesCredentialsInTheRevisionCAS(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_integration_rotation_controller_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}, &model.AetherIntegrationPendingCredentialRotation{}))
	rotationRequests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/integrations/new-api/v1/capabilities":
			_, _ = writer.Write([]byte(`{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":["credential_rotation_v1","control_hmac_v2"]}`))
		case "/api/integrations/new-api/v1/instances/aether-rotation":
			rotationRequests++
			var payload struct {
				CredentialRotation struct {
					ID                  string `json:"id"`
					TransitionExpiresAt int64  `json:"transition_expires_at"`
				} `json:"credential_rotation"`
			}
			require.NoError(t, common.DecodeJson(request.Body, &payload))
			body, err := common.Marshal(map[string]interface{}{
				"instance_id":   "aether-rotation",
				"base_revision": 1,
				"credential_rotation_ack": map[string]interface{}{
					"rotation_id":           payload.CredentialRotation.ID,
					"credential_revision":   2,
					"transition_expires_at": payload.CredentialRotation.TransitionExpiresAt,
					"state":                 "applied",
				},
			})
			require.NoError(t, err)
			_, _ = writer.Write(body)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	previousTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() {
		http.DefaultTransport = previousTransport
	})
	baseURL := server.URL
	require.NoError(t, db.Create(&model.Channel{Id: 52, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default", BaseURL: &baseURL}).Error)

	createRecorder := httptest.NewRecorder()
	createContext, _ := gin.CreateTestContext(createRecorder)
	createContext.Params = gin.Params{{Key: "id", Value: "52"}}
	createContext.Request = httptest.NewRequest(http.MethodPut, "/api/channel/52/aether", strings.NewReader(`{"instance_id":"aether-rotation","route_profile":"balanced","execution_mode":"direct_channel","enabled":true,"control_secret":"control-v1","relay_signing_secret":"relay-v1"}`))
	createContext.Request.Header.Set("Content-Type", "application/json")
	UpsertAetherIntegration(createContext)
	require.Equal(t, http.StatusOK, createRecorder.Code)

	rotateRecorder := httptest.NewRecorder()
	rotateContext, _ := gin.CreateTestContext(rotateRecorder)
	rotateContext.Params = gin.Params{{Key: "id", Value: "52"}}
	rotateContext.Request = httptest.NewRequest(http.MethodPut, "/api/channel/52/aether", strings.NewReader(`{"base_revision":1,"rotation_id":"rotation-test-v2","route_profile":"balanced","execution_mode":"direct_channel","enabled":true,"control_secret":"control-v2","relay_signing_secret":"relay-v2","secret_transition_seconds":60}`))
	rotateContext.Request.Header.Set("Content-Type", "application/json")
	UpsertAetherIntegration(rotateContext)
	require.Equal(t, http.StatusOK, rotateRecorder.Code)
	assert.NotContains(t, rotateRecorder.Body.String(), "control-v1")
	assert.NotContains(t, rotateRecorder.Body.String(), "relay-v1")
	assert.NotContains(t, rotateRecorder.Body.String(), "control-v2")
	assert.NotContains(t, rotateRecorder.Body.String(), "relay-v2")

	stored, err := model.GetAetherIntegrationByChannelID(52)
	require.NoError(t, err)
	require.Equal(t, int64(2), stored.ConfigRevision)
	controlSecrets, err := stored.ActiveControlSecrets(time.Now().UTC())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"control-v2", "control-v1"}, controlSecrets)

	staleRecorder := httptest.NewRecorder()
	staleContext, _ := gin.CreateTestContext(staleRecorder)
	staleContext.Params = gin.Params{{Key: "id", Value: "52"}}
	staleContext.Request = httptest.NewRequest(http.MethodPut, "/api/channel/52/aether", strings.NewReader(`{"base_revision":1,"rotation_id":"rotation-stale","route_profile":"balanced","execution_mode":"direct_channel","enabled":true,"control_secret":"control-stale","relay_signing_secret":"relay-stale"}`))
	staleContext.Request.Header.Set("Content-Type", "application/json")
	UpsertAetherIntegration(staleContext)
	require.Equal(t, http.StatusConflict, staleRecorder.Code)

	stored, err = model.GetAetherIntegrationByChannelID(52)
	require.NoError(t, err)
	controlSecret, relaySigningSecret, err := stored.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-v2", controlSecret)
	assert.Equal(t, "relay-v2", relaySigningSecret)
	assert.Equal(t, 1, rotationRequests)
}

func TestUpsertAetherIntegrationRotatesExistingSecretsRemoteFirstAfterVerifiedAck(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_integration_remote_rotation_controller_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}, &model.AetherIntegrationPendingCredentialRotation{}))

	remoteRotationReceived := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/integrations/new-api/v1/capabilities":
			require.Equal(t, "Bearer control-v1", request.Header.Get("Authorization"))
			require.Equal(t, "aether-controller", request.Header.Get(service.AetherInstanceIDHeader))
			_, _ = writer.Write([]byte(`{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":["credential_rotation_v1","control_hmac_v2"]}`))
		case "/api/integrations/new-api/v1/instances/aether-controller":
			require.Equal(t, http.MethodPut, request.Method)
			require.Empty(t, request.Header.Get("Authorization"))
			require.Equal(t, "v2", request.Header.Get("X-Aether-Signature-Version"))
			var payload struct {
				BaseRevision       int64 `json:"base_revision"`
				CredentialRotation struct {
					ID                  string `json:"id"`
					ControlSecret       string `json:"control_secret"`
					RelaySigningSecret  string `json:"relay_signing_secret"`
					TransitionExpiresAt int64  `json:"transition_expires_at"`
				} `json:"credential_rotation"`
			}
			require.NoError(t, common.DecodeJson(request.Body, &payload))
			assert.Equal(t, int64(7), payload.BaseRevision)
			assert.NotEmpty(t, payload.CredentialRotation.ID)
			assert.NotContains(t, payload.CredentialRotation.ID, "control-v2")
			assert.Equal(t, "control-v2", payload.CredentialRotation.ControlSecret)
			assert.Equal(t, "relay-v2", payload.CredentialRotation.RelaySigningSecret)
			stored, err := model.GetAetherIntegrationByChannelID(144)
			require.NoError(t, err)
			controlSecret, relaySigningSecret, err := stored.Secrets()
			require.NoError(t, err)
			assert.Equal(t, "control-v1", controlSecret)
			assert.Equal(t, "relay-v1", relaySigningSecret)
			remoteRotationReceived = true
			response := struct {
				InstanceID            string `json:"instance_id"`
				BaseRevision          int64  `json:"base_revision"`
				CredentialRotationAck struct {
					RotationID          string `json:"rotation_id"`
					CredentialRevision  int64  `json:"credential_revision"`
					TransitionExpiresAt int64  `json:"transition_expires_at"`
					State               string `json:"state"`
				} `json:"credential_rotation_ack"`
			}{
				InstanceID:   "aether-controller",
				BaseRevision: 8,
			}
			response.CredentialRotationAck.RotationID = payload.CredentialRotation.ID
			response.CredentialRotationAck.CredentialRevision = 4
			response.CredentialRotationAck.TransitionExpiresAt = payload.CredentialRotation.TransitionExpiresAt
			response.CredentialRotationAck.State = "applied"
			body, err := common.Marshal(response)
			require.NoError(t, err)
			_, _ = writer.Write(body)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	previousTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() {
		http.DefaultTransport = previousTransport
	})
	baseURL := server.URL
	require.NoError(t, db.Create(&model.Channel{Id: 144, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default", BaseURL: &baseURL}).Error)
	integration := &model.AetherIntegration{
		ChannelID:            144,
		InstanceID:           "aether-controller",
		RouteProfile:         "balanced",
		ExecutionMode:        model.AetherExecutionModeDirectChannel,
		Enabled:              true,
		ConfigRevision:       1,
		RemoteConfigRevision: 7,
	}
	require.NoError(t, integration.SetSecrets("control-v1", "relay-v1"))
	require.NoError(t, db.Create(integration).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: "144"}}
	context.Request = httptest.NewRequest(http.MethodPut, "/api/channel/144/aether", strings.NewReader(`{"base_revision":1,"control_secret":"control-v2","relay_signing_secret":"relay-v2","secret_transition_seconds":60}`))
	context.Request.Header.Set("Content-Type", "application/json")

	UpsertAetherIntegration(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.True(t, remoteRotationReceived)
	assert.NotContains(t, recorder.Body.String(), "control-v1")
	assert.NotContains(t, recorder.Body.String(), "relay-v1")
	assert.NotContains(t, recorder.Body.String(), "control-v2")
	assert.NotContains(t, recorder.Body.String(), "relay-v2")
	stored, err := model.GetAetherIntegrationByChannelID(144)
	require.NoError(t, err)
	assert.Equal(t, int64(2), stored.ConfigRevision)
	assert.Equal(t, int64(8), stored.RemoteConfigRevision)
	controlSecrets, err := stored.ActiveControlSecrets(time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, []string{"control-v2", "control-v1"}, controlSecrets)
}

func TestUpsertAetherIntegrationRejectsSharedConfigMutationDuringRemoteCredentialRotation(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_integration_remote_rotation_shared_config_controller_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}, &model.AetherIntegrationPendingCredentialRotation{}))
	require.NoError(t, db.Create(&model.Channel{Id: 145, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default"}).Error)
	integration := &model.AetherIntegration{
		ChannelID:      145,
		InstanceID:     "aether-controller-shared-config",
		RouteProfile:   "balanced",
		ExecutionMode:  model.AetherExecutionModeDirectChannel,
		Enabled:        true,
		ConfigRevision: 1,
	}
	require.NoError(t, integration.SetSecrets("control-v1", "relay-v1"))
	require.NoError(t, db.Create(integration).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: "145"}}
	context.Request = httptest.NewRequest(http.MethodPut, "/api/channel/145/aether", strings.NewReader(`{"base_revision":1,"route_profile":"low-cost","control_secret":"control-v2","relay_signing_secret":"relay-v2"}`))
	context.Request.Header.Set("Content-Type", "application/json")

	UpsertAetherIntegration(context)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "aether remote credential rotation cannot change shared configuration")
	stored, err := model.GetAetherIntegrationByChannelID(145)
	require.NoError(t, err)
	assert.Equal(t, "balanced", stored.RouteProfile)
	controlSecret, relaySigningSecret, err := stored.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-v1", controlSecret)
	assert.Equal(t, "relay-v1", relaySigningSecret)
}

func TestUpsertAetherIntegrationMapsRemoteRotationConflictAndUnsupportedCapability(t *testing.T) {
	tests := []struct {
		name             string
		databaseName     string
		capabilities     string
		rotationStatus   int
		expectedMessage  string
		expectedPending  int64
		expectedPutCalls int
	}{
		{
			name:             "remote conflict",
			databaseName:     "aether_controller_rotation_remote_conflict",
			capabilities:     `{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":["credential_rotation_v1","control_hmac_v2"]}`,
			rotationStatus:   http.StatusConflict,
			expectedMessage:  "aether remote configuration conflict",
			expectedPending:  1,
			expectedPutCalls: 1,
		},
		{
			name:             "unsupported capability",
			databaseName:     "aether_controller_rotation_unsupported_capability",
			capabilities:     `{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":["control_hmac_v2"]}`,
			rotationStatus:   http.StatusOK,
			expectedMessage:  "aether remote credential rotation is not supported",
			expectedPending:  0,
			expectedPutCalls: 0,
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previousDB := model.DB
			db, err := gorm.Open(sqlite.Open("file:"+test.databaseName+"?mode=memory&cache=shared"), &gorm.Config{})
			require.NoError(t, err)
			model.DB = db
			t.Cleanup(func() {
				model.DB = previousDB
			})
			require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}, &model.AetherIntegrationPendingCredentialRotation{}))
			putCalls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/api/integrations/new-api/v1/capabilities" {
					_, _ = writer.Write([]byte(test.capabilities))
					return
				}
				putCalls++
				writer.WriteHeader(test.rotationStatus)
			}))
			defer server.Close()
			previousTransport := http.DefaultTransport
			http.DefaultTransport = server.Client().Transport
			t.Cleanup(func() {
				http.DefaultTransport = previousTransport
			})
			baseURL := server.URL
			channelID := 160 + index
			require.NoError(t, db.Create(&model.Channel{Id: channelID, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default", BaseURL: &baseURL}).Error)
			integration := &model.AetherIntegration{
				ChannelID:            channelID,
				InstanceID:           "aether-controller-error",
				ExecutionMode:        model.AetherExecutionModeDirectChannel,
				Enabled:              true,
				ConfigRevision:       1,
				RemoteConfigRevision: 7,
			}
			require.NoError(t, integration.SetSecrets("control-v1", "relay-v1"))
			require.NoError(t, db.Create(integration).Error)

			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Params = gin.Params{{Key: "id", Value: strconv.Itoa(channelID)}}
			context.Request = httptest.NewRequest(http.MethodPut, "/api/channel/rotation/aether", strings.NewReader(`{"base_revision":1,"rotation_id":"rotation-error","control_secret":"control-v2","relay_signing_secret":"relay-v2","secret_transition_seconds":60}`))
			context.Request.Header.Set("Content-Type", "application/json")

			UpsertAetherIntegration(context)

			require.Equal(t, http.StatusConflict, recorder.Code)
			assert.Contains(t, recorder.Body.String(), test.expectedMessage)
			assert.NotContains(t, recorder.Body.String(), "control-v1")
			assert.NotContains(t, recorder.Body.String(), "relay-v1")
			assert.NotContains(t, recorder.Body.String(), "control-v2")
			assert.NotContains(t, recorder.Body.String(), "relay-v2")
			assert.Equal(t, test.expectedPutCalls, putCalls)
			var pendingCount int64
			require.NoError(t, db.Model(&model.AetherIntegrationPendingCredentialRotation{}).Count(&pendingCount).Error)
			assert.Equal(t, test.expectedPending, pendingCount)
			stored, err := model.GetAetherIntegrationByChannelID(channelID)
			require.NoError(t, err)
			controlSecret, relaySigningSecret, err := stored.Secrets()
			require.NoError(t, err)
			assert.Equal(t, "control-v1", controlSecret)
			assert.Equal(t, "relay-v1", relaySigningSecret)
		})
	}
}

func TestUpsertAetherIntegrationReturnsGatewayShapeOnRemoteRotationTimeout(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_controller_rotation_timeout?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}, &model.AetherIntegrationPendingCredentialRotation{}))
	putAttempted := false
	previousTransport := http.DefaultTransport
	http.DefaultTransport = aetherIntegrationControllerRoundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/integrations/new-api/v1/capabilities":
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":["credential_rotation_v1","control_hmac_v2"]}`)),
				Request:    request,
			}, nil
		case "/api/integrations/new-api/v1/instances/aether-controller-timeout":
			putAttempted = true
			return nil, context.DeadlineExceeded
		default:
			return nil, context.Canceled
		}
	})
	t.Cleanup(func() {
		http.DefaultTransport = previousTransport
	})
	baseURL := "https://aether.example"
	require.NoError(t, db.Create(&model.Channel{Id: 170, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default", BaseURL: &baseURL}).Error)
	integration := &model.AetherIntegration{
		ChannelID:            170,
		InstanceID:           "aether-controller-timeout",
		ExecutionMode:        model.AetherExecutionModeDirectChannel,
		Enabled:              true,
		ConfigRevision:       1,
		RemoteConfigRevision: 7,
	}
	require.NoError(t, integration.SetSecrets("control-v1", "relay-v1"))
	require.NoError(t, db.Create(integration).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: "170"}}
	context.Request = httptest.NewRequest(http.MethodPut, "/api/channel/170/aether", strings.NewReader(`{"base_revision":1,"rotation_id":"rotation-timeout","control_secret":"control-v2","relay_signing_secret":"relay-v2","secret_transition_seconds":60}`))
	context.Request.Header.Set("Content-Type", "application/json")

	UpsertAetherIntegration(context)

	require.Equal(t, http.StatusBadGateway, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "failed to rotate aether integration credentials")
	assert.NotContains(t, recorder.Body.String(), "context deadline exceeded")
	require.True(t, putAttempted)
	var pending model.AetherIntegrationPendingCredentialRotation
	require.NoError(t, db.Where("rotation_id = ?", "rotation-timeout").First(&pending).Error)
	stored, err := model.GetAetherIntegrationByChannelID(170)
	require.NoError(t, err)
	controlSecret, relaySigningSecret, err := stored.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-v1", controlSecret)
	assert.Equal(t, "relay-v1", relaySigningSecret)
}

func TestUpsertAetherIntegrationRetriesTheSamePendingRemoteRotation(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_controller_rotation_retry?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}, &model.AetherIntegrationPendingCredentialRotation{}))
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
		var payload struct {
			CredentialRotation struct {
				ID                  string `json:"id"`
				TransitionExpiresAt int64  `json:"transition_expires_at"`
			} `json:"credential_rotation"`
		}
		require.NoError(t, common.DecodeJson(request.Body, &payload))
		body, err := common.Marshal(map[string]interface{}{
			"instance_id":   "aether-controller-retry",
			"base_revision": 8,
			"credential_rotation_ack": map[string]interface{}{
				"rotation_id":           payload.CredentialRotation.ID,
				"credential_revision":   4,
				"transition_expires_at": payload.CredentialRotation.TransitionExpiresAt,
				"state":                 "applied",
			},
		})
		require.NoError(t, err)
		_, _ = writer.Write(body)
	}))
	defer server.Close()
	previousTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() {
		http.DefaultTransport = previousTransport
	})
	baseURL := server.URL
	require.NoError(t, db.Create(&model.Channel{Id: 171, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default", BaseURL: &baseURL}).Error)
	integration := &model.AetherIntegration{
		ChannelID:            171,
		InstanceID:           "aether-controller-retry",
		ExecutionMode:        model.AetherExecutionModeDirectChannel,
		Enabled:              true,
		ConfigRevision:       1,
		RemoteConfigRevision: 7,
	}
	require.NoError(t, integration.SetSecrets("control-v1", "relay-v1"))
	require.NoError(t, db.Create(integration).Error)

	requestBody := `{"base_revision":1,"control_secret":"control-v2","relay_signing_secret":"relay-v2","secret_transition_seconds":60}`
	firstRecorder := httptest.NewRecorder()
	firstContext, _ := gin.CreateTestContext(firstRecorder)
	firstContext.Params = gin.Params{{Key: "id", Value: "171"}}
	firstContext.Request = httptest.NewRequest(http.MethodPut, "/api/channel/171/aether", strings.NewReader(requestBody))
	firstContext.Request.Header.Set("Content-Type", "application/json")
	UpsertAetherIntegration(firstContext)
	require.Equal(t, http.StatusBadGateway, firstRecorder.Code)

	secondRecorder := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondRecorder)
	secondContext.Params = gin.Params{{Key: "id", Value: "171"}}
	secondContext.Request = httptest.NewRequest(http.MethodPut, "/api/channel/171/aether", strings.NewReader(requestBody))
	secondContext.Request.Header.Set("Content-Type", "application/json")
	UpsertAetherIntegration(secondContext)

	require.Equal(t, http.StatusOK, secondRecorder.Code)
	assert.Equal(t, 2, putAttempts)
	stored, err := model.GetAetherIntegrationByChannelID(171)
	require.NoError(t, err)
	controlSecret, relaySigningSecret, err := stored.Secrets()
	require.NoError(t, err)
	assert.Equal(t, "control-v2", controlSecret)
	assert.Equal(t, "relay-v2", relaySigningSecret)
	var pendingCount int64
	require.NoError(t, db.Model(&model.AetherIntegrationPendingCredentialRotation{}).Count(&pendingCount).Error)
	assert.Zero(t, pendingCount)
}

func TestUpsertAetherIntegrationSecretOnlyPreservesSharedConfig(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_integration_secret_only_controller_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}, &model.AetherIntegrationPendingCredentialRotation{}))
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/integrations/new-api/v1/capabilities" {
			_, _ = writer.Write([]byte(`{"supported_modes":["direct_channel"],"supported_formats":["openai"],"capability_version":"0.1.0","features":["credential_rotation_v1","control_hmac_v2"]}`))
			return
		}
		var payload struct {
			CredentialRotation struct {
				ID                  string `json:"id"`
				TransitionExpiresAt int64  `json:"transition_expires_at"`
			} `json:"credential_rotation"`
		}
		require.NoError(t, common.DecodeJson(request.Body, &payload))
		body, err := common.Marshal(map[string]interface{}{
			"instance_id":   "aether-secret-only",
			"base_revision": 1,
			"credential_rotation_ack": map[string]interface{}{
				"rotation_id":           payload.CredentialRotation.ID,
				"credential_revision":   2,
				"transition_expires_at": payload.CredentialRotation.TransitionExpiresAt,
				"state":                 "applied",
			},
		})
		require.NoError(t, err)
		_, _ = writer.Write(body)
	}))
	defer server.Close()
	previousTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() {
		http.DefaultTransport = previousTransport
	})
	baseURL := server.URL
	require.NoError(t, db.Create(&model.Channel{Id: 53, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default", BaseURL: &baseURL}).Error)

	integration := &model.AetherIntegration{
		ChannelID:         53,
		InstanceID:        "aether-secret-only",
		RouteProfile:      "preserve-me",
		ExecutionMode:     model.AetherExecutionModeDirectChannel,
		Enabled:           true,
		CapabilityVersion: "capability-v1",
		ConfigRevision:    2,
	}
	require.NoError(t, integration.SetSecrets("control-v1", "relay-v1"))
	require.NoError(t, db.Create(integration).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: "53"}}
	context.Request = httptest.NewRequest(http.MethodPut, "/api/channel/53/aether", strings.NewReader("{\"base_revision\":2,\"control_secret\":\"control-v2\",\"relay_signing_secret\":\"relay-v2\",\"secret_transition_seconds\":60}"))
	context.Request.Header.Set("Content-Type", "application/json")

	UpsertAetherIntegration(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var stored model.AetherIntegration
	require.NoError(t, db.First(&stored, integration.Id).Error)
	assert.Equal(t, "preserve-me", stored.RouteProfile)
	assert.Equal(t, model.AetherExecutionModeDirectChannel, stored.ExecutionMode)
	assert.True(t, stored.Enabled)
	assert.Equal(t, "capability-v1", stored.CapabilityVersion)
	assert.Equal(t, int64(3), stored.ConfigRevision)
}

func TestUpsertAetherIntegrationRevokeOnlyPreservesSharedConfig(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_integration_revoke_only_controller_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}, &model.AetherIntegrationPendingCredentialRotation{}))
	require.NoError(t, db.Create(&model.Channel{Id: 54, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default"}).Error)

	previousControlSecret, err := common.EncryptAetherSecret("control-v1")
	require.NoError(t, err)
	previousRelaySigningSecret, err := common.EncryptAetherSecret("relay-v1")
	require.NoError(t, err)
	integration := &model.AetherIntegration{
		ChannelID:                           54,
		InstanceID:                          "aether-revoke-only",
		RouteProfile:                        "preserve-me",
		ExecutionMode:                       model.AetherExecutionModeDirectChannel,
		Enabled:                             true,
		CapabilityVersion:                   "capability-v1",
		ConfigRevision:                      2,
		PreviousControlSecretEncrypted:      previousControlSecret,
		PreviousRelaySigningSecretEncrypted: previousRelaySigningSecret,
		TransitionSecretsExpireAt:           time.Now().UTC().Add(time.Minute).Unix(),
	}
	require.NoError(t, integration.SetSecrets("control-v2", "relay-v2"))
	integration.PreviousControlSecretEncrypted = previousControlSecret
	integration.PreviousRelaySigningSecretEncrypted = previousRelaySigningSecret
	integration.TransitionSecretsExpireAt = time.Now().UTC().Add(time.Minute).Unix()
	require.NoError(t, db.Create(integration).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: "54"}}
	context.Request = httptest.NewRequest(http.MethodPut, "/api/channel/54/aether", strings.NewReader("{\"base_revision\":2,\"revoke_transition_secrets\":true}"))
	context.Request.Header.Set("Content-Type", "application/json")

	UpsertAetherIntegration(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var stored model.AetherIntegration
	require.NoError(t, db.First(&stored, integration.Id).Error)
	assert.Equal(t, "preserve-me", stored.RouteProfile)
	assert.Equal(t, model.AetherExecutionModeDirectChannel, stored.ExecutionMode)
	assert.True(t, stored.Enabled)
	assert.Equal(t, "capability-v1", stored.CapabilityVersion)
	assert.Empty(t, stored.PreviousControlSecretEncrypted)
	assert.Empty(t, stored.PreviousRelaySigningSecretEncrypted)
	assert.Zero(t, stored.TransitionSecretsExpireAt)
	assert.Equal(t, int64(3), stored.ConfigRevision)
}

func TestUpsertAetherIntegrationZeroBaseRevisionUsesSafeConflictShape(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_integration_zero_revision_conflict_controller_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}, &model.AetherIntegrationPendingCredentialRotation{}))
	require.NoError(t, db.Create(&model.Channel{Id: 55, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default"}).Error)

	integration := &model.AetherIntegration{
		ChannelID:         55,
		InstanceID:        "aether-conflict-shape",
		RouteProfile:      "preserve-me",
		ExecutionMode:     model.AetherExecutionModeDirectChannel,
		Enabled:           true,
		CapabilityVersion: "capability-v1",
		ConfigRevision:    2,
	}
	require.NoError(t, integration.SetSecrets("control-v1", "relay-v1"))
	require.NoError(t, db.Create(integration).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: "55"}}
	context.Request = httptest.NewRequest(http.MethodPut, "/api/channel/55/aether", strings.NewReader("{\"base_revision\":0}"))
	context.Request.Header.Set("Content-Type", "application/json")

	UpsertAetherIntegration(context)

	require.Equal(t, http.StatusConflict, recorder.Code)
	var response struct {
		Data struct {
			CurrentRevision int64                  `json:"current_revision"`
			Current         map[string]interface{} `json:"current"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, int64(2), response.Data.CurrentRevision)
	assert.Equal(t, map[string]interface{}{
		"route_profile":      "preserve-me",
		"execution_mode":     model.AetherExecutionModeDirectChannel,
		"enabled":            true,
		"capability_version": "capability-v1",
	}, response.Data.Current)
}

func TestGetAetherIntegrationExpiresTransitionSecretsBeforePublicResponse(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_integration_expired_transition_controller_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.AetherIntegration{}, &model.AetherIntegrationPendingCredentialRotation{}))
	require.NoError(t, db.Create(&model.Channel{Id: 56, Type: constant.ChannelTypeAether, Name: "aether", Key: "relay-key", Group: "default"}).Error)

	previousControlSecret, err := common.EncryptAetherSecret("control-v1")
	require.NoError(t, err)
	previousRelaySigningSecret, err := common.EncryptAetherSecret("relay-v1")
	require.NoError(t, err)
	integration := &model.AetherIntegration{
		ChannelID:                           56,
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

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: "56"}}
	context.Request = httptest.NewRequest(http.MethodGet, "/api/channel/56/aether", nil)

	GetAetherIntegration(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data struct {
			HasTransitionControlSecret      bool  `json:"has_transition_control_secret"`
			HasTransitionRelaySigningSecret bool  `json:"has_transition_relay_signing_secret"`
			TransitionSecretsExpireAt       int64 `json:"transition_secrets_expire_at"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Data.HasTransitionControlSecret)
	assert.False(t, response.Data.HasTransitionRelaySigningSecret)
	assert.Zero(t, response.Data.TransitionSecretsExpireAt)

	var stored model.AetherIntegration
	require.NoError(t, db.First(&stored, integration.Id).Error)
	assert.Empty(t, stored.PreviousControlSecretEncrypted)
	assert.Empty(t, stored.PreviousRelaySigningSecretEncrypted)
	assert.Zero(t, stored.TransitionSecretsExpireAt)
}
