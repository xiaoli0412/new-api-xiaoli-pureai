package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type aetherIntegrationRequest struct {
	BaseRevision            int64   `json:"base_revision"`
	RotationID              string  `json:"rotation_id"`
	InstanceID              string  `json:"instance_id"`
	RouteProfile            *string `json:"route_profile"`
	ExecutionMode           *string `json:"execution_mode"`
	Enabled                 *bool   `json:"enabled"`
	CapabilityVersion       *string `json:"capability_version"`
	ControlSecret           string  `json:"control_secret"`
	RelaySigningSecret      string  `json:"relay_signing_secret"`
	SecretTransitionSeconds *int64  `json:"secret_transition_seconds"`
	RevokeTransitionSecrets bool    `json:"revoke_transition_secrets"`
}

type aetherIntegrationResponse struct {
	Id                              int64  `json:"id"`
	ChannelID                       int    `json:"channel_id"`
	InstanceID                      string `json:"instance_id"`
	RouteProfile                    string `json:"route_profile"`
	ExecutionMode                   string `json:"execution_mode"`
	Enabled                         bool   `json:"enabled"`
	CapabilityVersion               string `json:"capability_version"`
	ConfigRevision                  int64  `json:"config_revision"`
	RemoteConfigRevision            int64  `json:"remote_config_revision"`
	HasControlSecret                bool   `json:"has_control_secret"`
	HasRelaySigningSecret           bool   `json:"has_relay_signing_secret"`
	HasTransitionControlSecret      bool   `json:"has_transition_control_secret"`
	HasTransitionRelaySigningSecret bool   `json:"has_transition_relay_signing_secret"`
	TransitionSecretsExpireAt       int64  `json:"transition_secrets_expire_at"`
	LastSyncTime                    int64  `json:"last_sync_time"`
	LastHealthTime                  int64  `json:"last_health_time"`
	LastHealthStatus                string `json:"last_health_status"`
}

const (
	aetherIntegrationDefaultSecretTransition = 5 * time.Minute
	aetherIntegrationMaxSecretTransition     = time.Hour
)

func GetAetherIntegration(c *gin.Context) {
	channelID, ok := aetherChannelIDFromRequest(c)
	if !ok {
		return
	}
	integration, err := model.GetAetherIntegrationByChannelID(channelID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "aether integration is not configured"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to load aether integration"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": aetherIntegrationPublic(integration)})
}

