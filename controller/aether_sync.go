package controller

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

const maxAetherEventPageSize = 1000
const aetherEventsContractVersion = "aether-newapi-events/v1"
const aetherSnapshotContractVersion = "aether-newapi-snapshot/v1"
const maxAetherSnapshotRangeSeconds int64 = 31 * 24 * 60 * 60

type aetherEventView struct {
	Id           string         `json:"id"`
	InstanceID   string         `json:"instance_id"`
	DedupeKey    string         `json:"dedupe_key"`
	EventType    string         `json:"event_type"`
	OccurredAt   int64          `json:"occurred_at"`
	CreatedAt    int64          `json:"created_at"`
	QuotaPerUnit string         `json:"quota_per_unit"`
	Payload      map[string]any `json:"payload"`
}

type aetherChannelSnapshot struct {
	Id                 string `json:"id"`
	Type               int    `json:"type"`
	Name               string `json:"name"`
	Status             int    `json:"status"`
	Models             string `json:"models"`
	Group              string `json:"group"`
	Balance            string `json:"balance"`
	BalanceUpdatedTime int64  `json:"balance_updated_time"`
	UsedQuota          string `json:"used_quota"`
}

type aetherChannelSnapshotRecord struct {
	Id                 int
	Type               int
	Name               string
	Status             int
	Models             string
	Group              string `gorm:"column:group_name"`
	Balance            float64
	BalanceUpdatedTime int64
	UsedQuota          int64
}

type aetherSnapshotUsage struct {
	Requests         int64  `json:"requests"`
	ChargedQuota     string `json:"charged_quota"`
	PromptTokens     string `json:"prompt_tokens"`
	CompletionTokens string `json:"completion_tokens"`
}

type aetherSnapshotFinancial struct {
	Events      int64  `json:"events"`
	QuotaDelta  string `json:"quota_delta"`
	MoneyAmount string `json:"money_amount"`
}

type aetherSnapshotSubscriptions struct {
	Changes   int64 `json:"changes"`
	Activated int64 `json:"activated"`
	Cancelled int64 `json:"cancelled"`
	Deleted   int64 `json:"deleted"`
	Expired   int64 `json:"expired"`
}

func GetAetherEvents(c *gin.Context) {
	instanceID := c.GetString("aether_instance_id")
	if instanceID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "aether service authentication is required"})
		return
	}
	after, err := parseAetherEventCursor(c.Query("after"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid event cursor"})
		return
	}
	limit, err := parseAetherEventLimit(c.Query("limit"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid event limit"})
		return
	}

	events := make([]model.AetherLedgerEvent, 0, limit+1)
	if err := model.DB.Where("instance_id = ? AND id > ?", instanceID, after).
		Order("id asc").Limit(limit + 1).Find(&events).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to load aether events"})
		return
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	eventViews, err := buildAetherEventViews(events)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to decode aether event"})
		return
	}
	nextCursor := after
	if len(events) > 0 {
		nextCursor = events[len(events)-1].Id
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{
		"contract_version": aetherEventsContractVersion,
		"events":           eventViews,
		"next_cursor":      strconv.FormatInt(nextCursor, 10),
		"has_more":         hasMore,
	}})
}

