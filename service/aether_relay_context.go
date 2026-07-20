package service

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
)

const (
	AetherInstanceIDHeader      = "X-Aether-Instance-ID"
	AetherRelayContextHeader    = "X-Aether-Relay-Context"
	AetherRelaySignatureHeader  = "X-Aether-Relay-Signature"
	aetherRelaySubjectHashChars = 32
	aetherRequestIDMaxBytes     = 100
)

type AetherRelayContextInput struct {
	SigningSecret  string
	InstanceID     string
	RequestID      string
	UserID         int
	TokenID        int
	ChannelID      int
	Group          string
	Model          string
	RelayFormat    types.RelayFormat
	ConfigRevision int64
	ExpiresAt      time.Time
}

type AetherRelayContext struct {
	InstanceID     string `json:"instance_id"`
	RequestID      string `json:"request_id"`
	SubjectID      string `json:"subject_id"`
	TokenSubjectID string `json:"token_subject_id"`
	ChannelID      string `json:"channel_id"`
	Group          string `json:"group,omitempty"`
	Model          string `json:"model,omitempty"`
	RelayFormat    string `json:"relay_format,omitempty"`
	ConfigRevision int64  `json:"config_revision"`
	ExpiresAt      int64  `json:"expires_at"`
}

func BuildAetherRelayContextHeaders(input AetherRelayContextInput) (http.Header, error) {
	if strings.TrimSpace(input.SigningSecret) == "" {
		return nil, errors.New("aether signing secret is required")
	}
	if strings.TrimSpace(input.InstanceID) == "" {
		return nil, errors.New("aether instance ID is required")
	}
	if err := validateAetherRequestID(input.RequestID); err != nil {
		return nil, err
	}
	if input.ChannelID <= 0 {
		return nil, errors.New("aether channel ID is required")
	}
	if strings.TrimSpace(input.Group) == "" {
		return nil, errors.New("aether relay group is required")
	}
	if strings.TrimSpace(input.Model) == "" {
		return nil, errors.New("aether relay model is required")
	}
	if strings.TrimSpace(string(input.RelayFormat)) == "" {
		return nil, errors.New("aether relay format is required")
	}
	if input.ExpiresAt.IsZero() {
		return nil, errors.New("aether relay context expiry is required")
	}

	context := AetherRelayContext{
		InstanceID:     input.InstanceID,
		RequestID:      input.RequestID,
		SubjectID:      aetherSubjectID(input.SigningSecret, "user", input.UserID, "u_"),
		TokenSubjectID: aetherSubjectID(input.SigningSecret, "token", input.TokenID, "t_"),
		ChannelID:      strconv.Itoa(input.ChannelID),
		Group:          input.Group,
		Model:          input.Model,
		RelayFormat:    string(input.RelayFormat),
		ConfigRevision: input.ConfigRevision,
		ExpiresAt:      input.ExpiresAt.Unix(),
	}
	payload, err := common.Marshal(context)
	if err != nil {
		return nil, fmt.Errorf("marshal aether relay context: %w", err)
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)

	headers := make(http.Header, 3)
	headers.Set(AetherInstanceIDHeader, input.InstanceID)
	headers.Set(AetherRelayContextHeader, encodedPayload)
	headers.Set(AetherRelaySignatureHeader, common.GenerateHMACWithKey([]byte(input.SigningSecret), encodedPayload))
	return headers, nil
}

