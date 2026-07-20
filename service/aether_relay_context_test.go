package service

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAetherRelayContextIsSignedAndDoesNotExposeCredentials(t *testing.T) {
	expiresAt := time.Date(2026, 7, 15, 12, 0, 30, 0, time.UTC)
	headers, err := BuildAetherRelayContextHeaders(AetherRelayContextInput{
		SigningSecret:  "integration-signing-secret",
		InstanceID:     "aether-primary",
		RequestID:      "req_123",
		UserID:         17,
		TokenID:        29,
		ChannelID:      41,
		Group:          "pro",
		Model:          "gpt-5",
		RelayFormat:    types.RelayFormatOpenAI,
		ConfigRevision: 7,
		ExpiresAt:      expiresAt,
	})

	require.NoError(t, err)
	assert.Equal(t, "aether-primary", headers.Get(AetherInstanceIDHeader))
	assert.NotEmpty(t, headers.Get(AetherRelayContextHeader))
	assert.NotEmpty(t, headers.Get(AetherRelaySignatureHeader))
	assert.NotContains(t, headers.Get(AetherRelayContextHeader), "req_123")

	decoded, err := base64.RawURLEncoding.DecodeString(headers.Get(AetherRelayContextHeader))
	require.NoError(t, err)
	assert.NotContains(t, string(decoded), `"user_id"`)
	assert.NotContains(t, string(decoded), `"token_id"`)
	assert.NotContains(t, string(decoded), "token_key")
	assert.NotContains(t, string(decoded), "integration-signing-secret")

	context, err := VerifyAetherRelayContextHeaders(headers, "integration-signing-secret", expiresAt.Add(-time.Second))
	require.NoError(t, err)
	assert.Equal(t, "req_123", context.RequestID)
	assert.Equal(t, "pro", context.Group)
	assert.Equal(t, "gpt-5", context.Model)
	assert.Equal(t, "aether-primary", context.InstanceID)
	assert.Equal(t, "41", context.ChannelID)
	assert.Equal(t, int64(7), context.ConfigRevision)
	assert.True(t, strings.HasPrefix(context.SubjectID, "u_"))
	assert.True(t, strings.HasPrefix(context.TokenSubjectID, "t_"))
}

func TestBuildAetherRelayContextHeadersMatchesKnownAnswer(t *testing.T) {
	expiresAt := time.Unix(1784073630, 0).UTC()
	headers, err := BuildAetherRelayContextHeaders(AetherRelayContextInput{
		SigningSecret:  "test-only-relay-secret",
		InstanceID:     "aether-primary",
		RequestID:      "req_01JZ8K2A9Y",
		UserID:         17,
		TokenID:        29,
		ChannelID:      41,
		Group:          "default",
		Model:          "gpt-5",
		RelayFormat:    types.RelayFormatOpenAI,
		ConfigRevision: 7,
		ExpiresAt:      expiresAt,
	})

	require.NoError(t, err)
	require.Len(t, headers, 3)
	assert.Equal(t, []string{"aether-primary"}, headers.Values(AetherInstanceIDHeader))
	assert.Equal(t, []string{
		"eyJpbnN0YW5jZV9pZCI6ImFldGhlci1wcmltYXJ5IiwicmVxdWVzdF9pZCI6InJlcV8wMUpaOEsyQTlZIiwic3ViamVjdF9pZCI6InVfMGNkNzY1OTEyOTFmMzMwNWY3YmY2ZWVlYzUwZmRlOTUiLCJ0b2tlbl9zdWJqZWN0X2lkIjoidF83YTU5NzE0OGFkOTZjYTAzOGQ5YmVjNWMzMTQ5OTliOCIsImNoYW5uZWxfaWQiOiI0MSIsImdyb3VwIjoiZGVmYXVsdCIsIm1vZGVsIjoiZ3B0LTUiLCJyZWxheV9mb3JtYXQiOiJvcGVuYWkiLCJjb25maWdfcmV2aXNpb24iOjcsImV4cGlyZXNfYXQiOjE3ODQwNzM2MzB9",
	}, headers.Values(AetherRelayContextHeader))
	assert.Equal(t, []string{
		"6baaec93e4ef332a234ae3610f57cc6078c1f668413a7e252d203fff8bc09842",
	}, headers.Values(AetherRelaySignatureHeader))
}

func TestAetherRelayContextRejectsTamperingAndExpiry(t *testing.T) {
	expiresAt := time.Date(2026, 7, 15, 12, 0, 30, 0, time.UTC)
	headers, err := BuildAetherRelayContextHeaders(AetherRelayContextInput{
		SigningSecret: "integration-signing-secret",
		InstanceID:    "aether-primary",
		RequestID:     "req_123",
		UserID:        17,
		TokenID:       29,
		ChannelID:     41,
		Group:         "pro",
		Model:         "gpt-5",
		RelayFormat:   types.RelayFormatOpenAI,
		ExpiresAt:     expiresAt,
	})
	require.NoError(t, err)

	tampered := headers.Clone()
	tampered.Set(AetherRelayContextHeader, tampered.Get(AetherRelayContextHeader)+"x")
	_, err = VerifyAetherRelayContextHeaders(tampered, "integration-signing-secret", expiresAt.Add(-time.Second))
	require.Error(t, err)

	_, err = VerifyAetherRelayContextHeaders(headers, "integration-signing-secret", expiresAt)
	require.Error(t, err)
}