func GetAetherSnapshot(c *gin.Context) {
	instanceID := c.GetString("aether_instance_id")
	if instanceID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "aether service authentication is required"})
		return
	}
	from, to, err := parseAetherSnapshotRange(c.Query("from"), c.Query("to"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid snapshot time range"})
		return
	}
	cursor, err := parseAetherEventCursor(c.Query("cursor"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid snapshot cursor"})
		return
	}
	limit, err := parseAetherEventLimit(c.Query("limit"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid snapshot limit"})
		return
	}
	events := make([]model.AetherLedgerEvent, 0, limit+1)
	if err := model.DB.Where("instance_id = ? AND occurred_at >= ? AND occurred_at <= ? AND id > ?", instanceID, from, to, cursor).
		Order("id asc").Limit(limit + 1).Find(&events).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to load aether snapshot"})
		return
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	eventViews, err := buildAetherEventViews(events)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to decode aether snapshot event"})
		return
	}
	nextCursor := cursor
	if len(events) > 0 {
		nextCursor = events[len(events)-1].Id
	}
	usage := aetherSnapshotUsage{}
	financial := aetherSnapshotFinancial{}
	subscriptions := aetherSnapshotSubscriptions{}
	chargedQuota := decimal.Zero
	promptTokens := decimal.Zero
	completionTokens := decimal.Zero
	quotaDelta := decimal.Zero
	moneyAmount := decimal.Zero
	historicalAnalysisState := "unknown"
	hasHistoricalEvents := false
	hasHistoricalPricingGap := false
	windowDigest := sha256.New()
	windowRows, err := model.DB.Model(&model.AetherLedgerEvent{}).
		Where("instance_id = ? AND occurred_at >= ? AND occurred_at <= ?", instanceID, from, to).
		Order("id asc").Rows()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to load aether snapshot summary"})
		return
	}
	defer windowRows.Close()
	for windowRows.Next() {
		var event model.AetherLedgerEvent
		if err := model.DB.ScanRows(windowRows, &event); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to scan aether snapshot summary"})
			return
		}
		hasHistoricalEvents = true
		if event.QuotaPerUnitSnapshot <= 0 || math.IsNaN(event.QuotaPerUnitSnapshot) || math.IsInf(event.QuotaPerUnitSnapshot, 0) {
			hasHistoricalPricingGap = true
		}
		digestPayload, err := common.Marshal(event)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to hash aether snapshot event"})
			return
		}
		_, _ = windowDigest.Write(digestPayload)
		_, _ = windowDigest.Write([]byte{0})
		switch event.EventType {
		case model.AetherLedgerEventUsageSettled:
			var payload struct {
				ChargedQuota     string `json:"charged_quota"`
				PromptTokens     string `json:"prompt_tokens"`
				CompletionTokens string `json:"completion_tokens"`
			}
			if err := common.Unmarshal([]byte(event.Payload), &payload); err != nil {
				continue
			}
			if value, err := decimal.NewFromString(payload.ChargedQuota); err == nil && !value.IsNegative() {
				chargedQuota = chargedQuota.Add(value)
			}
			if value, err := decimal.NewFromString(payload.PromptTokens); err == nil && !value.IsNegative() {
				promptTokens = promptTokens.Add(value)
			}
			if value, err := decimal.NewFromString(payload.CompletionTokens); err == nil && !value.IsNegative() {
				completionTokens = completionTokens.Add(value)
			}
			usage.Requests++
		case model.AetherLedgerEventFinancial:
			var payload struct {
				QuotaDelta  string `json:"quota_delta"`
				MoneyAmount string `json:"money_amount"`
			}
			if err := common.Unmarshal([]byte(event.Payload), &payload); err != nil {
				continue
			}
			if value, err := decimal.NewFromString(payload.QuotaDelta); err == nil {
				quotaDelta = quotaDelta.Add(value)
			}
			if value, err := decimal.NewFromString(payload.MoneyAmount); err == nil {
				moneyAmount = moneyAmount.Add(value)
			}
			financial.Events++
		case model.AetherLedgerEventSubscriptionChanged:
			var payload struct {
				Action string `json:"action"`
			}
			if err := common.Unmarshal([]byte(event.Payload), &payload); err != nil {
				continue
			}
			subscriptions.Changes++
			switch payload.Action {
			case "activated":
				subscriptions.Activated++
			case "cancelled":
				subscriptions.Cancelled++
			case "deleted":
				subscriptions.Deleted++
			case "expired":
				subscriptions.Expired++
			}
		}
	}
	if err := windowRows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to scan aether snapshot summary"})
		return
	}
	if hasHistoricalEvents && !hasHistoricalPricingGap {
		historicalAnalysisState = "estimated"
	}
	usage.ChargedQuota = chargedQuota.String()
	usage.PromptTokens = promptTokens.String()
	usage.CompletionTokens = completionTokens.String()
	financial.QuotaDelta = quotaDelta.String()
	financial.MoneyAmount = moneyAmount.String()

	channelRecords := make([]aetherChannelSnapshotRecord, 0)
	if err := model.DB.Model(&model.Channel{}).
		Select("id, type, name, status, models, " + model.AetherSnapshotGroupColumn() + " as group_name, balance, balance_updated_time, used_quota").
		Order("id asc").
		Find(&channelRecords).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to load channel snapshot"})
		return
	}
	channels := make([]aetherChannelSnapshot, 0, len(channelRecords))
	for index := range channelRecords {
		record := &channelRecords[index]
		channels = append(channels, aetherChannelSnapshot{
			Id:                 strconv.Itoa(record.Id),
			Type:               record.Type,
			Name:               record.Name,
			Status:             record.Status,
			Models:             record.Models,
			Group:              record.Group,
			Balance:            strconv.FormatFloat(record.Balance, 'f', -1, 64),
			BalanceUpdatedTime: record.BalanceUpdatedTime,
			UsedQuota:          strconv.FormatInt(record.UsedQuota, 10),
		})
	}
	c.Header("Cache-Control", "no-store")
	data := gin.H{
		"contract_version":          aetherSnapshotContractVersion,
		"snapshot_revision":         int64(0),
		"from":                      from,
		"to":                        to,
		"usage":                     usage,
		"financial":                 financial,
		"subscriptions":             subscriptions,
		"events":                    eventViews,
		"next_cursor":               strconv.FormatInt(nextCursor, 10),
		"has_more":                  hasMore,
		"channels":                  channels,
		"quota_per_unit":            strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64),
		"historical_analysis_state": historicalAnalysisState,
		"pricing_contract_path":     "/api/aether/v1/pricing",
	}
	revisionInput := struct {
		Data         gin.H  `json:"data"`
		WindowDigest string `json:"window_digest"`
	}{
		Data:         data,
		WindowDigest: hex.EncodeToString(windowDigest.Sum(nil)),
	}
	revisionPayload, err := common.Marshal(revisionInput)
	if err != nil {
		common.SysError("failed to build aether snapshot revision: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to build aether snapshot"})
		return
	}
	revisionDigest := common.Sha256Raw(revisionPayload)
	data["snapshot_revision"] = int64(binary.BigEndian.Uint64(revisionDigest[:8]) & ((uint64(1) << 63) - 1))
	etagPayload, err := common.Marshal(revisionInput)
	if err != nil {
		common.SysError("failed to build aether snapshot ETag: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to build aether snapshot"})
		return
	}
	etag := `"` + hex.EncodeToString(common.Sha256Raw(etagPayload)) + `"`
	c.Header("ETag", etag)
	if aetherETagMatches(c.GetHeader("If-None-Match"), etag) {
		c.Status(http.StatusNotModified)
		c.Writer.WriteHeaderNow()
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func buildAetherEventViews(events []model.AetherLedgerEvent) ([]aetherEventView, error) {
	eventViews := make([]aetherEventView, 0, len(events))
	for index := range events {
		event := &events[index]
		payload := make(map[string]any)
		if err := common.Unmarshal([]byte(event.Payload), &payload); err != nil {
			return nil, fmt.Errorf("decode aether event %d: %w", event.Id, err)
		}
		eventViews = append(eventViews, aetherEventView{
			Id:           strconv.FormatInt(event.Id, 10),
			InstanceID:   event.InstanceID,
			DedupeKey:    event.DedupeKey,
			EventType:    event.EventType,
			OccurredAt:   event.OccurredAt,
			CreatedAt:    event.CreatedTime,
			QuotaPerUnit: strconv.FormatFloat(event.QuotaPerUnitSnapshot, 'f', -1, 64),
			Payload:      payload,
		})
	}
	return eventViews, nil
}

func parseAetherEventCursor(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, strconv.ErrSyntax
	}
	return value, nil
}

func parseAetherEventLimit(raw string) (int, error) {
	if raw == "" {
		return 100, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > maxAetherEventPageSize {
		return 0, strconv.ErrSyntax
	}
	return value, nil
}

func parseAetherSnapshotRange(rawFrom string, rawTo string) (int64, int64, error) {
	now := time.Now().Unix()
	from := now - 24*60*60
	to := now
	var err error
	if rawFrom != "" {
		from, err = strconv.ParseInt(rawFrom, 10, 64)
		if err != nil {
			return 0, 0, err
		}
	}
	if rawTo != "" {
		to, err = strconv.ParseInt(rawTo, 10, 64)
		if err != nil {
			return 0, 0, err
		}
	}
	if from < 0 || to < from || to-from > maxAetherSnapshotRangeSeconds || to-now > 366*24*60*60 {
		return 0, 0, strconv.ErrSyntax
	}
	return from, to, nil
}
