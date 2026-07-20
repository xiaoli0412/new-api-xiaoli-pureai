package model

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	AetherLedgerEventUsageSettled           = "usage_settled"
	AetherLedgerEventFinancial              = "financial_posted"
	AetherLedgerEventSubscriptionChanged    = "subscription_changed"
	AetherLedgerEventChannel                = "channel_changed"
	AetherLedgerEventChannelBalanceObserved = "channel_balance_observed"
	AetherLedgerEventPricingChanged         = "pricing_changed"
)

type AetherLedgerEvent struct {
	Id                   int64   `json:"id"`
	InstanceID           string  `json:"instance_id" gorm:"index:idx_aether_event_instance_id_id,priority:1;type:varchar(128)"`
	EventType            string  `json:"event_type" gorm:"type:varchar(64);index"`
	DedupeKey            string  `json:"dedupe_key" gorm:"uniqueIndex;type:varchar(255)"`
	OccurredAt           int64   `json:"occurred_at" gorm:"bigint;index:idx_aether_event_instance_id_id,priority:2"`
	Payload              string  `json:"payload" gorm:"type:text"`
	QuotaPerUnitSnapshot float64 `json:"quota_per_unit_snapshot"`
	CreatedTime          int64   `json:"created_time" gorm:"bigint"`
}

type aetherUsageLedgerPayload struct {
	SubjectID         string `json:"subject_id"`
	TokenSubjectID    string `json:"token_subject_id"`
	RequestID         string `json:"request_id"`
	UpstreamRequestID string `json:"upstream_request_id,omitempty"`
	ChannelID         string `json:"channel_id"`
	Model             string `json:"model"`
	Group             string `json:"group"`
	ChargedQuota      string `json:"charged_quota"`
	PromptTokens      string `json:"prompt_tokens"`
	CompletionTokens  string `json:"completion_tokens"`
	QuotaPerUnit      string `json:"quota_per_unit"`
	AnalysisStatus    string `json:"analysis_status"`
}

type AetherFinancialEventInput struct {
	UserID     int
	SourceType string
	SourceID   string
	// DedupeKeyID, when present, is HMACed with each integration signing secret
	// before forming the externally visible outbox dedupe key.
	DedupeKeyID     string
	QuotaDelta      int
	MoneyAmount     string
	PaymentCategory string
	OccurredAt      int64
}

type AetherSubscriptionEventInput struct {
	UserID         int
	SubscriptionID int
	PlanID         int
	Status         string
	Action         string
	AmountTotal    int64
	AmountUsed     int64
	StartTime      int64
	EndTime        int64
	OccurredAt     int64
}

type aetherFinancialLedgerPayload struct {
	SubjectID       string `json:"subject_id"`
	SourceType      string `json:"source_type"`
	QuotaDelta      string `json:"quota_delta"`
	MoneyAmount     string `json:"money_amount,omitempty"`
	PaymentCategory string `json:"payment_category,omitempty"`
	QuotaPerUnit    string `json:"quota_per_unit"`
	AnalysisStatus  string `json:"analysis_status"`
}

type aetherSubscriptionLedgerPayload struct {
	SubjectID      string `json:"subject_id"`
	SubscriptionID string `json:"subscription_id"`
	PlanID         string `json:"plan_id"`
	Status         string `json:"status"`
	Action         string `json:"action"`
	AmountTotal    string `json:"amount_total"`
	AmountUsed     string `json:"amount_used"`
	StartTime      int64  `json:"start_time"`
	EndTime        int64  `json:"end_time"`
	QuotaPerUnit   string `json:"quota_per_unit"`
	AnalysisStatus string `json:"analysis_status"`
}

type aetherChannelBalanceLedgerPayload struct {
	ChannelID      string `json:"channel_id"`
	Balance        string `json:"balance"`
	ObservedAt     int64  `json:"observed_at"`
	AnalysisStatus string `json:"analysis_status"`
}

type aetherChannelLedgerPayload struct {
	ChannelID      string `json:"channel_id"`
	Action         string `json:"action"`
	ChannelType    int    `json:"channel_type"`
	Status         int    `json:"status"`
	Models         string `json:"models"`
	Group          string `json:"group"`
	Priority       int64  `json:"priority"`
	Weight         int    `json:"weight"`
	AnalysisStatus string `json:"analysis_status"`
}