func TestVerifyAetherRelayContextHeadersRejectsValidDecodableTamperedPayload(t *testing.T) {
	expiresAt := time.Date(2026, 7, 15, 12, 0, 30, 0, time.UTC)
	headers, err := BuildAetherRelayContextHeaders(AetherRelayContextInput{
		SigningSecret: "integration-signing-secret",
		InstanceID:    "aether-primary",
		RequestID:     "req_123",
		UserID:        17,
		TokenID:       29,
		ChannelID:     41,
		Group:         "pro",
		Model:         "gpt-5",
		RelayFormat:   types.RelayFormatOpenAI,
		ExpiresAt:     expiresAt,
	})
	require.NoError(t, err)

	payload, err := base64.RawURLEncoding.DecodeString(headers.Get(AetherRelayContextHeader))
	require.NoError(t, err)
	var context AetherRelayContext
	require.NoError(t, common.Unmarshal(payload, &context))
	context.Model = "gpt-5-tampered"
	tamperedPayload, err := common.Marshal(context)
	require.NoError(t, err)
	tamperedContext := base64.RawURLEncoding.EncodeToString(tamperedPayload)
	decodedTamperedPayload, err := base64.RawURLEncoding.DecodeString(tamperedContext)
	require.NoError(t, err)
	var decodedTamperedContext AetherRelayContext
	require.NoError(t, common.Unmarshal(decodedTamperedPayload, &decodedTamperedContext))
	assert.Equal(t, "gpt-5-tampered", decodedTamperedContext.Model)

	tamperedHeaders := headers.Clone()
	tamperedHeaders.Set(AetherRelayContextHeader, tamperedContext)
	_, err = VerifyAetherRelayContextHeaders(tamperedHeaders, "integration-signing-secret", expiresAt.Add(-time.Second))
	assert.EqualError(t, err, "invalid aether relay signature")
}

func TestBuildAetherRelayContextHeadersRejectsMissingRoutingFields(t *testing.T) {
	baseInput := AetherRelayContextInput{
		SigningSecret:  "integration-signing-secret",
		InstanceID:     "aether-primary",
		RequestID:      "req_123",
		UserID:         17,
		TokenID:        29,
		ChannelID:      41,
		Group:          "pro",
		Model:          "gpt-5",
		RelayFormat:    types.RelayFormatOpenAI,
		ConfigRevision: 7,
		ExpiresAt:      time.Now().UTC().Add(30 * time.Second),
	}
	tests := []struct {
		name   string
		mutate func(*AetherRelayContextInput)
	}{
		{
			name: "group",
			mutate: func(input *AetherRelayContextInput) {
				input.Group = ""
			},
		},
		{
			name: "model",
			mutate: func(input *AetherRelayContextInput) {
				input.Model = ""
			},
		},
		{
			name: "relay format",
			mutate: func(input *AetherRelayContextInput) {
				input.RelayFormat = ""
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := baseInput
			test.mutate(&input)

			_, err := BuildAetherRelayContextHeaders(input)

			require.Error(t, err)
			assert.Contains(t, err.Error(), test.name)
		})
	}
}

func TestBuildAetherRelayContextHeadersValidatesRequestID(t *testing.T) {
	expiresAt := time.Now().UTC().Add(30 * time.Second)
	baseInput := AetherRelayContextInput{
		SigningSecret: "integration-signing-secret",
		InstanceID:    "aether-primary",
		RequestID:     "req_123",
		UserID:        17,
		TokenID:       29,
		ChannelID:     41,
		Group:         "pro",
		Model:         "gpt-5",
		RelayFormat:   types.RelayFormatOpenAI,
		ExpiresAt:     expiresAt,
	}
	tests := []struct {
		name      string
		requestID string
		wantError bool
	}{
		{name: "empty", requestID: "", wantError: true},
		{name: "whitespace", requestID: " \t", wantError: true},
		{name: "leading space", requestID: " req_123", wantError: true},
		{name: "trailing space", requestID: "req_123 ", wantError: true},
		{name: "LF", requestID: "req\n123", wantError: true},
		{name: "CR", requestID: "req\r123", wantError: true},
		{name: "CRLF", requestID: "req\r\n123", wantError: true},
		{name: "U+2028", requestID: "req\u2028123", wantError: true},
		{name: "U+2029", requestID: "req\u2029123", wantError: true},
		{name: "non-ASCII", requestID: "req_请求", wantError: true},
		{name: "101 bytes", requestID: strings.Repeat("r", 101), wantError: true},
		{name: "100 ASCII graphic bytes", requestID: strings.Repeat("r", 100)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := baseInput
			input.RequestID = test.requestID

			headers, err := BuildAetherRelayContextHeaders(input)

			if test.wantError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "request ID")
				return
			}
			require.NoError(t, err)
			context, err := VerifyAetherRelayContextHeaders(headers, input.SigningSecret, expiresAt.Add(-time.Second))
			require.NoError(t, err)
			assert.Equal(t, test.requestID, context.RequestID)
		})
	}
}

