package service

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

const aetherControlResponseLimit = 1 << 20

const (
	aetherControlCapabilityMajor = 0
	aetherControlCapabilityMinor = 1
)

var aetherControlHTTPClient = &http.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: aetherControlRejectRedirect,
}

func aetherControlRejectRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

type AetherControlConflictError struct {
	CurrentRevision int64 `json:"current_revision"`
	CurrentConfig   any   `json:"current_config,omitempty"`
}

type AetherControlError struct {
	Stage      string
	Code       string
	StatusCode int
	Detail     string
	Err        error
}

func (err *AetherControlError) Error() string {
	message := "aether control " + err.Stage + " failed: " + err.Code
	if err.StatusCode != 0 {
		message += fmt.Sprintf(" (status %d)", err.StatusCode)
	}
	if err.Detail != "" {
		message += ": " + err.Detail
	}
	if err.Err != nil {
		message += ": " + err.Err.Error()
	}
	return message
}

func (err *AetherControlError) Unwrap() error {
	return err.Err
}

func (err *AetherControlError) ControlStage() string {
	return err.Stage
}

func (err *AetherControlError) ControlCode() string {
	return err.Code
}

func (err *AetherControlConflictError) Error() string {
	return "aether remote configuration conflict"
}

type aetherControlConfigPayload struct {
	RouteProfile      string `json:"route_profile"`
	ExecutionMode     string `json:"execution_mode"`
	CapabilityVersion string `json:"capability_version"`
	Enabled           bool   `json:"enabled"`
	BaseRevision      int64  `json:"base_revision"`
}

type aetherControlCredentialRotationPayload struct {
	ID                  string `json:"id"`
	ControlSecret       string `json:"control_secret"`
	RelaySigningSecret  string `json:"relay_signing_secret"`
	TransitionExpiresAt int64  `json:"transition_expires_at"`
	RevokePrevious      bool   `json:"revoke_previous"`
}

type aetherControlCredentialRotationConfigPayload struct {
	BaseRevision       int64                                  `json:"base_revision"`
	CredentialRotation aetherControlCredentialRotationPayload `json:"credential_rotation"`
	RouteProfile       string                                 `json:"route_profile,omitempty"`
	ExecutionMode      string                                 `json:"execution_mode,omitempty"`
	Enabled            bool                                   `json:"enabled"`
}

type aetherControlCapabilitiesResponse struct {
	SupportedModes    []string `json:"supported_modes"`
	SupportedFormats  []string `json:"supported_formats"`
	CapabilityVersion string   `json:"capability_version"`
	Features          []string `json:"features"`
}

type aetherControlConfigResponse struct {
	InstanceID            string                              `json:"instance_id"`
	CapabilityVersion     string                              `json:"capability_version"`
	BaseRevision          int64                               `json:"base_revision"`
	CredentialRotationAck *aetherControlCredentialRotationAck `json:"credential_rotation_ack"`
}

type aetherControlCredentialRotationAck struct {
	RotationID          string `json:"rotation_id"`
	CredentialRevision  int64  `json:"credential_revision"`
	TransitionExpiresAt int64  `json:"transition_expires_at"`
	State               string `json:"state"`
}

type aetherControlStatusResponse struct {
	InstanceID   string `json:"instance_id"`
	Healthy      bool   `json:"healthy"`
	BaseRevision int64  `json:"base_revision"`
}