type aetherPricingLedgerPayload struct {
	Scope          string `json:"scope"`
	AnalysisStatus string `json:"analysis_status"`
}

func insertAetherLedgerEventTx(tx *gorm.DB, event *AetherLedgerEvent) error {
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "dedupe_key"}},
		DoNothing: true,
	}).Create(event).Error
}

func RecordAetherUsageEvent(log *Log) {
	if DB == nil {
		return
	}
	if err := RecordAetherUsageEventTx(DB, log); err != nil {
		common.SysError("failed to write aether usage event: " + err.Error())
	}
}

func RecordAetherUsageEventTx(tx *gorm.DB, log *Log) error {
	if tx == nil {
		return errors.New("aether usage event transaction is required")
	}
	if log == nil || log.Type != LogTypeConsume || strings.TrimSpace(log.RequestId) == "" {
		return errors.New("invalid aether usage event input")
	}
	var integrations []AetherIntegration
	if err := tx.Where("enabled = ?", true).Find(&integrations).Error; err != nil {
		return fmt.Errorf("load active aether integrations: %w", err)
	}
	for index := range integrations {
		integration := &integrations[index]
		_, signingSecret, err := integration.Secrets()
		if err != nil {
			return fmt.Errorf("read aether integration secrets: integration_id=%d: %w", integration.Id, err)
		}
		payload, err := common.Marshal(aetherUsageLedgerPayload{
			SubjectID:         aetherLedgerSubjectID(signingSecret, "user", log.UserId, "u_"),
			TokenSubjectID:    aetherLedgerSubjectID(signingSecret, "token", log.TokenId, "t_"),
			RequestID:         log.RequestId,
			UpstreamRequestID: log.UpstreamRequestId,
			ChannelID:         strconv.Itoa(log.ChannelId),
			Model:             log.ModelName,
			Group:             log.Group,
			ChargedQuota:      strconv.Itoa(log.Quota),
			PromptTokens:      strconv.Itoa(log.PromptTokens),
			CompletionTokens:  strconv.Itoa(log.CompletionTokens),
			QuotaPerUnit:      strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64),
			AnalysisStatus:    "settled",
		})
		if err != nil {
			return fmt.Errorf("marshal aether usage event: %w", err)
		}
		event := &AetherLedgerEvent{
			InstanceID:           integration.InstanceID,
			EventType:            AetherLedgerEventUsageSettled,
			DedupeKey:            "usage:" + integration.InstanceID + ":" + log.RequestId,
			OccurredAt:           log.CreatedAt,
			Payload:              string(payload),
			QuotaPerUnitSnapshot: common.QuotaPerUnit,
			CreatedTime:          common.GetTimestamp(),
		}
		if event.OccurredAt <= 0 {
			event.OccurredAt = event.CreatedTime
		}
		if err := insertAetherLedgerEventTx(tx, event); err != nil {
			return fmt.Errorf("write aether usage event: integration_id=%d: %w", integration.Id, err)
		}
	}
	return nil
}

func RecordAetherFinancialEvent(input AetherFinancialEventInput) {
	if DB == nil {
		return
	}
	if err := RecordAetherFinancialEventTx(DB, input); err != nil {
		common.SysError("failed to write aether financial event: " + err.Error())
	}
}

