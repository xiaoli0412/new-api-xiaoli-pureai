package model

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	AetherExecutionModeDisabled       = "disabled"
	AetherExecutionModeDirectChannel  = "direct_channel"
	AetherExecutionModeParallelShadow = "parallel_shadow"
	AetherExecutionModeAetherDecision = "aether_decision"
)

type AetherIntegration struct {
	Id                                  int    `json:"id"`
	ChannelID                           int    `json:"channel_id" gorm:"uniqueIndex"`
	InstanceID                          string `json:"instance_id" gorm:"uniqueIndex;type:varchar(128)"`
	RouteProfile                        string `json:"route_profile" gorm:"type:varchar(128);default:''"`
	ExecutionMode                       string `json:"execution_mode" gorm:"type:varchar(32);default:'direct_channel'"`
	CapabilityVersion                   string `json:"capability_version" gorm:"type:varchar(64);default:''"`
	Enabled                             bool   `json:"enabled"`
	ConfigRevision                      int64  `json:"config_revision" gorm:"bigint;default:1"`
	RemoteConfigRevision                int64  `json:"remote_config_revision" gorm:"bigint"`
	ControlSecretEncrypted              string `json:"-" gorm:"type:text"`
	RelaySigningSecretEncrypted         string `json:"-" gorm:"type:text"`
	PreviousControlSecretEncrypted      string `json:"-" gorm:"type:text"`
	PreviousRelaySigningSecretEncrypted string `json:"-" gorm:"type:text"`
	TransitionSecretsExpireAt           int64  `json:"-" gorm:"bigint"`
	LastSyncTime                        int64  `json:"last_sync_time" gorm:"bigint"`
	LastHealthTime                      int64  `json:"last_health_time" gorm:"bigint"`
	LastHealthStatus                    string `json:"last_health_status" gorm:"type:varchar(32);default:''"`
	CreatedTime                         int64  `json:"created_time" gorm:"bigint"`
	UpdatedTime                         int64  `json:"updated_time" gorm:"bigint"`
}

type AetherIntegrationSharedConfig struct {
	RouteProfile      string `json:"route_profile"`
	ExecutionMode     string `json:"execution_mode"`
	Enabled           bool   `json:"enabled"`
	CapabilityVersion string `json:"capability_version"`
}

type AetherIntegrationConflict struct {
	CurrentRevision int64                                  `json:"current_revision"`
	Current         AetherIntegrationSharedConfig          `json:"current"`
	Diff            map[string]AetherIntegrationConfigDiff `json:"diff"`
}

type AetherIntegrationConfigDiff struct {
	Requested any `json:"requested"`
	Current   any `json:"current"`
}

type AetherIntegrationSecretRotation struct {
	ControlSecret       string
	RelaySigningSecret  string
	TransitionExpiresAt time.Time
	RevokePrevious      bool
}

func ValidateAetherExecutionMode(mode string) error {
	switch strings.TrimSpace(mode) {
	case AetherExecutionModeDisabled, AetherExecutionModeDirectChannel:
		return nil
	case AetherExecutionModeParallelShadow, AetherExecutionModeAetherDecision:
		return fmt.Errorf("aether execution mode %q is not enabled in this release", mode)
	default:
		return fmt.Errorf("invalid aether execution mode %q", mode)
	}
}

func validateAetherCredentialPair(controlSecret string, relaySigningSecret string) error {
	if controlSecret == relaySigningSecret {
		return errors.New("aether control and relay signing secrets must differ")
	}
	return nil
}

func aetherCredentialDomainsOverlap(controlSecrets []string, relaySigningSecrets []string) bool {
	for _, controlSecret := range controlSecrets {
		for _, relaySigningSecret := range relaySigningSecrets {
			if controlSecret == relaySigningSecret {
				return true
			}
		}
	}
	return false
}

func (integration *AetherIntegration) SetSecrets(controlSecret string, relaySigningSecret string) error {
	if strings.TrimSpace(controlSecret) == "" || strings.TrimSpace(relaySigningSecret) == "" {
		return errors.New("aether control and relay signing secrets are required")
	}
	if err := validateAetherCredentialPair(controlSecret, relaySigningSecret); err != nil {
		return err
	}
	controlSecretEncrypted, err := common.EncryptAetherSecret(controlSecret)
	if err != nil {
		return err
	}
	relaySigningSecretEncrypted, err := common.EncryptAetherSecret(relaySigningSecret)
	if err != nil {
		return err
	}
	integration.ControlSecretEncrypted = controlSecretEncrypted
	integration.RelaySigningSecretEncrypted = relaySigningSecretEncrypted
	integration.PreviousControlSecretEncrypted = ""
	integration.PreviousRelaySigningSecretEncrypted = ""
	integration.TransitionSecretsExpireAt = 0
	return nil
}