func UpsertAetherIntegration(c *gin.Context) {
	channelID, ok := aetherChannelIDFromRequest(c)
	if !ok {
		return
	}
	var request aetherIntegrationRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid aether integration payload"})
		return
	}
	request.InstanceID = strings.TrimSpace(request.InstanceID)
	request.RotationID = strings.TrimSpace(request.RotationID)
	request.ControlSecret = strings.TrimSpace(request.ControlSecret)
	request.RelaySigningSecret = strings.TrimSpace(request.RelaySigningSecret)
	routeProfile := aetherIntegrationRequestString(request.RouteProfile)
	if request.RouteProfile != nil && routeProfile == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "aether route profile is required"})
		return
	}
	executionMode := model.AetherExecutionModeDirectChannel
	if request.ExecutionMode != nil {
		executionMode = strings.TrimSpace(*request.ExecutionMode)
		if executionMode == "" {
			executionMode = model.AetherExecutionModeDirectChannel
		}
	}
	enabled := request.Enabled != nil && *request.Enabled
	capabilityVersion := aetherIntegrationRequestString(request.CapabilityVersion)
	if err := model.ValidateAetherExecutionMode(executionMode); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	if (request.ControlSecret == "") != (request.RelaySigningSecret == "") {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "aether control and relay signing secrets must be updated together"})
		return
	}
	if request.SecretTransitionSeconds != nil && (*request.SecretTransitionSeconds < 0 || *request.SecretTransitionSeconds > int64(aetherIntegrationMaxSecretTransition/time.Second)) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "aether secret transition duration is invalid"})
		return
	}
	if request.SecretTransitionSeconds != nil && request.ControlSecret == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "aether secret transition duration requires new secrets"})
		return
	}
	if request.ControlSecret != "" && request.SecretTransitionSeconds != nil && *request.SecretTransitionSeconds == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "aether remote credential rotation requires a positive transition duration"})
		return
	}
	if request.RevokeTransitionSecrets && request.ControlSecret != "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "aether credential rotation cannot revoke and replace secrets together"})
		return
	}

	integration, err := model.GetAetherIntegrationByChannelID(channelID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if request.RevokeTransitionSecrets || request.SecretTransitionSeconds != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "aether secret transition requires an existing integration"})
			return
		}
		if request.InstanceID == "" || routeProfile == "" || request.ControlSecret == "" || request.RelaySigningSecret == "" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "instance ID, route profile, and both aether secrets are required"})
			return
		}
		integration = &model.AetherIntegration{
			ChannelID:         channelID,
			InstanceID:        request.InstanceID,
			RouteProfile:      routeProfile,
			ExecutionMode:     executionMode,
			Enabled:           enabled,
			CapabilityVersion: capabilityVersion,
			ConfigRevision:    1,
			CreatedTime:       common.GetTimestamp(),
			UpdatedTime:       common.GetTimestamp(),
		}
		if err := integration.SetSecrets(request.ControlSecret, request.RelaySigningSecret); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to protect aether secrets"})
			return
		}
		if err := model.DB.Create(integration).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to create aether integration"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": aetherIntegrationPublic(integration)})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to load aether integration"})
		return
	}
	sharedConfig := model.AetherIntegrationSharedConfig{
		RouteProfile:      routeProfile,
		ExecutionMode:     executionMode,
		Enabled:           enabled,
		CapabilityVersion: capabilityVersion,
	}
	hasCredentialReplacement := request.ControlSecret != ""
	if hasCredentialReplacement && aetherIntegrationRequestChangesSharedConfig(request, integration, routeProfile, executionMode, enabled, capabilityVersion) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "aether remote credential rotation cannot change shared configuration"})
		return
	}
	if hasCredentialReplacement || (request.RevokeTransitionSecrets &&
		request.RouteProfile == nil &&
		request.ExecutionMode == nil &&
		request.Enabled == nil &&
		request.CapabilityVersion == nil) {
		sharedConfig = aetherIntegrationSharedConfig(integration)
	}
	if request.BaseRevision <= 0 {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "aether integration configuration conflict", "data": model.NewAetherIntegrationConflict(integration, sharedConfig)})
		return
	}
	if request.InstanceID != "" && request.InstanceID != integration.InstanceID {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "aether instance ID cannot be changed after creation"})
		return
	}
	if hasCredentialReplacement {
		rotationID := aetherIntegrationCredentialRotationID(integration, request.BaseRevision, request.RotationID)
		transitionExpiresAt := time.Time{}
		pending, pendingErr := model.GetAetherIntegrationPendingCredentialRotation(integration.Id)
		switch {
		case pendingErr == nil:
			if pending.RotationID != rotationID || pending.BaseConfigRevision != request.BaseRevision || pending.ReservedConfigRevision != integration.ConfigRevision {
				c.JSON(http.StatusConflict, gin.H{"success": false, "message": "aether integration configuration conflict", "data": model.NewAetherIntegrationConflict(integration, sharedConfig)})
				return
			}
			transitionExpiresAt = time.Unix(pending.TransitionSecretsExpireAt, 0).UTC()
		case errors.Is(pendingErr, gorm.ErrRecordNotFound):
			if request.BaseRevision != integration.ConfigRevision {
				c.JSON(http.StatusConflict, gin.H{"success": false, "message": "aether integration configuration conflict", "data": model.NewAetherIntegrationConflict(integration, sharedConfig)})
				return
			}
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to load aether credential rotation"})
			return
		}
		if transitionExpiresAt.IsZero() {
			transition := aetherIntegrationDefaultSecretTransition
			if request.SecretTransitionSeconds != nil {
				transition = time.Duration(*request.SecretTransitionSeconds) * time.Second
			}
			transitionExpiresAt = time.Now().UTC().Add(transition)
		}
		if transitionExpiresAt.Unix() <= time.Now().UTC().Unix() {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "aether integration configuration conflict", "data": model.NewAetherIntegrationConflict(integration, sharedConfig)})
			return
		}
		updated, err := service.RotateAetherIntegrationCredentialsRemoteFirst(
			channelID,
			request.BaseRevision,
			model.AetherIntegrationPendingCredentialRotationRequest{
				RotationID:          rotationID,
				ControlSecret:       request.ControlSecret,
				RelaySigningSecret:  request.RelaySigningSecret,
				TransitionExpiresAt: transitionExpiresAt,
			},
		)
		if err != nil {
			writeAetherCredentialRotationError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": aetherIntegrationPublic(updated)})
		return
	}
	var secretRotation *model.AetherIntegrationSecretRotation
	if request.RevokeTransitionSecrets {
		secretRotation = &model.AetherIntegrationSecretRotation{RevokePrevious: true}
	}
	updated, conflict, err := model.UpdateAetherIntegrationSharedConfigWithSecretRotation(integration, request.BaseRevision, sharedConfig, secretRotation)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	if conflict != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "aether integration configuration conflict", "data": conflict})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": aetherIntegrationPublic(updated)})
}