func RecordAetherFinancialEventTx(tx *gorm.DB, input AetherFinancialEventInput) error {
	if tx == nil {
		return errors.New("aether financial event transaction is required")
	}
	if input.UserID <= 0 || strings.TrimSpace(input.SourceType) == "" || strings.TrimSpace(input.SourceID) == "" {
		return errors.New("invalid aether financial event input")
	}
	var integrations []AetherIntegration
	if err := tx.Where("enabled = ?", true).Find(&integrations).Error; err != nil {
		return fmt.Errorf("load active aether integrations: %w", err)
	}
	for index := range integrations {
		integration := &integrations[index]
		_, signingSecret, err := integration.Secrets()
		if err != nil {
			return fmt.Errorf("read aether integration secrets: integration_id=%d: %w", integration.Id, err)
		}
		dedupeSourceID := input.SourceID
		if input.DedupeKeyID != "" {
			dedupeSourceID = common.GenerateHMACWithKey([]byte(signingSecret), input.SourceType+":"+input.DedupeKeyID)
			if len(dedupeSourceID) > 32 {
				dedupeSourceID = dedupeSourceID[:32]
			}
			dedupeSourceID = "h_" + dedupeSourceID
		}
		payload, err := common.Marshal(aetherFinancialLedgerPayload{
			SubjectID:       aetherLedgerSubjectID(signingSecret, "user", input.UserID, "u_"),
			SourceType:      input.SourceType,
			QuotaDelta:      strconv.Itoa(input.QuotaDelta),
			MoneyAmount:     input.MoneyAmount,
			PaymentCategory: input.PaymentCategory,
			QuotaPerUnit:    strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64),
			AnalysisStatus:  "settled",
		})
		if err != nil {
			return fmt.Errorf("marshal aether financial event: %w", err)
		}
		event := &AetherLedgerEvent{
			InstanceID:           integration.InstanceID,
			EventType:            AetherLedgerEventFinancial,
			DedupeKey:            "financial:" + integration.InstanceID + ":" + input.SourceType + ":" + dedupeSourceID,
			OccurredAt:           input.OccurredAt,
			Payload:              string(payload),
			QuotaPerUnitSnapshot: common.QuotaPerUnit,
			CreatedTime:          common.GetTimestamp(),
		}
		if event.OccurredAt <= 0 {
			event.OccurredAt = event.CreatedTime
		}
		if err := insertAetherLedgerEventTx(tx, event); err != nil {
			return fmt.Errorf("write aether financial event: integration_id=%d: %w", integration.Id, err)
		}
	}
	return nil
}

func RecordAetherSubscriptionEvent(input AetherSubscriptionEventInput) {
	if DB == nil {
		return
	}
	if err := RecordAetherSubscriptionEventTx(DB, input); err != nil {
		common.SysError("failed to write aether subscription event: " + err.Error())
	}
}

func RecordAetherSubscriptionEventTx(tx *gorm.DB, input AetherSubscriptionEventInput) error {
	if tx == nil {
		return errors.New("aether subscription event transaction is required")
	}
	if input.UserID <= 0 || input.SubscriptionID <= 0 || strings.TrimSpace(input.Action) == "" {
		return errors.New("invalid aether subscription event input")
	}
	var integrations []AetherIntegration
	if err := tx.Where("enabled = ?", true).Find(&integrations).Error; err != nil {
		return fmt.Errorf("load active aether integrations: %w", err)
	}
	for index := range integrations {
		integration := &integrations[index]
		_, signingSecret, err := integration.Secrets()
		if err != nil {
			return fmt.Errorf("read aether integration secrets: integration_id=%d: %w", integration.Id, err)
		}
		payload, err := common.Marshal(aetherSubscriptionLedgerPayload{
			SubjectID:      aetherLedgerSubjectID(signingSecret, "user", input.UserID, "u_"),
			SubscriptionID: strconv.Itoa(input.SubscriptionID),
			PlanID:         strconv.Itoa(input.PlanID),
			Status:         strings.TrimSpace(input.Status),
			Action:         strings.TrimSpace(input.Action),
			AmountTotal:    strconv.FormatInt(input.AmountTotal, 10),
			AmountUsed:     strconv.FormatInt(input.AmountUsed, 10),
			StartTime:      input.StartTime,
			EndTime:        input.EndTime,
			QuotaPerUnit:   strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64),
			AnalysisStatus: "settled",
		})
		if err != nil {
			return fmt.Errorf("marshal aether subscription event: %w", err)
		}
		event := &AetherLedgerEvent{
			InstanceID:           integration.InstanceID,
			EventType:            AetherLedgerEventSubscriptionChanged,
			DedupeKey:            "subscription:" + integration.InstanceID + ":" + input.Action + ":" + strconv.Itoa(input.SubscriptionID) + ":" + strconv.FormatInt(input.OccurredAt, 10),
			OccurredAt:           input.OccurredAt,
			Payload:              string(payload),
			QuotaPerUnitSnapshot: common.QuotaPerUnit,
			CreatedTime:          common.GetTimestamp(),
		}
		if event.OccurredAt <= 0 {
			event.OccurredAt = event.CreatedTime
			event.DedupeKey = "subscription:" + integration.InstanceID + ":" + input.Action + ":" + strconv.Itoa(input.SubscriptionID) + ":" + strconv.FormatInt(event.OccurredAt, 10)
		}
		if err := insertAetherLedgerEventTx(tx, event); err != nil {
			return fmt.Errorf("write aether subscription event: integration_id=%d: %w", integration.Id, err)
		}
	}
	return nil
}