func SyncAetherIntegration(channelID int) (*model.AetherIntegration, error) {
	channel, err := model.GetChannelById(channelID, true)
	if err != nil {
		return nil, err
	}
	if channel.Type != constant.ChannelTypeAether {
		return nil, errors.New("channel is not an aether channel")
	}
	integration, err := model.GetAetherIntegrationByChannelID(channelID)
	if err != nil {
		return nil, err
	}
	controlSecret, _, err := integration.Secrets()
	if err != nil {
		return nil, fmt.Errorf("read aether control secret: %w", err)
	}

	capabilitiesURL, err := aetherControlURL(channel, "/api/integrations/new-api/v1/capabilities")
	if err != nil {
		return nil, &AetherControlError{Stage: "capabilities", Code: "invalid_url", Err: err}
	}
	capabilitiesBody, statusCode, err := sendAetherControlRequest("capabilities", http.MethodGet, capabilitiesURL, controlSecret, integration.InstanceID, nil)
	if err != nil {
		return nil, err
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return nil, &AetherControlError{Stage: "capabilities", Code: "unexpected_status", StatusCode: statusCode}
	}
	var capabilities aetherControlCapabilitiesResponse
	if err := common.Unmarshal(capabilitiesBody, &capabilities); err != nil {
		return nil, &AetherControlError{Stage: "capabilities", Code: "invalid_response", Err: err}
	}
	if err := validateAetherControlCapabilities(capabilities, nil); err != nil {
		return nil, err
	}
	if err := model.ValidateAetherExecutionMode(integration.ExecutionMode); err != nil {
		return nil, &AetherControlError{Stage: "configuration", Code: "unsupported_mode", Err: err}
	}

	configURL, err := aetherControlURL(channel, "/api/integrations/new-api/v1/instances/"+url.PathEscape(integration.InstanceID))
	if err != nil {
		return nil, &AetherControlError{Stage: "configuration", Code: "invalid_url", Err: err}
	}
	payload, err := common.Marshal(aetherControlConfigPayload{
		RouteProfile:      integration.RouteProfile,
		ExecutionMode:     integration.ExecutionMode,
		CapabilityVersion: integration.CapabilityVersion,
		Enabled:           integration.Enabled,
		BaseRevision:      integration.RemoteConfigRevision,
	})
	if err != nil {
		return nil, &AetherControlError{Stage: "configuration", Code: "request_encoding_failed", Err: err}
	}
	body, statusCode, err := sendAetherControlRequest("configuration", http.MethodPut, configURL, controlSecret, integration.InstanceID, payload)
	if err != nil {
		return nil, err
	}
	if statusCode == http.StatusConflict {
		var conflict AetherControlConflictError
		if err := common.Unmarshal(body, &conflict); err != nil {
			return nil, &AetherControlError{Stage: "configuration", Code: "invalid_conflict_response", StatusCode: statusCode, Err: err}
		}
		return nil, &conflict
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return nil, &AetherControlError{Stage: "configuration", Code: "unexpected_status", StatusCode: statusCode}
	}
	var remoteConfig aetherControlConfigResponse
	if err := common.Unmarshal(body, &remoteConfig); err != nil {
		return nil, &AetherControlError{Stage: "configuration", Code: "invalid_response", Err: err}
	}
	if remoteConfig.InstanceID != integration.InstanceID || remoteConfig.BaseRevision < 0 {
		return nil, &AetherControlError{Stage: "configuration", Code: "invalid_response"}
	}

	now := common.GetTimestamp()
	if err := model.DB.Model(&model.AetherIntegration{}).Where("id = ?", integration.Id).Updates(map[string]interface{}{
		"remote_config_revision": remoteConfig.BaseRevision,
		"last_sync_time":         now,
	}).Error; err != nil {
		return nil, &AetherControlError{Stage: "configuration", Code: "persistence_failed", Err: err}
	}
	updated := &model.AetherIntegration{}
	if err := model.DB.Where("id = ?", integration.Id).First(updated).Error; err != nil {
		return nil, &AetherControlError{Stage: "configuration", Code: "persistence_failed", Err: err}
	}

	statusURL, err := aetherControlURL(channel, "/api/integrations/new-api/v1/instances/"+url.PathEscape(integration.InstanceID)+"/status")
	if err != nil {
		return updated, &AetherControlError{Stage: "status", Code: "invalid_url", Err: err}
	}
	statusBody, statusCode, err := sendAetherControlRequest("status", http.MethodGet, statusURL, controlSecret, integration.InstanceID, nil)
	if err != nil {
		return updated, err
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return updated, &AetherControlError{Stage: "status", Code: "unexpected_status", StatusCode: statusCode}
	}
	var remoteStatus aetherControlStatusResponse
	if err := common.Unmarshal(statusBody, &remoteStatus); err != nil {
		return updated, &AetherControlError{Stage: "status", Code: "invalid_response", Err: err}
	}
	if remoteStatus.InstanceID != integration.InstanceID || remoteStatus.BaseRevision < 0 || remoteStatus.BaseRevision != remoteConfig.BaseRevision {
		return updated, &AetherControlError{Stage: "status", Code: "invalid_response"}
	}
	healthStatus := "unhealthy"
	if remoteStatus.Healthy {
		healthStatus = "healthy"
	}
	if err := model.DB.Model(&model.AetherIntegration{}).Where("id = ?", integration.Id).Updates(map[string]interface{}{
		"last_health_time":   common.GetTimestamp(),
		"last_health_status": healthStatus,
	}).Error; err != nil {
		return nil, &AetherControlError{Stage: "status", Code: "persistence_failed", Err: err}
	}
	updated = &model.AetherIntegration{}
	if err := model.DB.Where("id = ?", integration.Id).First(updated).Error; err != nil {
		return nil, &AetherControlError{Stage: "status", Code: "persistence_failed", Err: err}
	}
	return updated, nil
}