func SyncAetherIntegration(c *gin.Context) {
	channelID, ok := aetherChannelIDFromRequest(c)
	if !ok {
		return
	}
	updated, err := service.SyncAetherIntegration(channelID)
	if err != nil {
		var conflict *service.AetherControlConflictError
		if errors.As(err, &conflict) {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "aether remote configuration conflict", "data": conflict})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "failed to synchronize aether integration"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": aetherIntegrationPublic(updated)})
}

func aetherChannelIDFromRequest(c *gin.Context) (int, bool) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid aether channel ID"})
		return 0, false
	}
	channel, err := model.GetChannelById(channelID, false)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "channel not found"})
		return 0, false
	}
	if channel.Type != constant.ChannelTypeAether {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "channel is not an aether channel"})
		return 0, false
	}
	return channelID, true
}

func aetherIntegrationPublic(integration *model.AetherIntegration) aetherIntegrationResponse {
	hasTransitionSecrets := integration.TransitionSecretsExpireAt > time.Now().UTC().Unix()
	transitionSecretsExpireAt := int64(0)
	if hasTransitionSecrets {
		transitionSecretsExpireAt = integration.TransitionSecretsExpireAt
	}
	return aetherIntegrationResponse{
		Id:                              int64(integration.Id),
		ChannelID:                       integration.ChannelID,
		InstanceID:                      integration.InstanceID,
		RouteProfile:                    integration.RouteProfile,
		ExecutionMode:                   integration.ExecutionMode,
		Enabled:                         integration.Enabled,
		CapabilityVersion:               integration.CapabilityVersion,
		ConfigRevision:                  integration.ConfigRevision,
		RemoteConfigRevision:            integration.RemoteConfigRevision,
		HasControlSecret:                integration.ControlSecretEncrypted != "",
		HasRelaySigningSecret:           integration.RelaySigningSecretEncrypted != "",
		HasTransitionControlSecret:      hasTransitionSecrets && integration.PreviousControlSecretEncrypted != "",
		HasTransitionRelaySigningSecret: hasTransitionSecrets && integration.PreviousRelaySigningSecretEncrypted != "",
		TransitionSecretsExpireAt:       transitionSecretsExpireAt,
		LastSyncTime:                    integration.LastSyncTime,
		LastHealthTime:                  integration.LastHealthTime,
		LastHealthStatus:                integration.LastHealthStatus,
	}
}

func aetherIntegrationRequestString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func aetherIntegrationRequestChangesSharedConfig(
	request aetherIntegrationRequest,
	integration *model.AetherIntegration,
	routeProfile string,
	executionMode string,
	enabled bool,
	capabilityVersion string,
) bool {
	if request.RouteProfile != nil && routeProfile != integration.RouteProfile {
		return true
	}
	if request.ExecutionMode != nil && executionMode != integration.ExecutionMode {
		return true
	}
	if request.Enabled != nil && enabled != integration.Enabled {
		return true
	}
	return request.CapabilityVersion != nil && capabilityVersion != integration.CapabilityVersion
}

func aetherIntegrationCredentialRotationID(integration *model.AetherIntegration, baseRevision int64, requestedRotationID string) string {
	if requestedRotationID != "" {
		return requestedRotationID
	}
	payload := strings.Join([]string{
		"new-api-aether-admin-credential-rotation-v1",
		strconv.Itoa(integration.Id),
		integration.InstanceID,
		strconv.FormatInt(baseRevision, 10),
	}, "\n")
	return "rotation-" + common.GenerateHMACWithKey([]byte(common.CryptoSecret), payload)
}

func writeAetherCredentialRotationError(c *gin.Context, err error) {
	var remoteConflict *service.AetherControlConflictError
	if errors.As(err, &remoteConflict) {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "aether remote configuration conflict", "data": remoteConflict})
		return
	}
	var controlErr *service.AetherControlError
	if errors.As(err, &controlErr) {
		if controlErr.StatusCode == http.StatusConflict || controlErr.Code == "local_conflict" {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "aether remote configuration conflict"})
			return
		}
		if controlErr.Stage == "capabilities" &&
			(controlErr.Code == "unsupported_feature" || controlErr.Code == "unsupported_mode" || controlErr.Code == "unsupported_format" || controlErr.Code == "incompatible_version") {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "aether remote credential rotation is not supported"})
			return
		}
		if controlErr.Code == "prepare_failed" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid aether credential rotation"})
			return
		}
	}
	c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "failed to rotate aether integration credentials"})
}

func aetherIntegrationSharedConfig(integration *model.AetherIntegration) model.AetherIntegrationSharedConfig {
	return model.AetherIntegrationSharedConfig{
		RouteProfile:      integration.RouteProfile,
		ExecutionMode:     integration.ExecutionMode,
		Enabled:           integration.Enabled,
		CapabilityVersion: integration.CapabilityVersion,
	}
}