func VerifyAetherRelayContextHeaders(headers http.Header, signingSecret string, now time.Time) (AetherRelayContext, error) {
	if strings.TrimSpace(signingSecret) == "" {
		return AetherRelayContext{}, errors.New("aether signing secret is required")
	}
	encodedPayload := strings.TrimSpace(headers.Get(AetherRelayContextHeader))
	signature := strings.TrimSpace(headers.Get(AetherRelaySignatureHeader))
	if encodedPayload == "" || signature == "" {
		return AetherRelayContext{}, errors.New("aether relay signature headers are required")
	}
	expectedSignature, err := hex.DecodeString(common.GenerateHMACWithKey([]byte(signingSecret), encodedPayload))
	if err != nil {
		return AetherRelayContext{}, fmt.Errorf("build aether relay signature: %w", err)
	}
	providedSignature, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(expectedSignature, providedSignature) {
		return AetherRelayContext{}, errors.New("invalid aether relay signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return AetherRelayContext{}, errors.New("invalid aether relay context encoding")
	}
	var context AetherRelayContext
	if err := common.Unmarshal(payload, &context); err != nil {
		return AetherRelayContext{}, errors.New("invalid aether relay context")
	}
	if err := validateAetherRequestID(context.RequestID); err != nil {
		return AetherRelayContext{}, err
	}
	if strings.TrimSpace(context.InstanceID) == "" || strings.TrimSpace(context.ChannelID) == "" {
		return AetherRelayContext{}, errors.New("invalid aether relay context fields")
	}
	if strings.TrimSpace(context.Group) == "" {
		return AetherRelayContext{}, errors.New("invalid aether relay group")
	}
	if strings.TrimSpace(context.Model) == "" {
		return AetherRelayContext{}, errors.New("invalid aether relay model")
	}
	if strings.TrimSpace(context.RelayFormat) == "" {
		return AetherRelayContext{}, errors.New("invalid aether relay format")
	}
	if headers.Get(AetherInstanceIDHeader) != context.InstanceID {
		return AetherRelayContext{}, errors.New("aether relay instance mismatch")
	}
	if context.ExpiresAt <= now.Unix() {
		return AetherRelayContext{}, errors.New("aether relay context expired")
	}
	return context, nil
}

func validateAetherRequestID(requestID string) error {
	if len(requestID) == 0 || len(requestID) > aetherRequestIDMaxBytes {
		return errors.New("invalid aether request ID")
	}
	for index := 0; index < len(requestID); index++ {
		if requestID[index] < '!' || requestID[index] > '~' {
			return errors.New("invalid aether request ID")
		}
	}
	return nil
}

func aetherSubjectID(secret string, kind string, id int, prefix string) string {
	value := common.GenerateHMACWithKey([]byte(secret), kind+":"+strconv.Itoa(id))
	if len(value) > aetherRelaySubjectHashChars {
		value = value[:aetherRelaySubjectHashChars]
	}
	return prefix + value
}

func ApplyAetherRelayContextHeaders(info *relaycommon.RelayInfo, headers *http.Header) error {
	if info == nil || info.ChannelMeta == nil || info.ChannelType != constant.ChannelTypeAether {
		return nil
	}
	if headers == nil {
		return errors.New("aether relay headers are required")
	}
	integration, err := model.GetAetherIntegrationByChannelID(info.ChannelId)
	if err != nil {
		return fmt.Errorf("load aether integration: %w", err)
	}
	if !integration.Enabled || integration.ExecutionMode != model.AetherExecutionModeDirectChannel {
		return errors.New("aether integration is not available for direct forwarding")
	}
	_, signingSecret, err := integration.Secrets()
	if err != nil {
		return fmt.Errorf("read aether integration secrets: %w", err)
	}
	aetherHeaders, err := BuildAetherRelayContextHeaders(AetherRelayContextInput{
		SigningSecret:  signingSecret,
		InstanceID:     integration.InstanceID,
		RequestID:      info.RequestId,
		UserID:         info.UserId,
		TokenID:        info.TokenId,
		ChannelID:      info.ChannelId,
		Group:          info.UsingGroup,
		Model:          info.OriginModelName,
		RelayFormat:    info.RelayFormat,
		ConfigRevision: integration.ConfigRevision,
		ExpiresAt:      time.Now().UTC().Add(30 * time.Second),
	})
	if err != nil {
		return err
	}
	for key, values := range aetherHeaders {
		headers.Del(key)
		for _, value := range values {
			headers.Add(key, value)
		}
	}
	return nil
}
