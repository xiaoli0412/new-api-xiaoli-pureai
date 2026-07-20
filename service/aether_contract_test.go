package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAetherContractBundleHasAuditedBilateralBaseline(t *testing.T) {
	const baselineName = "aether-newapi-v1.baseline.json"
	localContractDir := filepath.Join("..", "docs", "contracts")
	peerRoot := os.Getenv("AETHER_NEWAPI_CONTRACT_PEER_ROOT")
	if peerRoot == "" {
		peerRoot = filepath.Join("..", "..", "Aether-pureai")
	}
	peerContractDir := filepath.Join(peerRoot, "docs", "contracts")
	trackedFiles := []string{
		"aether-newapi-v1.json",
		"aether-newapi-v1.schema.json",
		"aether-newapi-v1.examples.json",
	}

	for _, name := range trackedFiles {
		local, err := os.ReadFile(filepath.Join(localContractDir, name))
		require.NoError(t, err)
		peer, err := os.ReadFile(filepath.Join(peerContractDir, name))
		require.NoError(t, err)
		require.Equal(t, local, peer, "%s must be byte-identical in both repositories", name)
	}

	localBaseline, err := os.ReadFile(filepath.Join(localContractDir, baselineName))
	require.NoError(t, err)
	peerBaseline, err := os.ReadFile(filepath.Join(peerContractDir, baselineName))
	require.NoError(t, err)
	require.Equal(t, localBaseline, peerBaseline, "the audited baseline must be byte-identical in both repositories")

	var baseline struct {
		BaselineFormat   string            `json:"baseline_format"`
		BaselineRevision int               `json:"baseline_revision"`
		ContractVersion  string            `json:"contract_version"`
		SchemaRevision   int               `json:"schema_revision"`
		BundleSHA256     string            `json:"bundle_sha256"`
		FileSHA256       map[string]string `json:"file_sha256"`
		TrackedFiles     []string          `json:"tracked_files"`
		ChangeControl    struct {
			ChangeID     string `json:"change_id"`
			RecordedAt   string `json:"recorded_at"`
			ReviewPolicy string `json:"review_policy"`
			Rationale    string `json:"rationale"`
		} `json:"change_control"`
	}
	require.NoError(t, common.Unmarshal(localBaseline, &baseline))
	assert.Equal(t, "aether-newapi-contract-baseline/v1", baseline.BaselineFormat)
	assert.Positive(t, baseline.BaselineRevision)
	assert.Equal(t, trackedFiles, baseline.TrackedFiles)
	require.Len(t, baseline.FileSHA256, len(trackedFiles))
	assert.NotEmpty(t, baseline.ChangeControl.ChangeID)
	assert.NotEmpty(t, baseline.ChangeControl.RecordedAt)
	assert.Equal(t, "cross-repository-review-required", baseline.ChangeControl.ReviewPolicy)
	assert.NotEmpty(t, baseline.ChangeControl.Rationale)

	manifestBytes, err := os.ReadFile(filepath.Join(localContractDir, trackedFiles[0]))
	require.NoError(t, err)
	var manifest struct {
		ContractVersion string `json:"contract_version"`
		SchemaRevision  int    `json:"schema_revision"`
		BundleSHA256    string `json:"bundle_sha256"`
	}
	require.NoError(t, common.Unmarshal(manifestBytes, &manifest))
	assert.Equal(t, manifest.ContractVersion, baseline.ContractVersion)
	assert.Equal(t, manifest.SchemaRevision, baseline.SchemaRevision)

	schema, err := os.ReadFile(filepath.Join(localContractDir, trackedFiles[1]))
	require.NoError(t, err)
	examples, err := os.ReadFile(filepath.Join(localContractDir, trackedFiles[2]))
	require.NoError(t, err)
	for _, name := range trackedFiles {
		contents, err := os.ReadFile(filepath.Join(localContractDir, name))
		require.NoError(t, err)
		digest := sha256.Sum256(contents)
		assert.Equal(t, hex.EncodeToString(digest[:]), baseline.FileSHA256[name])
	}
	bundle := make([]byte, 0, len(schema)+len(examples)+1)
	bundle = append(bundle, schema...)
	bundle = append(bundle, 0)
	bundle = append(bundle, examples...)
	digest := sha256.Sum256(bundle)
	actualBundleSHA256 := hex.EncodeToString(digest[:])
	assert.Equal(t, actualBundleSHA256, manifest.BundleSHA256)
	assert.Equal(t, actualBundleSHA256, baseline.BundleSHA256)
}