// RotateAetherIntegrationCredentialsRemoteFirst preserves active credentials
// until the remote AETHER instance acknowledges the exact V2 rotation.
func RotateAetherIntegrationCredentialsRemoteFirst(
	channelID int,
	baseRevision int64,
	rotation model.AetherIntegrationPendingCredentialRotationRequest,
) (*model.AetherIntegration, error) {
	channel, err := model.GetChannelById(channelID, true)
	if err != nil {
		return nil, err
	}
	if channel.Type != constant.ChannelTypeAether {
		return nil, errors.New("channel is not an aether channel")
	}
	integration, err := model.GetAetherIntegrationByChannelID(channelID)
	if err != nil {
		return nil, err
	}
	controlSecret, _, err := integration.Secrets()
	if err != nil {
		return nil, fmt.Errorf("read aether control secret: %w", err)
	}
	remoteControlSecret := controlSecret

	capabilitiesURL, err := aetherControlURL(channel, "/api/integrations/new-api/v1/capabilities")
	if err != nil {
		return nil, &AetherControlError{Stage: "capabilities", Code: "invalid_url", Err: err}
	}
	capabilitiesBody, statusCode, err := sendAetherControlRequest("capabilities", http.MethodGet, capabilitiesURL, controlSecret, integration.InstanceID, nil)
	if err != nil {
		return nil, err
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		pendingControlSecret, matches, pendingErr := aetherPendingRotationRecoveryControlSecret(integration, baseRevision, rotation)
		if pendingErr != nil {
			return nil, &AetherControlError{Stage: "rotation", Code: "pending_secret_unavailable", Err: pendingErr}
		}
		if matches {
			capabilitiesBody, statusCode, err = sendAetherControlRequest("capabilities", http.MethodGet, capabilitiesURL, pendingControlSecret, integration.InstanceID, nil)
			if err != nil {
				return nil, err
			}
			remoteControlSecret = pendingControlSecret
		}
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return nil, &AetherControlError{Stage: "capabilities", Code: "unexpected_status", StatusCode: statusCode}
	}
	var capabilities aetherControlCapabilitiesResponse
	if err := common.Unmarshal(capabilitiesBody, &capabilities); err != nil {
		return nil, &AetherControlError{Stage: "capabilities", Code: "invalid_response", Err: err}
	}
	if err := validateAetherControlCapabilities(capabilities, []string{"credential_rotation_v1", "control_hmac_v2"}); err != nil {
		return nil, err
	}

	pending, conflict, err := model.PrepareAetherIntegrationPendingCredentialRotation(integration, baseRevision, rotation)
	if err != nil {
		return nil, &AetherControlError{Stage: "rotation", Code: "prepare_failed", Err: err}
	}
	if conflict != nil {
		return nil, &AetherControlError{
			Stage:  "rotation",
			Code:   "local_conflict",
			Detail: fmt.Sprintf("current revision %d", conflict.CurrentRevision),
		}
	}
	controlSecretNext, relaySigningSecretNext, err := pending.Secrets()
	if err != nil {
		return nil, &AetherControlError{Stage: "rotation", Code: "pending_secret_unavailable", Err: err}
	}
	// Reload after the reservation so the config payload cannot use a stale
	// remote revision from a concurrent successful synchronization.
	integration, err = model.GetAetherIntegrationByChannelID(channelID)
	if err != nil {
		return nil, &AetherControlError{Stage: "rotation", Code: "persistence_failed", Err: err}
	}
	rotationURL, err := aetherControlURL(channel, "/api/integrations/new-api/v1/instances/"+url.PathEscape(integration.InstanceID))
	if err != nil {
		return nil, &AetherControlError{Stage: "rotation", Code: "invalid_url", Err: err}
	}
	payload, err := common.Marshal(aetherControlCredentialRotationConfigPayload{
		BaseRevision: integration.RemoteConfigRevision,
		CredentialRotation: aetherControlCredentialRotationPayload{
			ID:                  pending.RotationID,
			ControlSecret:       controlSecretNext,
			RelaySigningSecret:  relaySigningSecretNext,
			TransitionExpiresAt: pending.TransitionSecretsExpireAt,
			RevokePrevious:      pending.RevokePrevious,
		},
		RouteProfile:  integration.RouteProfile,
		ExecutionMode: integration.ExecutionMode,
		Enabled:       integration.Enabled,
	})
	if err != nil {
		return nil, &AetherControlError{Stage: "rotation", Code: "request_encoding_failed", Err: err}
	}
	body, statusCode, err := sendAetherControlV2Request(
		"rotation",
		http.MethodPut,
		rotationURL,
		remoteControlSecret,
		integration.InstanceID,
		payload,
	)
	if err != nil {
		return nil, err
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return nil, &AetherControlError{Stage: "rotation", Code: "unexpected_status", StatusCode: statusCode}
	}
	var remoteConfig aetherControlConfigResponse
	if err := common.Unmarshal(body, &remoteConfig); err != nil {
		return nil, &AetherControlError{Stage: "rotation", Code: "invalid_response", Err: err}
	}
	ack := remoteConfig.CredentialRotationAck
	if remoteConfig.InstanceID != integration.InstanceID || remoteConfig.BaseRevision <= 0 || ack == nil ||
		ack.RotationID != pending.RotationID || ack.CredentialRevision <= 0 ||
		ack.TransitionExpiresAt != pending.TransitionSecretsExpireAt || ack.State != "applied" {
		return nil, &AetherControlError{Stage: "rotation", Code: "invalid_response"}
	}
	updated, err := model.PromoteAetherIntegrationPendingCredentialRotation(
		integration.Id,
		pending.RotationID,
		remoteConfig.BaseRevision,
	)
	if err != nil {
		return nil, &AetherControlError{Stage: "rotation", Code: "persistence_failed", Err: err}
	}
	return updated, nil
}