func RecordAetherChannelBalanceObservation(channelID int, balance float64, observedAt int64) {
	if DB == nil {
		return
	}
	if err := RecordAetherChannelBalanceObservationTx(DB, channelID, balance, observedAt); err != nil {
		common.SysError("failed to write aether channel balance event: " + err.Error())
	}
}

func RecordAetherChannelBalanceObservationTx(tx *gorm.DB, channelID int, balance float64, observedAt int64) error {
	return RecordAetherChannelBalanceObservationTxWithMutationID(tx, channelID, balance, observedAt, common.NewRequestId())
}

func RecordAetherChannelBalanceObservationTxWithMutationID(tx *gorm.DB, channelID int, balance float64, observedAt int64, mutationID string) error {
	if tx == nil {
		return errors.New("aether channel balance event transaction is required")
	}
	if channelID <= 0 || strings.TrimSpace(mutationID) == "" {
		return errors.New("invalid aether channel balance event input")
	}
	mutationID = strings.TrimSpace(mutationID)
	if observedAt <= 0 {
		observedAt = common.GetTimestamp()
	}
	balanceText := strconv.FormatFloat(balance, 'f', -1, 64)
	var integrations []AetherIntegration
	if err := tx.Where("enabled = ?", true).Find(&integrations).Error; err != nil {
		return fmt.Errorf("load active aether integrations: %w", err)
	}
	for index := range integrations {
		integration := &integrations[index]
		mutationDigest := common.GenerateHMACWithKey([]byte(integration.InstanceID), mutationID)
		if len(mutationDigest) > 32 {
			mutationDigest = mutationDigest[:32]
		}
		payload, err := common.Marshal(aetherChannelBalanceLedgerPayload{
			ChannelID:      strconv.Itoa(channelID),
			Balance:        balanceText,
			ObservedAt:     observedAt,
			AnalysisStatus: "observed",
		})
		if err != nil {
			return fmt.Errorf("marshal aether channel balance event: %w", err)
		}
		event := &AetherLedgerEvent{
			InstanceID:           integration.InstanceID,
			EventType:            AetherLedgerEventChannelBalanceObserved,
			DedupeKey:            "channel-balance:" + integration.InstanceID + ":" + mutationDigest + ":" + strconv.Itoa(channelID),
			OccurredAt:           observedAt,
			Payload:              string(payload),
			QuotaPerUnitSnapshot: common.QuotaPerUnit,
			CreatedTime:          common.GetTimestamp(),
		}
		if err := insertAetherLedgerEventTx(tx, event); err != nil {
			return fmt.Errorf("write aether channel balance event: integration_id=%d: %w", integration.Id, err)
		}
	}
	return nil
}

func RecordAetherChannelEvent(channel *Channel, action string, occurredAt int64) {
	if DB == nil {
		return
	}
	if err := RecordAetherChannelEventTx(DB, channel, action, occurredAt); err != nil {
		common.SysError("failed to write aether channel event: " + err.Error())
	}
}

func RecordAetherChannelEventTx(tx *gorm.DB, channel *Channel, action string, occurredAt int64) error {
	return RecordAetherChannelEventTxWithMutationID(tx, channel, action, occurredAt, common.NewRequestId())
}

func RecordAetherChannelEventTxWithMutationID(tx *gorm.DB, channel *Channel, action string, occurredAt int64, mutationID string) error {
	if tx == nil {
		return errors.New("aether channel event transaction is required")
	}
	if channel == nil || channel.Id <= 0 || strings.TrimSpace(action) == "" || strings.TrimSpace(mutationID) == "" {
		return errors.New("invalid aether channel event input")
	}
	action = strings.TrimSpace(action)
	mutationID = strings.TrimSpace(mutationID)
	if occurredAt <= 0 {
		occurredAt = common.GetTimestamp()
	}
	payload, err := common.Marshal(aetherChannelLedgerPayload{
		ChannelID:      strconv.Itoa(channel.Id),
		Action:         action,
		ChannelType:    channel.Type,
		Status:         channel.Status,
		Models:         channel.Models,
		Group:          channel.Group,
		Priority:       channel.GetPriority(),
		Weight:         channel.GetWeight(),
		AnalysisStatus: "observed",
	})
	if err != nil {
		return fmt.Errorf("marshal aether channel event: %w", err)
	}
	var integrations []AetherIntegration
	if err := tx.Where("enabled = ?", true).Find(&integrations).Error; err != nil {
		return fmt.Errorf("load active aether integrations: %w", err)
	}
	for index := range integrations {
		integration := &integrations[index]
		mutationDigest := common.GenerateHMACWithKey([]byte(integration.InstanceID), mutationID)
		if len(mutationDigest) > 32 {
			mutationDigest = mutationDigest[:32]
		}
		event := &AetherLedgerEvent{
			InstanceID:           integration.InstanceID,
			EventType:            AetherLedgerEventChannel,
			DedupeKey:            "channel:" + integration.InstanceID + ":" + mutationDigest + ":" + action + ":" + strconv.Itoa(channel.Id),
			OccurredAt:           occurredAt,
			Payload:              string(payload),
			QuotaPerUnitSnapshot: common.QuotaPerUnit,
			CreatedTime:          common.GetTimestamp(),
		}
		if err := insertAetherLedgerEventTx(tx, event); err != nil {
			return fmt.Errorf("write aether channel event: integration_id=%d: %w", integration.Id, err)
		}
	}
	return nil
}

