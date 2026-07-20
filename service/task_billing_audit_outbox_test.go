package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogTaskConsumptionDoesNotPromoteFailedSettlementToUsageEvent(t *testing.T) {
	previousLogDB := model.LOG_DB
	previousLogConsumeEnabled := common.LogConsumeEnabled
	previousDataExportEnabled := common.DataExportEnabled
	t.Cleanup(func() {
		model.LOG_DB = previousLogDB
		common.LogConsumeEnabled = previousLogConsumeEnabled
		common.DataExportEnabled = previousDataExportEnabled
	})

	db := openBillingRequestStateDB(t, "task_consumption_failed_settlement_audit")
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	integration := &model.AetherIntegration{
		ChannelID:                   6114,
		InstanceID:                  "task-consumption-failed-settlement-audit",
		ExecutionMode:               model.AetherExecutionModeDirectChannel,
		Enabled:                     true,
		ConfigRevision:              1,
		ControlSecretEncrypted:      "invalid-secret",
		RelaySigningSecretEncrypted: "invalid-secret",
	}
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&model.User{Id: 6114, Username: "task-consumption-failed-settlement-audit", Quota: 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6114, UserId: 6114, Key: "task-consumption-failed-settlement-audit-token", RemainQuota: 1000}).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 6114, Key: "task-consumption-failed-settlement-audit-channel", Name: "task-consumption-failed-settlement-audit"}).Error)

	requestID := "req_task_consumption_failed_settlement_audit"
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	context.Set(common.RequestIdKey, requestID)
	info := newWalletBillingRelayInfo(6114, 6114, "task-consumption-failed-settlement-audit-token", requestID)
	info.OriginModelName = "gpt-5"
	info.UsingGroup = "default"
	info.PriceData.Quota = 60
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 6114}
	info.TaskRelayInfo = &relaycommon.TaskRelayInfo{Action: "generate"}
	session, apiErr := NewBillingSession(context, info, 100)
	require.Nil(t, apiErr)
	require.Error(t, session.Settle(60))

	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Save(integration).Error)
	model.LOG_DB = nil
	common.LogConsumeEnabled = true
	common.DataExportEnabled = false
	LogTaskConsumption(context, info)

	var user model.User
	var token model.Token
	var channel model.Channel
	var state model.BillingRequestState
	var eventCount int64
	require.NoError(t, db.First(&user, 6114).Error)
	require.NoError(t, db.First(&token, 6114).Error)
	require.NoError(t, db.First(&channel, 6114).Error)
	require.NoError(t, db.Where("user_id = ? AND token_id = ?", 6114, 6114).First(&state).Error)
	require.NoError(t, db.Model(&model.AetherLedgerEvent{}).Where("event_type = ?", model.AetherLedgerEventUsageSettled).Count(&eventCount).Error)
	assert.Equal(t, 900, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.Equal(t, 900, token.RemainQuota)
	assert.Zero(t, channel.UsedQuota)
	assert.Equal(t, model.BillingRequestStatePreconsumed, state.State)
	assert.Zero(t, eventCount)
}

func TestLogTaskConsumptionKeepsSuccessfulSettlementEventWhenAuditDatabaseIsUnavailable(t *testing.T) {
	previousLogDB := model.LOG_DB
	previousLogConsumeEnabled := common.LogConsumeEnabled
	t.Cleanup(func() {
		model.LOG_DB = previousLogDB
		common.LogConsumeEnabled = previousLogConsumeEnabled
	})

	db := openBillingRequestStateDB(t, "task_consumption_successful_settlement_audit")
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	integration := &model.AetherIntegration{
		ChannelID:      6115,
		InstanceID:     "task-consumption-successful-settlement-audit",
		ExecutionMode:  model.AetherExecutionModeDirectChannel,
		Enabled:        true,
		ConfigRevision: 1,
	}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&model.User{Id: 6115, Username: "task-consumption-successful-settlement-audit", Quota: 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6115, UserId: 6115, Key: "task-consumption-successful-settlement-audit-token", RemainQuota: 1000}).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 6115, Key: "task-consumption-successful-settlement-audit-channel", Name: "task-consumption-successful-settlement-audit"}).Error)

	requestID := "req_task_consumption_successful_settlement_audit"
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	context.Set(common.RequestIdKey, requestID)
	info := newWalletBillingRelayInfo(6115, 6115, "task-consumption-successful-settlement-audit-token", requestID)
	info.OriginModelName = "gpt-5"
	info.UsingGroup = "default"
	info.PriceData.Quota = 60
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 6115}
	info.TaskRelayInfo = &relaycommon.TaskRelayInfo{Action: "generate"}
	session, apiErr := NewBillingSession(context, info, 100)
	require.Nil(t, apiErr)
	require.NoError(t, session.Settle(60))

	model.LOG_DB = nil
	common.LogConsumeEnabled = true
	LogTaskConsumption(context, info)

	var state model.BillingRequestState
	var eventCount int64
	require.NoError(t, db.Where("user_id = ? AND token_id = ?", 6115, 6115).First(&state).Error)
	require.NoError(t, db.Model(&model.AetherLedgerEvent{}).Where("event_type = ?", model.AetherLedgerEventUsageSettled).Count(&eventCount).Error)
	assert.Equal(t, model.BillingRequestStateSettled, state.State)
	assert.Equal(t, int64(1), eventCount)
}
