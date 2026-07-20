package model

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const aetherPendingCredentialRotationHashDomain = "new-api-aether-pending-credential-rotation-v1"

// AetherIntegrationPendingCredentialRotation holds new credentials until the
// remote AETHER instance has acknowledged the same rotation. Its secret fields
// are deliberately excluded from JSON and String so they cannot escape in API
// responses or routine logs.
type AetherIntegrationPendingCredentialRotation struct {
	Id                          int    `json:"id"`
	IntegrationID               int    `json:"integration_id" gorm:"uniqueIndex:idx_aether_pending_rotation_integration"`
	InstanceID                  string `json:"instance_id" gorm:"uniqueIndex:idx_aether_pending_rotation_instance_id,priority:1;type:varchar(128)"`
	RotationID                  string `json:"rotation_id" gorm:"uniqueIndex:idx_aether_pending_rotation_instance_id,priority:2;type:varchar(255)"`
	PayloadSHA256               string `json:"payload_sha256" gorm:"type:char(64)"`
	BaseConfigRevision          int64  `json:"base_config_revision" gorm:"bigint"`
	ReservedConfigRevision      int64  `json:"reserved_config_revision" gorm:"bigint"`
	RevokePrevious              bool   `json:"revoke_previous"`
	ControlSecretEncrypted      string `json:"-" gorm:"type:text"`
	RelaySigningSecretEncrypted string `json:"-" gorm:"type:text"`
	TransitionSecretsExpireAt   int64  `json:"transition_secrets_expire_at" gorm:"bigint"`
	CreatedTime                 int64  `json:"created_time" gorm:"bigint"`
	UpdatedTime                 int64  `json:"updated_time" gorm:"bigint"`
}

// AetherIntegrationPendingCredentialRotationRequest carries plaintext only
// until PrepareAetherIntegrationPendingCredentialRotation encrypts it.
type AetherIntegrationPendingCredentialRotationRequest struct {
	RotationID          string    `json:"-"`
	ControlSecret       string    `json:"-"`
	RelaySigningSecret  string    `json:"-"`
	TransitionExpiresAt time.Time `json:"-"`
	RevokePrevious      bool      `json:"revoke_previous"`
}

func (AetherIntegrationPendingCredentialRotation) TableName() string {
	return "aether_integration_pending_credential_rotations"
}

func (rotation AetherIntegrationPendingCredentialRotation) String() string {
	return fmt.Sprintf(
		"AetherIntegrationPendingCredentialRotation{Id:%d IntegrationID:%d InstanceID:%q RotationID:%q PayloadSHA256:%q BaseConfigRevision:%d ReservedConfigRevision:%d RevokePrevious:%t ControlSecretEncrypted:[REDACTED] RelaySigningSecretEncrypted:[REDACTED] TransitionSecretsExpireAt:%d CreatedTime:%d UpdatedTime:%d}",
		rotation.Id,
		rotation.IntegrationID,
		rotation.InstanceID,
		rotation.RotationID,
		rotation.PayloadSHA256,
		rotation.BaseConfigRevision,
		rotation.ReservedConfigRevision,
		rotation.RevokePrevious,
		rotation.TransitionSecretsExpireAt,
		rotation.CreatedTime,
		rotation.UpdatedTime,
	)
}

func (rotation AetherIntegrationPendingCredentialRotation) GoString() string {
	return rotation.String()
}

func (request AetherIntegrationPendingCredentialRotationRequest) String() string {
	return fmt.Sprintf(
		"AetherIntegrationPendingCredentialRotationRequest{RotationID:%q ControlSecret:[REDACTED] RelaySigningSecret:[REDACTED] TransitionExpiresAt:%d RevokePrevious:%t}",
		request.RotationID,
		request.TransitionExpiresAt.Unix(),
		request.RevokePrevious,
	)
}

func (request AetherIntegrationPendingCredentialRotationRequest) GoString() string {
	return request.String()
}

func (rotation *AetherIntegrationPendingCredentialRotation) Secrets() (controlSecret string, relaySigningSecret string, err error) {
	if rotation == nil {
		return "", "", errors.New("aether pending credential rotation is nil")
	}
	controlSecret, err = common.DecryptAetherSecret(rotation.ControlSecretEncrypted)
	if err != nil {
		return "", "", err
	}
	relaySigningSecret, err = common.DecryptAetherSecret(rotation.RelaySigningSecretEncrypted)
	if err != nil {
		return "", "", err
	}
	return controlSecret, relaySigningSecret, nil
}

func GetAetherIntegrationPendingCredentialRotation(integrationID int) (*AetherIntegrationPendingCredentialRotation, error) {
	if integrationID <= 0 {
		return nil, errors.New("invalid aether integration ID")
	}
	pending := &AetherIntegrationPendingCredentialRotation{}
	if err := DB.Where("integration_id = ?", integrationID).First(pending).Error; err != nil {
		return nil, err
	}
	return pending, nil
}