// aetherPendingRotationRecoveryControlSecret permits a retry to authenticate
// with the pending credential only after an old credential has been rejected.
// It prevents an unrelated pending request from becoming an authentication
// fallback for a new rotation attempt.
func aetherPendingRotationRecoveryControlSecret(
	integration *model.AetherIntegration,
	baseRevision int64,
	rotation model.AetherIntegrationPendingCredentialRotationRequest,
) (string, bool, error) {
	if integration == nil || integration.Id <= 0 || baseRevision <= 0 {
		return "", false, nil
	}
	nextRevision := baseRevision + 1
	if nextRevision <= baseRevision {
		return "", false, nil
	}
	pending, err := model.GetAetherIntegrationPendingCredentialRotation(integration.Id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if pending.InstanceID != integration.InstanceID ||
		pending.RotationID != strings.TrimSpace(rotation.RotationID) ||
		pending.BaseConfigRevision != baseRevision ||
		pending.ReservedConfigRevision != nextRevision ||
		integration.ConfigRevision != nextRevision ||
		pending.TransitionSecretsExpireAt != rotation.TransitionExpiresAt.UTC().Unix() ||
		pending.RevokePrevious != rotation.RevokePrevious {
		return "", false, nil
	}
	pendingControlSecret, pendingRelaySigningSecret, err := pending.Secrets()
	if err != nil {
		return "", false, err
	}
	if subtle.ConstantTimeCompare([]byte(pendingControlSecret), []byte(rotation.ControlSecret)) != 1 ||
		subtle.ConstantTimeCompare([]byte(pendingRelaySigningSecret), []byte(rotation.RelaySigningSecret)) != 1 {
		return "", false, nil
	}
	return pendingControlSecret, true, nil
}

func sendAetherControlRequest(stage string, method string, requestURL string, controlSecret string, instanceID string, payload []byte) ([]byte, int, error) {
	var requestBody io.Reader
	if payload != nil {
		requestBody = bytes.NewReader(payload)
	}
	request, err := http.NewRequest(method, requestURL, requestBody)
	if err != nil {
		return nil, 0, &AetherControlError{Stage: stage, Code: "build_request_failed", Err: err}
	}
	request.Header.Set("Authorization", "Bearer "+controlSecret)
	request.Header.Set(AetherInstanceIDHeader, instanceID)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return sendAetherControlHTTP(request, stage)
}

func sendAetherControlV2Request(stage string, method string, requestURL string, controlSecret string, instanceID string, payload []byte) ([]byte, int, error) {
	request, err := http.NewRequest(method, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, &AetherControlError{Stage: stage, Code: "build_request_failed", Err: err}
	}
	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	nonce := common.GetUUID()
	bodySHA256 := aetherControlV2BodySHA256(payload)
	canonicalPayload := aetherControlV2CanonicalPayload(method, request.URL, instanceID, timestamp, nonce, bodySHA256)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Aether-Instance-ID", instanceID)
	request.Header.Set("X-Aether-Signature-Version", "v2")
	request.Header.Set("X-Aether-Timestamp", timestamp)
	request.Header.Set("X-Aether-Nonce", nonce)
	request.Header.Set("X-Aether-Body-SHA256", bodySHA256)
	request.Header.Set("X-Aether-Signature", common.GenerateHMACWithKey([]byte(controlSecret), canonicalPayload))
	return sendAetherControlHTTP(request, stage)
}

func sendAetherControlHTTP(request *http.Request, stage string) ([]byte, int, error) {

	client := *aetherControlHTTPClient
	client.CheckRedirect = aetherControlRejectRedirect
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, &AetherControlError{Stage: stage, Code: "transport_failed", Err: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, aetherControlResponseLimit+1))
	if err != nil {
		return nil, response.StatusCode, &AetherControlError{Stage: stage, Code: "read_response_failed", StatusCode: response.StatusCode, Err: err}
	}
	if len(body) > aetherControlResponseLimit {
		return nil, response.StatusCode, &AetherControlError{Stage: stage, Code: "response_too_large", StatusCode: response.StatusCode}
	}
	return body, response.StatusCode, nil
}