func RecordAetherPricingEvent(scope string, occurredAt int64) {
	if DB == nil {
		return
	}
	if err := RecordAetherPricingEventTx(DB, scope, occurredAt); err != nil {
		common.SysError("failed to write aether pricing event: " + err.Error())
	}
}

func RecordAetherPricingEventTx(tx *gorm.DB, scope string, occurredAt int64) error {
	return RecordAetherPricingEventTxWithMutationID(tx, scope, occurredAt, common.NewRequestId())
}

func RecordAetherPricingEventTxWithMutationID(tx *gorm.DB, scope string, occurredAt int64, mutationID string) error {
	if tx == nil {
		return errors.New("aether pricing event transaction is required")
	}
	if strings.TrimSpace(scope) == "" || strings.TrimSpace(mutationID) == "" {
		return errors.New("invalid aether pricing event input")
	}
	scope = strings.TrimSpace(scope)
	mutationID = strings.TrimSpace(mutationID)
	if occurredAt <= 0 {
		occurredAt = common.GetTimestamp()
	}
	payload, err := common.Marshal(aetherPricingLedgerPayload{
		Scope:          scope,
		AnalysisStatus: "refresh_required",
	})
	if err != nil {
		return fmt.Errorf("marshal aether pricing event: %w", err)
	}
	var integrations []AetherIntegration
	if err := tx.Where("enabled = ?", true).Find(&integrations).Error; err != nil {
		return fmt.Errorf("load active aether integrations: %w", err)
	}
	for index := range integrations {
		integration := &integrations[index]
		mutationDigest := common.GenerateHMACWithKey([]byte(integration.InstanceID), mutationID)
		if len(mutationDigest) > 32 {
			mutationDigest = mutationDigest[:32]
		}
		event := &AetherLedgerEvent{
			InstanceID:           integration.InstanceID,
			EventType:            AetherLedgerEventPricingChanged,
			DedupeKey:            "pricing:" + integration.InstanceID + ":" + mutationDigest + ":" + scope,
			OccurredAt:           occurredAt,
			Payload:              string(payload),
			QuotaPerUnitSnapshot: common.QuotaPerUnit,
			CreatedTime:          common.GetTimestamp(),
		}
		if err := insertAetherLedgerEventTx(tx, event); err != nil {
			return fmt.Errorf("write aether pricing event: integration_id=%d: %w", integration.Id, err)
		}
	}
	return nil
}

func shouldRecordAetherPricingOption(key string) bool {
	if strings.HasPrefix(key, "billing_setting.") {
		return true
	}
	switch key {
	case "QuotaPerUnit", "ModelRatio", "GroupRatio", "GroupGroupRatio", "CompletionRatio", "ModelPrice", "CacheRatio", "CreateCacheRatio", "ImageRatio", "AudioRatio", "AudioCompletionRatio", "TopUpGroupRatio", "Price", "USDExchangeRate":
		return true
	default:
		return false
	}
}

func aetherLedgerSubjectID(secret string, kind string, id int, prefix string) string {
	value := common.GenerateHMACWithKey([]byte(secret), kind+":"+strconv.Itoa(id))
	if len(value) > 32 {
		value = value[:32]
	}
	return prefix + value
}

func AetherSnapshotGroupColumn() string {
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		return `"group"`
	}
	return "`group`"
}