func (integration *AetherIntegration) Secrets() (controlSecret string, relaySigningSecret string, err error) {
	if integration == nil {
		return "", "", errors.New("aether integration is nil")
	}
	controlSecret, err = common.DecryptAetherSecret(integration.ControlSecretEncrypted)
	if err != nil {
		return "", "", err
	}
	relaySigningSecret, err = common.DecryptAetherSecret(integration.RelaySigningSecretEncrypted)
	if err != nil {
		return "", "", err
	}
	return controlSecret, relaySigningSecret, nil
}

func (integration *AetherIntegration) ActiveControlSecrets(now time.Time) ([]string, error) {
	if err := integration.ExpireTransitionSecrets(now); err != nil {
		return nil, err
	}
	controlSecret, _, err := integration.Secrets()
	if err != nil {
		return nil, err
	}
	secrets := []string{controlSecret}
	if integration.TransitionSecretsExpireAt <= now.Unix() || integration.PreviousControlSecretEncrypted == "" {
		return secrets, nil
	}
	previousControlSecret, err := common.DecryptAetherSecret(integration.PreviousControlSecretEncrypted)
	if err != nil {
		return nil, err
	}
	if previousControlSecret != controlSecret {
		secrets = append(secrets, previousControlSecret)
	}
	return secrets, nil
}

func (integration *AetherIntegration) ActiveRelaySigningSecrets(now time.Time) ([]string, error) {
	if err := integration.ExpireTransitionSecrets(now); err != nil {
		return nil, err
	}
	_, relaySigningSecret, err := integration.Secrets()
	if err != nil {
		return nil, err
	}
	secrets := []string{relaySigningSecret}
	if integration.TransitionSecretsExpireAt <= now.Unix() || integration.PreviousRelaySigningSecretEncrypted == "" {
		return secrets, nil
	}
	previousRelaySigningSecret, err := common.DecryptAetherSecret(integration.PreviousRelaySigningSecretEncrypted)
	if err != nil {
		return nil, err
	}
	if previousRelaySigningSecret != relaySigningSecret {
		secrets = append(secrets, previousRelaySigningSecret)
	}
	return secrets, nil
}