func TestAetherContractManifestDefinesCanonicalV1Surface(t *testing.T) {
	data, err := os.ReadFile("../docs/contracts/aether-newapi-v1.json")
	require.NoError(t, err)

	var manifest struct {
		ContractVersion string   `json:"contract_version"`
		RelayHeaders    []string `json:"relay_headers"`
		ServiceHeaders  []string `json:"service_headers"`
		ControlHMACV2   struct {
			SignatureVersion string `json:"signature_version"`
			CanonicalPayload string `json:"canonical_payload"`
		} `json:"control_hmac_v2"`
		RevisionConflict struct {
			HTTPStatus         int      `json:"http_status"`
			ResponseSchema     string   `json:"response_schema"`
			CurrentConfigField string   `json:"current_config_field"`
			DiffEntryFields    []string `json:"diff_entry_fields"`
		} `json:"revision_conflict"`
		InstanceStatus struct {
			ResponseSchema                   string   `json:"response_schema"`
			HealthySemantics                 string   `json:"healthy_semantics"`
			Fields                           []string `json:"fields"`
			LastSyncAtSemantics              string   `json:"last_sync_at_semantics"`
			UptimeSecsSemantics              string   `json:"uptime_secs_semantics"`
			ConfigStoreUnavailableHTTPStatus int      `json:"config_store_unavailable_http_status"`
			RoutingModeValues                []string `json:"routing_mode_values"`
		} `json:"instance_status"`
		RelayContext struct {
			RequiredFields []string `json:"required_fields"`
		} `json:"relay_context"`
		NewAPIEndpoints []string `json:"new_api_endpoints"`
		AetherEndpoints []string `json:"aether_endpoints"`
		ActiveModes     []string `json:"active_modes"`
		JSONSchema      string   `json:"json_schema"`
		Examples        string   `json:"examples"`
		BundleSHA256    string   `json:"bundle_sha256"`
	}
	require.NoError(t, common.Unmarshal(data, &manifest))
	assert.Equal(t, "aether-newapi/v1", manifest.ContractVersion)
	assert.ElementsMatch(t, []string{
		"X-Aether-Instance-ID",
		"X-Aether-Relay-Context",
		"X-Aether-Relay-Signature",
	}, manifest.RelayHeaders)
	assert.ElementsMatch(t, []string{
		"X-Aether-Instance-ID",
		"X-Aether-Signature-Version",
		"X-Aether-Timestamp",
		"X-Aether-Nonce",
		"X-Aether-Body-SHA256",
		"X-Aether-Signature",
	}, manifest.ServiceHeaders)
	assert.Equal(t, "v2", manifest.ControlHMACV2.SignatureVersion)
	assert.Equal(t,
		"AETHER-CONTROL-V2\n{METHOD}\n{ESCAPED_PATH}\n{RAW_QUERY}\n{INSTANCE_ID}\n{TIMESTAMP}\n{NONCE}\n{BODY_SHA256_LOWER_HEX}",
		manifest.ControlHMACV2.CanonicalPayload,
	)
	assert.Equal(t, 409, manifest.RevisionConflict.HTTPStatus)
	assert.Equal(t, "revision_conflict_response", manifest.RevisionConflict.ResponseSchema)
	assert.Equal(t, "current_config", manifest.RevisionConflict.CurrentConfigField)
	assert.ElementsMatch(t, []string{"requested", "current"}, manifest.RevisionConflict.DiffEntryFields)
	assert.Equal(t, "instance_status_response", manifest.InstanceStatus.ResponseSchema)
	assert.Equal(t, "control_execution_readiness_not_upstream_sla", manifest.InstanceStatus.HealthySemantics)
	assert.ElementsMatch(t, []string{
		"instance_id",
		"healthy",
		"last_sync_at",
		"capability_version",
		"base_revision",
		"uptime_secs",
		"active_channels",
		"routing_mode",
	}, manifest.InstanceStatus.Fields)
	assert.Equal(t, "persisted_config_updated_at_rfc3339_or_null", manifest.InstanceStatus.LastSyncAtSemantics)
	assert.Equal(t, "zero_until_a_durable_uptime_source_exists", manifest.InstanceStatus.UptimeSecsSemantics)
	assert.Equal(t, 503, manifest.InstanceStatus.ConfigStoreUnavailableHTTPStatus)
	assert.ElementsMatch(t, []string{
		"direct_channel",
		"disabled",
		"parallel_shadow",
		"aether_decision",
	}, manifest.InstanceStatus.RoutingModeValues)
	assert.Contains(t, manifest.NewAPIEndpoints, "/api/aether/v1/events")
	assert.Contains(t, manifest.AetherEndpoints, "/api/integrations/new-api/v1/capabilities")
	assert.Equal(t, []string{"direct_channel"}, manifest.ActiveModes)
	assert.Equal(t, "aether-newapi-v1.schema.json", manifest.JSONSchema)
	assert.Equal(t, "aether-newapi-v1.examples.json", manifest.Examples)

	schema, err := os.ReadFile("../docs/contracts/" + manifest.JSONSchema)
	require.NoError(t, err)
	examples, err := os.ReadFile("../docs/contracts/" + manifest.Examples)
	require.NoError(t, err)
	var schemaDocument struct {
		OneOf []struct {
			Ref string `json:"$ref"`
		} `json:"oneOf"`
		Definitions map[string]struct {
			Type                 string   `json:"type"`
			MinLength            *int     `json:"minLength"`
			MaxLength            *int     `json:"maxLength"`
			Pattern              string   `json:"pattern"`
			AdditionalProperties *bool    `json:"additionalProperties"`
			Required             []string `json:"required"`
			Properties           map[string]struct {
				Ref         string   `json:"$ref"`
				Type        any      `json:"type"`
				Description string   `json:"description"`
				Enum        []string `json:"enum"`
				OneOf       []struct {
					Type   string `json:"type"`
					Format string `json:"format"`
				} `json:"oneOf"`
			} `json:"properties"`
			PatternProperties map[string]struct {
				Ref string `json:"$ref"`
			} `json:"patternProperties"`
		} `json:"$defs"`
	}
	require.NoError(t, common.Unmarshal(schema, &schemaDocument))
	requestID, ok := schemaDocument.Definitions["request_id_string"]
	require.True(t, ok)
	assert.Equal(t, "string", requestID.Type)
	require.NotNil(t, requestID.MinLength)
	assert.Equal(t, 1, *requestID.MinLength)
	require.NotNil(t, requestID.MaxLength)
	assert.Equal(t, 100, *requestID.MaxLength)
	assert.Equal(t, `^[!-~]+$`, requestID.Pattern)
	relayContext, ok := schemaDocument.Definitions["relay_context"]
	require.True(t, ok)
	require.NotNil(t, relayContext.AdditionalProperties)
	assert.False(t, *relayContext.AdditionalProperties)
	assert.ElementsMatch(t, relayContext.Required, manifest.RelayContext.RequiredFields)
	assert.Equal(t, "#/$defs/request_id_string", relayContext.Properties["request_id"].Ref)
	assert.Contains(t, relayContext.Required, "group")
	assert.Contains(t, relayContext.Required, "model")
	assert.Contains(t, relayContext.Required, "relay_format")
	assert.Contains(t, schemaDocument.OneOf, struct {
		Ref string `json:"$ref"`
	}{Ref: "#/$defs/instance_status_response"})
	instanceStatus, ok := schemaDocument.Definitions["instance_status_response"]
	require.True(t, ok)
	require.NotNil(t, instanceStatus.AdditionalProperties)
	assert.False(t, *instanceStatus.AdditionalProperties)
	assert.ElementsMatch(t, manifest.InstanceStatus.Fields, instanceStatus.Required)
	assert.Equal(t, "#/$defs/id_string", instanceStatus.Properties["instance_id"].Ref)
	assert.Equal(t, "boolean", instanceStatus.Properties["healthy"].Type)
	assert.Equal(t, manifest.InstanceStatus.HealthySemantics, instanceStatus.Properties["healthy"].Description)
	assert.Equal(t, "string", instanceStatus.Properties["capability_version"].Type)
	assert.Equal(t, "integer", instanceStatus.Properties["base_revision"].Type)
	assert.Equal(t, "integer", instanceStatus.Properties["uptime_secs"].Type)
	assert.Equal(t, "integer", instanceStatus.Properties["active_channels"].Type)
	assert.ElementsMatch(t, manifest.InstanceStatus.RoutingModeValues, instanceStatus.Properties["routing_mode"].Enum)
	require.Len(t, instanceStatus.Properties["last_sync_at"].OneOf, 2)
	assert.ElementsMatch(t, []string{"string", "null"}, []string{
		instanceStatus.Properties["last_sync_at"].OneOf[0].Type,
		instanceStatus.Properties["last_sync_at"].OneOf[1].Type,
	})
	assert.Contains(t, []string{
		instanceStatus.Properties["last_sync_at"].OneOf[0].Format,
		instanceStatus.Properties["last_sync_at"].OneOf[1].Format,
	}, "date-time")
	credentialRotation, ok := schemaDocument.Definitions["credential_rotation_request"]
	require.True(t, ok)
	require.NotNil(t, credentialRotation.AdditionalProperties)
	assert.False(t, *credentialRotation.AdditionalProperties)
	assert.ElementsMatch(t, []string{
		"id",
		"control_secret",
		"relay_signing_secret",
		"transition_expires_at",
		"revoke_previous",
	}, credentialRotation.Required)
	credentialRotationAck, ok := schemaDocument.Definitions["credential_rotation_ack"]
	require.True(t, ok)
	require.NotNil(t, credentialRotationAck.AdditionalProperties)
	assert.False(t, *credentialRotationAck.AdditionalProperties)
	assert.ElementsMatch(t, []string{
		"rotation_id",
		"credential_revision",
		"transition_expires_at",
		"state",
	}, credentialRotationAck.Required)
	revisionConflict, ok := schemaDocument.Definitions["revision_conflict_response"]
	require.True(t, ok)
	require.NotNil(t, revisionConflict.AdditionalProperties)
	assert.False(t, *revisionConflict.AdditionalProperties)
	assert.ElementsMatch(t, []string{
		"error",
		"current_revision",
		"current_config",
		"diff",
	}, revisionConflict.Required)
	assert.Equal(t, "#/$defs/instance_config", revisionConflict.Properties["current_config"].Ref)
	assert.Equal(t, "#/$defs/revision_conflict_diff", revisionConflict.Properties["diff"].Ref)
	revisionConflictDiff, ok := schemaDocument.Definitions["revision_conflict_diff"]
	require.True(t, ok)
	require.NotNil(t, revisionConflictDiff.AdditionalProperties)
	assert.False(t, *revisionConflictDiff.AdditionalProperties)
	assert.Equal(t, "#/$defs/revision_conflict_diff_entry", revisionConflictDiff.PatternProperties["^.+$"].Ref)
	revisionConflictDiffEntry, ok := schemaDocument.Definitions["revision_conflict_diff_entry"]
	require.True(t, ok)
	require.NotNil(t, revisionConflictDiffEntry.AdditionalProperties)
	assert.False(t, *revisionConflictDiffEntry.AdditionalProperties)
	assert.ElementsMatch(t, []string{"requested", "current"}, revisionConflictDiffEntry.Required)
	var exampleDocument map[string]any
	require.NoError(t, common.Unmarshal(examples, &exampleDocument))
	assert.Contains(t, exampleDocument, "relay_context")
	assert.Contains(t, exampleDocument, "events_response")
	assert.Contains(t, exampleDocument, "pricing_response")
	assert.Contains(t, exampleDocument, "snapshot_response")
	assert.Contains(t, exampleDocument, "revision_conflict_response")
	assert.Contains(t, exampleDocument, "instance_status_response")
	var contractExamples struct {
		RelaySignatureVector struct {
			SigningSecret  string `json:"signing_secret"`
			EncodedContext string `json:"encoded_context"`
			SignatureHex   string `json:"signature_hex"`
		} `json:"relay_signature_vector"`
		ServiceSignatureVector struct {
			SigningSecret    string `json:"signing_secret"`
			CanonicalPayload string `json:"canonical_payload"`
			SignatureHex     string `json:"signature_hex"`
		} `json:"service_signature_vector"`
		ControlHMACV2SignatureVector struct {
			SigningSecret    string `json:"signing_secret"`
			RawBody          string `json:"raw_body"`
			BodySHA256       string `json:"body_sha256"`
			CanonicalPayload string `json:"canonical_payload"`
			SignatureHex     string `json:"signature_hex"`
		} `json:"control_hmac_v2_signature_vector"`
		RevisionConflictResponse struct {
			Error           string `json:"error"`
			CurrentRevision int64  `json:"current_revision"`
			CurrentConfig   struct {
				InstanceID   string `json:"instance_id"`
				BaseRevision int64  `json:"base_revision"`
			} `json:"current_config"`
			Diff map[string]struct {
				Requested any `json:"requested"`
				Current   any `json:"current"`
			} `json:"diff"`
		} `json:"revision_conflict_response"`
		InstanceStatusResponse struct {
			InstanceID        string  `json:"instance_id"`
			Healthy           bool    `json:"healthy"`
			LastSyncAt        *string `json:"last_sync_at"`
			CapabilityVersion string  `json:"capability_version"`
			BaseRevision      int64   `json:"base_revision"`
			UptimeSecs        uint64  `json:"uptime_secs"`
			ActiveChannels    uint64  `json:"active_channels"`
			RoutingMode       string  `json:"routing_mode"`
		} `json:"instance_status_response"`
	}
	require.NoError(t, common.Unmarshal(examples, &contractExamples))
	require.NotEmpty(t, contractExamples.RelaySignatureVector.SigningSecret)
	require.NotEmpty(t, contractExamples.RelaySignatureVector.EncodedContext)
	require.NotEmpty(t, contractExamples.RelaySignatureVector.SignatureHex)
	decodedContext, err := base64.RawURLEncoding.DecodeString(contractExamples.RelaySignatureVector.EncodedContext)
	require.NoError(t, err)
	assert.Contains(t, string(decodedContext), `"request_id":"req_01JZ8K2A9Y"`)
	assert.Equal(t,
		common.GenerateHMACWithKey(
			[]byte(contractExamples.RelaySignatureVector.SigningSecret),
			contractExamples.RelaySignatureVector.EncodedContext,
		),
		contractExamples.RelaySignatureVector.SignatureHex,
	)
	require.NotEmpty(t, contractExamples.ServiceSignatureVector.SigningSecret)
	assert.Equal(t,
		"GET\n/api/aether/v1/pricing\ngroup=%E4%B8%AD%E6%96%87+pro&cursor=a%2Fb%3F\n1784073600\nnonce-1234567890",
		contractExamples.ServiceSignatureVector.CanonicalPayload,
	)
	assert.Equal(t,
		common.GenerateHMACWithKey(
			[]byte(contractExamples.ServiceSignatureVector.SigningSecret),
			contractExamples.ServiceSignatureVector.CanonicalPayload,
		),
		contractExamples.ServiceSignatureVector.SignatureHex,
	)
	require.NotEmpty(t, contractExamples.ControlHMACV2SignatureVector.SigningSecret)
	controlBodyDigest := sha256.Sum256([]byte(contractExamples.ControlHMACV2SignatureVector.RawBody))
	assert.Equal(t,
		hex.EncodeToString(controlBodyDigest[:]),
		contractExamples.ControlHMACV2SignatureVector.BodySHA256,
	)
	assert.Equal(t,
		"AETHER-CONTROL-V2\nPUT\n/api/integrations/new-api/v1/instances/aether-primary\n\naether-primary\n1784073600\nnonce-control-v2-123456\ned3912c274f42d73d47de42f64632bc09cce1e430b97cc76294ff80f8103cb47",
		contractExamples.ControlHMACV2SignatureVector.CanonicalPayload,
	)
	assert.Equal(t,
		common.GenerateHMACWithKey(
			[]byte(contractExamples.ControlHMACV2SignatureVector.SigningSecret),
			contractExamples.ControlHMACV2SignatureVector.CanonicalPayload,
		),
		contractExamples.ControlHMACV2SignatureVector.SignatureHex,
	)
	assert.NotEmpty(t, contractExamples.RevisionConflictResponse.Error)
	assert.Positive(t, contractExamples.RevisionConflictResponse.CurrentRevision)
	assert.Equal(t, contractExamples.RevisionConflictResponse.CurrentRevision, contractExamples.RevisionConflictResponse.CurrentConfig.BaseRevision)
	assert.NotEmpty(t, contractExamples.RevisionConflictResponse.CurrentConfig.InstanceID)
	require.Contains(t, contractExamples.RevisionConflictResponse.Diff, "route_profile")
	assert.NotNil(t, contractExamples.RevisionConflictResponse.Diff["route_profile"].Requested)
	assert.NotNil(t, contractExamples.RevisionConflictResponse.Diff["route_profile"].Current)
	assert.Equal(t, "aether-primary", contractExamples.InstanceStatusResponse.InstanceID)
	assert.True(t, contractExamples.InstanceStatusResponse.Healthy)
	require.NotNil(t, contractExamples.InstanceStatusResponse.LastSyncAt)
	assert.Equal(t, "2026-07-18T10:00:00Z", *contractExamples.InstanceStatusResponse.LastSyncAt)
	assert.Equal(t, "0.1.0", contractExamples.InstanceStatusResponse.CapabilityVersion)
	assert.Positive(t, contractExamples.InstanceStatusResponse.BaseRevision)
	assert.Zero(t, contractExamples.InstanceStatusResponse.UptimeSecs)
	assert.Positive(t, contractExamples.InstanceStatusResponse.ActiveChannels)
	assert.Equal(t, "direct_channel", contractExamples.InstanceStatusResponse.RoutingMode)

	bundle := make([]byte, 0, len(schema)+len(examples)+1)
	bundle = append(bundle, schema...)
	bundle = append(bundle, 0)
	bundle = append(bundle, examples...)
	digest := sha256.Sum256(bundle)
	assert.Equal(t, hex.EncodeToString(digest[:]), manifest.BundleSHA256)
}