func TestVerifyAetherRelayContextHeadersRejectsEmptyRoutingFields(t *testing.T) {
	expiresAt := time.Now().UTC().Add(30 * time.Second)
	baseContext := AetherRelayContext{
		InstanceID:     "aether-primary",
		RequestID:      "req_123",
		SubjectID:      "u_anonymous",
		TokenSubjectID: "t_anonymous",
		ChannelID:      "41",
		Group:          "pro",
		Model:          "gpt-5",
		RelayFormat:    "openai",
		ConfigRevision: 7,
		ExpiresAt:      expiresAt.Unix(),
	}
	tests := []struct {
		name   string
		mutate func(*AetherRelayContext)
	}{
		{
			name: "group",
			mutate: func(context *AetherRelayContext) {
				context.Group = ""
			},
		},
		{
			name: "model",
			mutate: func(context *AetherRelayContext) {
				context.Model = ""
			},
		},
		{
			name: "relay format",
			mutate: func(context *AetherRelayContext) {
				context.RelayFormat = ""
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			context := baseContext
			test.mutate(&context)
			payload, err := common.Marshal(context)
			require.NoError(t, err)
			encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
			headers := make(http.Header)
			headers.Set(AetherInstanceIDHeader, context.InstanceID)
			headers.Set(AetherRelayContextHeader, encodedPayload)
			headers.Set(AetherRelaySignatureHeader, common.GenerateHMACWithKey([]byte("integration-signing-secret"), encodedPayload))

			_, err = VerifyAetherRelayContextHeaders(headers, "integration-signing-secret", expiresAt.Add(-time.Second))

			require.Error(t, err)
			assert.Contains(t, err.Error(), test.name)
		})
	}
}

func TestVerifyAetherRelayContextHeadersValidatesRequestID(t *testing.T) {
	expiresAt := time.Now().UTC().Add(30 * time.Second)
	baseContext := AetherRelayContext{
		InstanceID:     "aether-primary",
		RequestID:      "req_123",
		SubjectID:      "u_anonymous",
		TokenSubjectID: "t_anonymous",
		ChannelID:      "41",
		Group:          "pro",
		Model:          "gpt-5",
		RelayFormat:    "openai",
		ConfigRevision: 7,
		ExpiresAt:      expiresAt.Unix(),
	}
	tests := []struct {
		name      string
		requestID string
		wantError bool
	}{
		{name: "empty", requestID: "", wantError: true},
		{name: "whitespace", requestID: " \t", wantError: true},
		{name: "leading space", requestID: " req_123", wantError: true},
		{name: "trailing space", requestID: "req_123 ", wantError: true},
		{name: "LF", requestID: "req\n123", wantError: true},
		{name: "CR", requestID: "req\r123", wantError: true},
		{name: "CRLF", requestID: "req\r\n123", wantError: true},
		{name: "U+2028", requestID: "req\u2028123", wantError: true},
		{name: "U+2029", requestID: "req\u2029123", wantError: true},
		{name: "non-ASCII", requestID: "req_请求", wantError: true},
		{name: "101 bytes", requestID: strings.Repeat("r", 101), wantError: true},
		{name: "100 ASCII graphic bytes", requestID: strings.Repeat("r", 100)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			context := baseContext
			context.RequestID = test.requestID
			payload, err := common.Marshal(context)
			require.NoError(t, err)
			encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
			headers := make(http.Header)
			headers.Set(AetherInstanceIDHeader, context.InstanceID)
			headers.Set(AetherRelayContextHeader, encodedPayload)
			headers.Set(AetherRelaySignatureHeader, common.GenerateHMACWithKey([]byte("integration-signing-secret"), encodedPayload))

			verified, err := VerifyAetherRelayContextHeaders(headers, "integration-signing-secret", expiresAt.Add(-time.Second))

			if test.wantError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "request ID")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.requestID, verified.RequestID)
		})
	}
}

func TestApplyAetherRelayContextHeadersUsesIntegrationSecrets(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:aether_relay_context_service_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
	require.NoError(t, db.AutoMigrate(&model.AetherIntegration{}))

	integration := &model.AetherIntegration{
		ChannelID:      41,
		InstanceID:     "aether-primary",
		ExecutionMode:  model.AetherExecutionModeDirectChannel,
		Enabled:        true,
		ConfigRevision: 3,
	}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)

	headers := make(http.Header)
	info := &relaycommon.RelayInfo{
		RequestId:       "req_123",
		UserId:          17,
		TokenId:         29,
		UsingGroup:      "pro",
		OriginModelName: "gpt-5",
		RelayFormat:     types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeAether,
			ChannelId:   41,
		},
	}

	require.NoError(t, ApplyAetherRelayContextHeaders(info, &headers))
	context, err := VerifyAetherRelayContextHeaders(headers, "relay-signing-secret", time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, "req_123", context.RequestID)
	assert.Equal(t, int64(3), context.ConfigRevision)
	assert.True(t, strings.HasPrefix(context.SubjectID, "u_"))
}