func (integration *AetherIntegration) ExpireTransitionSecrets(now time.Time) error {
	if integration == nil || integration.Id <= 0 {
		return nil
	}
	if integration.TransitionSecretsExpireAt > now.Unix() {
		return nil
	}
	if integration.TransitionSecretsExpireAt == 0 &&
		integration.PreviousControlSecretEncrypted == "" &&
		integration.PreviousRelaySigningSecretEncrypted == "" {
		return nil
	}
	result := DB.Model(&AetherIntegration{}).
		Where("id = ? AND transition_secrets_expire_at <= ?", integration.Id, now.Unix()).
		Updates(map[string]interface{}{
			"previous_control_secret_encrypted":       "",
			"previous_relay_signing_secret_encrypted": "",
			"transition_secrets_expire_at":            0,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		latest := &AetherIntegration{}
		if err := DB.Where("id = ?", integration.Id).First(latest).Error; err != nil {
			return err
		}
		*integration = *latest
		return nil
	}
	integration.PreviousControlSecretEncrypted = ""
	integration.PreviousRelaySigningSecretEncrypted = ""
	integration.TransitionSecretsExpireAt = 0
	return nil
}

func GetAetherIntegrationByChannelID(channelID int) (*AetherIntegration, error) {
	if channelID <= 0 {
		return nil, errors.New("invalid aether channel ID")
	}
	integration := &AetherIntegration{}
	if err := DB.Where("channel_id = ?", channelID).First(integration).Error; err != nil {
		return nil, err
	}
	if err := integration.ExpireTransitionSecrets(time.Now().UTC()); err != nil {
		return nil, err
	}
	return integration, nil
}

func GetAetherIntegrationByInstanceID(instanceID string) (*AetherIntegration, error) {
	if strings.TrimSpace(instanceID) == "" {
		return nil, errors.New("invalid aether instance ID")
	}
	integration := &AetherIntegration{}
	if err := DB.Where("instance_id = ?", instanceID).First(integration).Error; err != nil {
		return nil, err
	}
	if err := integration.ExpireTransitionSecrets(time.Now().UTC()); err != nil {
		return nil, err
	}
	return integration, nil
}

func UpdateAetherIntegrationSharedConfig(integration *AetherIntegration, baseRevision int64, config AetherIntegrationSharedConfig) (*AetherIntegration, *AetherIntegrationConflict, error) {
	return UpdateAetherIntegrationSharedConfigWithSecretRotation(integration, baseRevision, config, nil)
}

func UpdateAetherIntegrationSharedConfigWithSecretRotation(integration *AetherIntegration, baseRevision int64, config AetherIntegrationSharedConfig, secretRotation *AetherIntegrationSecretRotation) (*AetherIntegration, *AetherIntegrationConflict, error) {
	if integration == nil || integration.Id <= 0 {
		return nil, nil, errors.New("aether integration is required")
	}
	if baseRevision <= 0 {
		return nil, nil, errors.New("aether integration base revision is required")
	}
	if err := ValidateAetherExecutionMode(config.ExecutionMode); err != nil {
		return nil, nil, err
	}
	if secretRotation != nil {
		hasControlSecret := strings.TrimSpace(secretRotation.ControlSecret) != ""
		hasRelaySigningSecret := strings.TrimSpace(secretRotation.RelaySigningSecret) != ""
		if hasControlSecret != hasRelaySigningSecret {
			return nil, nil, errors.New("aether control and relay signing secrets must be updated together")
		}
		if hasControlSecret {
			if err := validateAetherCredentialPair(secretRotation.ControlSecret, secretRotation.RelaySigningSecret); err != nil {
				return nil, nil, err
			}
		}
		if secretRotation.RevokePrevious && hasControlSecret {
			return nil, nil, errors.New("aether credential rotation cannot revoke and replace secrets together")
		}
	}

	var updated *AetherIntegration
	var conflictDetected bool
	err := DB.Transaction(func(tx *gorm.DB) error {
		current := &AetherIntegration{}
		if err := lockForUpdate(tx).Where("id = ?", integration.Id).First(current).Error; err != nil {
			return err
		}
		if err := rejectAetherIntegrationPendingCredentialRotation(tx, current.Id); err != nil {
			return err
		}
		if current.ConfigRevision != baseRevision {
			conflictDetected = true
			return nil
		}

		current.RouteProfile = strings.TrimSpace(config.RouteProfile)
		current.ExecutionMode = strings.TrimSpace(config.ExecutionMode)
		current.Enabled = config.Enabled
		current.CapabilityVersion = strings.TrimSpace(config.CapabilityVersion)
		if secretRotation != nil && strings.TrimSpace(secretRotation.ControlSecret) != "" {
			now := time.Now().UTC()
			if secretRotation.TransitionExpiresAt.After(now) {
				currentControlSecret, currentRelaySigningSecret, err := current.Secrets()
				if err != nil {
					return err
				}
				controlSecrets := []string{secretRotation.ControlSecret, currentControlSecret}
				relaySigningSecrets := []string{secretRotation.RelaySigningSecret, currentRelaySigningSecret}
				if current.TransitionSecretsExpireAt > now.Unix() {
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
			}
			newControlSecretEncrypted, err := common.EncryptAetherSecret(secretRotation.ControlSecret)
			if err != nil {
				return err
			}
			newRelaySigningSecretEncrypted, err := common.EncryptAetherSecret(secretRotation.RelaySigningSecret)
			if err != nil {
				return err
			}
			if secretRotation.TransitionExpiresAt.After(now) {
				current.PreviousControlSecretEncrypted = current.ControlSecretEncrypted
				current.PreviousRelaySigningSecretEncrypted = current.RelaySigningSecretEncrypted
				current.TransitionSecretsExpireAt = secretRotation.TransitionExpiresAt.Unix()
			} else {
				current.PreviousControlSecretEncrypted = ""
				current.PreviousRelaySigningSecretEncrypted = ""
				current.TransitionSecretsExpireAt = 0
			}
			current.ControlSecretEncrypted = newControlSecretEncrypted
			current.RelaySigningSecretEncrypted = newRelaySigningSecretEncrypted
		} else if secretRotation != nil && secretRotation.RevokePrevious {
			current.PreviousControlSecretEncrypted = ""
			current.PreviousRelaySigningSecretEncrypted = ""
			current.TransitionSecretsExpireAt = 0
		}
		current.ConfigRevision = baseRevision + 1
		current.UpdatedTime = common.GetTimestamp()
		result := tx.Model(&AetherIntegration{}).
			Where("id = ? AND config_revision = ?", current.Id, baseRevision).
			Updates(map[string]interface{}{
				"route_profile":                           current.RouteProfile,
				"execution_mode":                          current.ExecutionMode,
				"enabled":                                 current.Enabled,
				"capability_version":                      current.CapabilityVersion,
				"control_secret_encrypted":                current.ControlSecretEncrypted,
				"relay_signing_secret_encrypted":          current.RelaySigningSecretEncrypted,
				"previous_control_secret_encrypted":       current.PreviousControlSecretEncrypted,
				"previous_relay_signing_secret_encrypted": current.PreviousRelaySigningSecretEncrypted,
				"transition_secrets_expire_at":            current.TransitionSecretsExpireAt,
				"config_revision":                         current.ConfigRevision,
				"updated_time":                            current.UpdatedTime,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			conflictDetected = true
			return nil
		}
		updated = current
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
		return nil, NewAetherIntegrationConflict(latest, config), nil
	}
	return updated, nil, nil
}

func RevokeAetherIntegrationTransitionSecrets(integration *AetherIntegration, baseRevision int64) (*AetherIntegration, *AetherIntegrationConflict, error) {
	if integration == nil {
		return nil, nil, errors.New("aether integration is required")
	}
	return UpdateAetherIntegrationSharedConfigWithSecretRotation(
		integration,
		baseRevision,
		aetherIntegrationSharedConfig(integration),
		&AetherIntegrationSecretRotation{RevokePrevious: true},
	)
}

func UpdateAetherIntegrationRemoteState(integrationID int, remoteConfigRevision int64, lastSyncTime int64, lastHealthTime int64, lastHealthStatus string) (*AetherIntegration, error) {
	if integrationID <= 0 {
		return nil, errors.New("invalid aether integration ID")
	}
	updates := map[string]interface{}{
		"remote_config_revision": remoteConfigRevision,
		"last_sync_time":         lastSyncTime,
		"last_health_time":       lastHealthTime,
		"last_health_status":     strings.TrimSpace(lastHealthStatus),
	}
	if err := DB.Model(&AetherIntegration{}).Where("id = ?", integrationID).Updates(updates).Error; err != nil {
		return nil, err
	}
	updated := &AetherIntegration{}
	if err := DB.Where("id = ?", integrationID).First(updated).Error; err != nil {
		return nil, err
	}
	return updated, nil
}

func aetherIntegrationSharedConfig(integration *AetherIntegration) AetherIntegrationSharedConfig {
	return AetherIntegrationSharedConfig{
		RouteProfile:      integration.RouteProfile,
		ExecutionMode:     integration.ExecutionMode,
		Enabled:           integration.Enabled,
		CapabilityVersion: integration.CapabilityVersion,
	}
}

func NewAetherIntegrationConflict(current *AetherIntegration, requested AetherIntegrationSharedConfig) *AetherIntegrationConflict {
	currentConfig := aetherIntegrationSharedConfig(current)
	return &AetherIntegrationConflict{
		CurrentRevision: current.ConfigRevision,
		Current:         currentConfig,
		Diff:            aetherIntegrationSharedConfigDiff(requested, currentConfig),
	}
}

func aetherIntegrationSharedConfigDiff(requested AetherIntegrationSharedConfig, current AetherIntegrationSharedConfig) map[string]AetherIntegrationConfigDiff {
	diff := make(map[string]AetherIntegrationConfigDiff)
	if requested.RouteProfile != current.RouteProfile {
		diff["route_profile"] = AetherIntegrationConfigDiff{Requested: requested.RouteProfile, Current: current.RouteProfile}
	}
	if requested.ExecutionMode != current.ExecutionMode {
		diff["execution_mode"] = AetherIntegrationConfigDiff{Requested: requested.ExecutionMode, Current: current.ExecutionMode}
	}
	if requested.Enabled != current.Enabled {
		diff["enabled"] = AetherIntegrationConfigDiff{Requested: requested.Enabled, Current: current.Enabled}
	}
	if requested.CapabilityVersion != current.CapabilityVersion {
		diff["capability_version"] = AetherIntegrationConfigDiff{Requested: requested.CapabilityVersion, Current: current.CapabilityVersion}
	}
	return diff
}
