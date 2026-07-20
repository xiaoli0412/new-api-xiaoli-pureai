package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
)

func TestMidjourneyChannelLookupFailureLeavesTaskRetryable(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Midjourney{}, &model.Token{}))

	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
	})

	user := &model.User{
		Id:       910001,
		Username: "midjourney-cache-retry-user",
		Password: "password",
		Quota:    700,
	}
	require.NoError(t, db.Create(user).Error)

	token := &model.Token{
		Id:          910001,
		UserId:      user.Id,
		Key:         "midjourney-cache-retry-token",
		RemainQuota: 500,
	}
	require.NoError(t, db.Create(token).Error)

	task := &model.Midjourney{
		UserId:    user.Id,
		MjId:      "midjourney-cache-retry-task",
		Status:    "IN_PROGRESS",
		Progress:  "30%",
		ChannelId: 910001,
		Quota:     123,
	}
	require.NoError(t, db.Create(task).Error)

	summary := runMidjourneyTaskUpdateOnce(context.Background(), nil)
	require.Equal(t, 1, summary.UnfinishedTasks)
	require.Equal(t, 1, summary.ChannelsScanned)

	var persistedTask model.Midjourney
	require.NoError(t, db.First(&persistedTask, task.Id).Error)
	require.Equal(t, "IN_PROGRESS", persistedTask.Status)
	require.Equal(t, "30%", persistedTask.Progress)
	require.Equal(t, 123, persistedTask.Quota)
	require.Empty(t, persistedTask.FailReason)

	var persistedUser model.User
	require.NoError(t, db.First(&persistedUser, user.Id).Error)
	require.Equal(t, 700, persistedUser.Quota)

	var persistedToken model.Token
	require.NoError(t, db.First(&persistedToken, token.Id).Error)
	require.Equal(t, 500, persistedToken.RemainQuota)

	retryableTasks := model.GetAllUnFinishTasks()
	require.Len(t, retryableTasks, 1)
	require.Equal(t, task.Id, retryableTasks[0].Id)
}

func TestMidjourneyTerminalFailureRollsBackWhenFinancialOutboxFails(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.Midjourney{},
		&model.AetherIntegration{},
		&model.AetherLedgerEvent{},
		&model.BillingRefundClaim{},
		&model.Log{},
	))

	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
	})
	service.InitHttpClient()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		payload, err := common.Marshal([]dto.MidjourneyDto{{
			MjId:       "midjourney-terminal-outbox-failure",
			Status:     "FAILURE",
			Progress:   "100%",
			FailReason: "upstream failure",
		}})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)

	const userID = 910002
	const channelID = 910002
	const quota = 123
	require.NoError(t, db.Create(&model.User{
		Id:       userID,
		Username: "midjourney-terminal-outbox-user",
		Password: "password",
		Quota:    700,
	}).Error)
	baseURL := server.URL
	require.NoError(t, db.Create(&model.Channel{
		Id:      channelID,
		Name:    "midjourney-terminal-outbox-channel",
		Key:     "midjourney-terminal-outbox-key",
		BaseURL: &baseURL,
		Status:  common.ChannelStatusEnabled,
	}).Error)
	require.NoError(t, db.Create(&model.AetherIntegration{
		ChannelID:                   channelID,
		InstanceID:                  "aether-midjourney-terminal-outbox-failure",
		ExecutionMode:               model.AetherExecutionModeDirectChannel,
		Enabled:                     true,
		ConfigRevision:              1,
		ControlSecretEncrypted:      "invalid-secret",
		RelaySigningSecretEncrypted: "invalid-secret",
	}).Error)
	task := &model.Midjourney{
		UserId:     userID,
		MjId:       "midjourney-terminal-outbox-failure",
		Status:     "IN_PROGRESS",
		Progress:   "50%",
		ChannelId:  channelID,
		Quota:      quota,
		SubmitTime: time.Now().UnixMilli(),
	}
	require.NoError(t, db.Create(task).Error)

	runMidjourneyTaskUpdateOnce(context.Background(), nil)

	var persistedTask model.Midjourney
	require.NoError(t, db.First(&persistedTask, task.Id).Error)
	require.Equal(t, "IN_PROGRESS", persistedTask.Status)
	require.Equal(t, "50%", persistedTask.Progress)
	require.Empty(t, persistedTask.FailReason)

	var persistedUser model.User
	require.NoError(t, db.First(&persistedUser, userID).Error)
	require.Equal(t, 700, persistedUser.Quota)

	var claimCount int64
	require.NoError(t, db.Model(&model.BillingRefundClaim{}).Count(&claimCount).Error)
	require.Zero(t, claimCount)
	var eventCount int64
	require.NoError(t, db.Model(&model.AetherLedgerEvent{}).Count(&eventCount).Error)
	require.Zero(t, eventCount)
}