// PrepareAetherIntegrationPendingCredentialRotation atomically reserves the
// next local configuration revision and persists the encrypted credentials.
// It intentionally does not alter any active or transition credential fields
// on AetherIntegration; promotion must wait for a separate remote ACK path.
func PrepareAetherIntegrationPendingCredentialRotation(
	integration *AetherIntegration,
	baseRevision int64,
	request AetherIntegrationPendingCredentialRotationRequest,
) (*AetherIntegrationPendingCredentialRotation, *AetherIntegrationConflict, error) {
	if integration == nil || integration.Id <= 0 {
		return nil, nil, errors.New("aether integration is required")
	}
	if baseRevision <= 0 {
		return nil, nil, errors.New("aether integration base revision is required")
	}
	nextRevision := baseRevision + 1
	if nextRevision <= baseRevision {
		return nil, nil, errors.New("aether integration revision overflow")
	}

	rotationID := strings.TrimSpace(request.RotationID)
	if rotationID == "" {
		return nil, nil, errors.New("aether credential rotation ID is required")
	}
	if strings.Contains(rotationID, "\x00") || len(rotationID) > 255 {
		return nil, nil, errors.New("aether credential rotation ID is invalid")
	}
	if strings.TrimSpace(request.ControlSecret) == "" || strings.TrimSpace(request.RelaySigningSecret) == "" {
		return nil, nil, errors.New("aether control and relay signing secrets are required")
	}
	if err := validateAetherCredentialPair(request.ControlSecret, request.RelaySigningSecret); err != nil {
		return nil, nil, err
	}
	if request.RevokePrevious {
		return nil, nil, errors.New("aether credential rotation cannot revoke and replace secrets together")
	}

	now := time.Now().UTC()
	transitionExpiresAt := request.TransitionExpiresAt.UTC().Unix()
	if transitionExpiresAt <= now.Unix() {
		return nil, nil, errors.New("aether credential transition expiry must be in the future")
	}
	var prepared *AetherIntegrationPendingCredentialRotation
	var conflictDetected bool
	err := DB.Transaction(func(tx *gorm.DB) error {
		current := &AetherIntegration{}
		if err := lockForUpdate(tx).Where("id = ?", integration.Id).First(current).Error; err != nil {
			return err
		}
		payloadSHA256 := aetherPendingCredentialRotationPayloadSHA256(
			current.Id,
			current.InstanceID,
			baseRevision,
			rotationID,
			request.ControlSecret,
			request.RelaySigningSecret,
			transitionExpiresAt,
			request.RevokePrevious,
		)

		existing := &AetherIntegrationPendingCredentialRotation{}
		err := lockForUpdate(tx).Where("integration_id = ?", integration.Id).First(existing).Error
		if err == nil {
			if existing.RotationID != rotationID {
				return errors.New("aether credential rotation is already pending")
			}
			if existing.PayloadSHA256 != payloadSHA256 {
				return errors.New("aether credential rotation ID is already bound to a different payload")
			}
			if existing.BaseConfigRevision != baseRevision || existing.ReservedConfigRevision != nextRevision || current.ConfigRevision != nextRevision {
				conflictDetected = true
				return nil
			}
			prepared = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if current.ConfigRevision != baseRevision {
			conflictDetected = true
			return nil
		}

		if err := validateAetherPendingCredentialRotationDomains(current, request, now); err != nil {
			return err
		}
		controlSecretEncrypted, err := common.EncryptAetherSecret(request.ControlSecret)
		if err != nil {
			return err
		}
		relaySigningSecretEncrypted, err := common.EncryptAetherSecret(request.RelaySigningSecret)
		if err != nil {
			return err
		}

		updatedTime := common.GetTimestamp()
		result := tx.Model(&AetherIntegration{}).
			Where("id = ? AND config_revision = ?", current.Id, baseRevision).
			Updates(map[string]interface{}{
				"config_revision": nextRevision,
				"updated_time":    updatedTime,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			conflictDetected = true
			return nil
		}

		pending := &AetherIntegrationPendingCredentialRotation{
			IntegrationID:               current.Id,
			InstanceID:                  current.InstanceID,
			RotationID:                  rotationID,
			PayloadSHA256:               payloadSHA256,
			BaseConfigRevision:          baseRevision,
			ReservedConfigRevision:      nextRevision,
			RevokePrevious:              request.RevokePrevious,
			ControlSecretEncrypted:      controlSecretEncrypted,
			RelaySigningSecretEncrypted: relaySigningSecretEncrypted,
			TransitionSecretsExpireAt:   transitionExpiresAt,
			CreatedTime:                 updatedTime,
			UpdatedTime:                 updatedTime,
		}
		if err := tx.Create(pending).Error; err != nil {
			return err
		}
		prepared = pending
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if conflictDetected {
		latest := &AetherIntegration{}
		if err := DB.Where("id = ?", integration.Id).First(latest).Error; err != nil {
			return nil, nil, err
		}
		return nil, NewAetherIntegrationConflict(latest, aetherIntegrationSharedConfig(integration)), nil
	}
	return prepared, nil, nil
}

// PromoteAetherIntegrationPendingCredentialRotation makes an acknowledged
// pending rotation active. The reserved local revision is retained because it
// was allocated when the rotation was prepared.
func PromoteAetherIntegrationPendingCredentialRotation(
	integrationID int,
	rotationID string,
	remoteConfigRevision int64,
) (*AetherIntegration, error) {
	if integrationID <= 0 {
		return nil, errors.New("invalid aether integration ID")
	}
	rotationID = strings.TrimSpace(rotationID)
	if rotationID == "" {
		return nil, errors.New("aether credential rotation ID is required")
	}
	if remoteConfigRevision <= 0 {
		return nil, errors.New("aether remote configuration revision is required")
	}

	var promoted *AetherIntegration
	err := DB.Transaction(func(tx *gorm.DB) error {
		current := &AetherIntegration{}
		if err := lockForUpdate(tx).Where("id = ?", integrationID).First(current).Error; err != nil {
			return err
		}
		pending := &AetherIntegrationPendingCredentialRotation{}
		if err := lockForUpdate(tx).Where("integration_id = ?", integrationID).First(pending).Error; err != nil {
			return err
		}
		if pending.InstanceID != current.InstanceID || pending.RotationID != rotationID {
			return errors.New("aether pending credential rotation does not match")
		}
		if pending.ReservedConfigRevision != pending.BaseConfigRevision+1 || current.ConfigRevision != pending.ReservedConfigRevision {
			return errors.New("aether pending credential rotation reserved revision does not match")
		}
		controlSecret, relaySigningSecret, err := pending.Secrets()
		if err != nil {
			return err
		}
		if err := validateAetherPendingCredentialRotationDomains(current, AetherIntegrationPendingCredentialRotationRequest{
			ControlSecret:       controlSecret,
			RelaySigningSecret:  relaySigningSecret,
			TransitionExpiresAt: time.Unix(pending.TransitionSecretsExpireAt, 0).UTC(),
			RevokePrevious:      pending.RevokePrevious,
		}, time.Now().UTC()); err != nil {
			return err
		}

		now := common.GetTimestamp()
		current.PreviousControlSecretEncrypted = current.ControlSecretEncrypted
		current.PreviousRelaySigningSecretEncrypted = current.RelaySigningSecretEncrypted
		current.ControlSecretEncrypted = pending.ControlSecretEncrypted
		current.RelaySigningSecretEncrypted = pending.RelaySigningSecretEncrypted
		current.TransitionSecretsExpireAt = pending.TransitionSecretsExpireAt
		current.RemoteConfigRevision = remoteConfigRevision
		current.LastSyncTime = now
		current.UpdatedTime = now
		result := tx.Model(&AetherIntegration{}).
			Where("id = ? AND config_revision = ?", current.Id, pending.ReservedConfigRevision).
			Updates(map[string]interface{}{
				"control_secret_encrypted":                current.ControlSecretEncrypted,
				"relay_signing_secret_encrypted":          current.RelaySigningSecretEncrypted,
				"previous_control_secret_encrypted":       current.PreviousControlSecretEncrypted,
				"previous_relay_signing_secret_encrypted": current.PreviousRelaySigningSecretEncrypted,
				"transition_secrets_expire_at":            current.TransitionSecretsExpireAt,
				"remote_config_revision":                  current.RemoteConfigRevision,
				"last_sync_time":                          current.LastSyncTime,
				"updated_time":                            current.UpdatedTime,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("aether pending credential rotation promotion conflict")
		}
		if err := tx.Where("id = ? AND integration_id = ?", pending.Id, current.Id).Delete(&AetherIntegrationPendingCredentialRotation{}).Error; err != nil {
			return err
		}
		promoted = current
		return nil
	})
	if err != nil {
		return nil, err
	}
	return promoted, nil
}

// CancelAetherIntegrationPendingCredentialRotation is only safe before a
// remote request has been attempted. It restores the exact reserved revision.
func CancelAetherIntegrationPendingCredentialRotation(
	integrationID int,
	rotationID string,
	expectedReservedRevision int64,
) (*AetherIntegration, error) {
	if integrationID <= 0 {
		return nil, errors.New("invalid aether integration ID")
	}
	rotationID = strings.TrimSpace(rotationID)
	if rotationID == "" {
		return nil, errors.New("aether credential rotation ID is required")
	}
	if expectedReservedRevision <= 0 {
		return nil, errors.New("aether pending credential rotation reserved revision is required")
	}

	var canceled *AetherIntegration
	err := DB.Transaction(func(tx *gorm.DB) error {
		current := &AetherIntegration{}
		if err := lockForUpdate(tx).Where("id = ?", integrationID).First(current).Error; err != nil {
			return err
		}
		pending := &AetherIntegrationPendingCredentialRotation{}
		if err := lockForUpdate(tx).Where("integration_id = ?", integrationID).First(pending).Error; err != nil {
			return err
		}
		if pending.RotationID != rotationID || pending.ReservedConfigRevision != expectedReservedRevision ||
			pending.ReservedConfigRevision != pending.BaseConfigRevision+1 || current.ConfigRevision != expectedReservedRevision {
			return errors.New("aether pending credential rotation reserved revision does not match")
		}
		current.ConfigRevision = pending.BaseConfigRevision
		current.UpdatedTime = common.GetTimestamp()
		result := tx.Model(&AetherIntegration{}).
			Where("id = ? AND config_revision = ?", current.Id, expectedReservedRevision).
			Updates(map[string]interface{}{
				"config_revision": current.ConfigRevision,
				"updated_time":    current.UpdatedTime,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("aether pending credential rotation cancellation conflict")
		}
		if err := tx.Where("id = ? AND integration_id = ?", pending.Id, current.Id).Delete(&AetherIntegrationPendingCredentialRotation{}).Error; err != nil {
			return err
		}
		canceled = current
		return nil
	})
	if err != nil {
		return nil, err
	}
	return canceled, nil
}

func rejectAetherIntegrationPendingCredentialRotation(tx *gorm.DB, integrationID int) error {
	pending := &AetherIntegrationPendingCredentialRotation{}
	err := lockForUpdate(tx).Where("integration_id = ?", integrationID).First(pending).Error
	if err == nil {
		if pending.IntegrationID == integrationID {
			return errors.New("aether credential rotation is pending")
		}
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

func validateAetherPendingCredentialRotationDomains(current *AetherIntegration, request AetherIntegrationPendingCredentialRotationRequest, now time.Time) error {
	currentControlSecret, currentRelaySigningSecret, err := current.Secrets()
	if err != nil {
		return err
	}
	controlSecrets := []string{request.ControlSecret, currentControlSecret}
	relaySigningSecrets := []string{request.RelaySigningSecret, currentRelaySigningSecret}
	if current.TransitionSecretsExpireAt > now.Unix() {
		if current.PreviousControlSecretEncrypted == "" || current.PreviousRelaySigningSecretEncrypted == "" {
			return errors.New("aether transition credentials are incomplete")
		}
		previousControlSecret, err := common.DecryptAetherSecret(current.PreviousControlSecretEncrypted)
		if err != nil {
			return err
		}
		previousRelaySigningSecret, err := common.DecryptAetherSecret(current.PreviousRelaySigningSecretEncrypted)
		if err != nil {
			return err
		}
		controlSecrets = append(controlSecrets, previousControlSecret)
		relaySigningSecrets = append(relaySigningSecrets, previousRelaySigningSecret)
	}
	if aetherCredentialDomainsOverlap(controlSecrets, relaySigningSecrets) {
		return errors.New("aether control and relay signing secret domains must not overlap during transition")
	}
	return nil
}

func aetherPendingCredentialRotationPayloadSHA256(
	integrationID int,
	instanceID string,
	baseRevision int64,
	rotationID string,
	controlSecret string,
	relaySigningSecret string,
	transitionExpiresAt int64,
	revokePrevious bool,
) string {
	digest := hmac.New(sha256.New, []byte(common.CryptoSecret))
	writeAetherPendingCredentialRotationHashField(digest, []byte(aetherPendingCredentialRotationHashDomain))
	writeAetherPendingCredentialRotationHashField(digest, []byte(instanceID))
	writeAetherPendingCredentialRotationHashInt64(digest, int64(integrationID))
	writeAetherPendingCredentialRotationHashInt64(digest, baseRevision)
	writeAetherPendingCredentialRotationHashField(digest, []byte(rotationID))
	writeAetherPendingCredentialRotationHashField(digest, []byte(controlSecret))
	writeAetherPendingCredentialRotationHashField(digest, []byte(relaySigningSecret))
	writeAetherPendingCredentialRotationHashInt64(digest, transitionExpiresAt)
	if revokePrevious {
		writeAetherPendingCredentialRotationHashField(digest, []byte{1})
	} else {
		writeAetherPendingCredentialRotationHashField(digest, []byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writeAetherPendingCredentialRotationHashField(digest interface{ Write([]byte) (int, error) }, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(value)
}

func writeAetherPendingCredentialRotationHashInt64(digest interface{ Write([]byte) (int, error) }, value int64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	_, _ = digest.Write(encoded[:])
}