func aetherControlV2BodySHA256(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func aetherControlV2CanonicalPayload(method string, requestURL *url.URL, instanceID string, timestamp string, nonce string, bodySHA256 string) string {
	escapedPath := requestURL.EscapedPath()
	if escapedPath == "" {
		escapedPath = "/"
	}
	return strings.Join([]string{
		"AETHER-CONTROL-V2",
		strings.ToUpper(method),
		escapedPath,
		requestURL.RawQuery,
		instanceID,
		timestamp,
		nonce,
		bodySHA256,
	}, "\n")
}

func validateAetherControlCapabilities(capabilities aetherControlCapabilitiesResponse, requiredFeatures []string) *AetherControlError {
	supportsDirectChannel := false
	for _, mode := range capabilities.SupportedModes {
		if mode == model.AetherExecutionModeDirectChannel {
			supportsDirectChannel = true
			break
		}
	}
	if !supportsDirectChannel {
		return &AetherControlError{
			Stage:  "capabilities",
			Code:   "unsupported_mode",
			Detail: "direct_channel is required",
		}
	}
	supportsOpenAI := false
	for _, format := range capabilities.SupportedFormats {
		if format == "openai" {
			supportsOpenAI = true
			break
		}
	}
	if !supportsOpenAI {
		return &AetherControlError{
			Stage:  "capabilities",
			Code:   "unsupported_format",
			Detail: "openai is required",
		}
	}
	if !isAetherCapabilityVersionCompatible(strings.TrimSpace(capabilities.CapabilityVersion)) {
		return &AetherControlError{
			Stage:  "capabilities",
			Code:   "incompatible_version",
			Detail: "capability_version must be compatible with 0.1.x",
		}
	}
	for _, requiredFeature := range requiredFeatures {
		found := false
		for _, feature := range capabilities.Features {
			if strings.TrimSpace(feature) == requiredFeature {
				found = true
				break
			}
		}
		if !found {
			return &AetherControlError{
				Stage:  "capabilities",
				Code:   "unsupported_feature",
				Detail: requiredFeature + " is required",
			}
		}
	}
	return nil
}

func isAetherCapabilityVersionCompatible(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return major == aetherControlCapabilityMajor && minor == aetherControlCapabilityMinor
}

func aetherControlURL(channel *model.Channel, path string) (string, error) {
	if channel == nil {
		return "", errors.New("aether channel is required")
	}
	baseURL, err := url.Parse(channel.GetBaseURL())
	if err != nil || baseURL.Scheme != "https" || baseURL.Host == "" || baseURL.User != nil {
		return "", errors.New("aether control URL must be an HTTPS channel base URL")
	}
	baseURL.Path = path
	baseURL.RawPath = ""
	baseURL.RawQuery = ""
	baseURL.Fragment = ""
	return baseURL.String(), nil
}
