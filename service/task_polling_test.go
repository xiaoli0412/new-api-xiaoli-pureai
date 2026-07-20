package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type taskPollingFetchAdaptor struct {
	mu           sync.Mutex
	taskIDs      []string
	fetched      chan string
	blockTaskID  string
	blockStarted chan struct{}
	releaseBlock chan struct{}
	blockOnce    sync.Once
}

type sunoFailurePollingAdaptor struct {
	failReason string
}

func (a *sunoFailurePollingAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (a *sunoFailurePollingAdaptor) FetchTask(_ string, _ string, body map[string]any, _ string) (*http.Response, error) {
	taskIDs, _ := body["ids"].([]string)
	items := make([]dto.SunoDataResponse, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		items = append(items, dto.SunoDataResponse{
			TaskID:     taskID,
			Status:     string(model.TaskStatusFailure),
			FailReason: a.failReason,
			FinishTime: time.Now().Unix(),
		})
	}

	responseBody, err := common.Marshal(dto.TaskResponse[[]dto.SunoDataResponse]{
		Code: dto.TaskSuccessCode,
		Data: items,
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
	}, nil
}

func (a *sunoFailurePollingAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return nil, nil
}

func (a *sunoFailurePollingAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}

func (a *taskPollingFetchAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (a *taskPollingFetchAdaptor) FetchTask(_ string, _ string, body map[string]any, _ string) (*http.Response, error) {
	taskID, _ := body["task_id"].(string)
	if taskID == a.blockTaskID && a.releaseBlock != nil {
		a.blockOnce.Do(func() {
			if a.blockStarted != nil {
				close(a.blockStarted)
			}
		})
		<-a.releaseBlock
	}

	a.mu.Lock()
	a.taskIDs = append(a.taskIDs, taskID)
	a.mu.Unlock()
	if a.fetched != nil {
		select {
		case a.fetched <- taskID:
		default:
		}
	}

	response := dto.TaskResponse[model.Task]{
		Code: dto.TaskSuccessCode,
		Data: model.Task{
			TaskID:   taskID,
			Status:   model.TaskStatusInProgress,
			Progress: "30%",
		},
	}
	responseBody, err := common.Marshal(response)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
	}, nil
}

func (a *taskPollingFetchAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return &relaycommon.TaskInfo{Status: model.TaskStatusInProgress}, nil
}

func (a *taskPollingFetchAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}

func (a *taskPollingFetchAdaptor) fetchCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.taskIDs)
}

func (a *taskPollingFetchAdaptor) fetchedTaskIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.taskIDs...)
}

func seedTaskPollingChannel(t *testing.T, id int, disableSleep bool) {
	t.Helper()
	ch := &model.Channel{
		Id:     id,
		Type:   constant.ChannelTypeKling,
		Name:   "polling_channel",
		Key:    "sk-test",
		Status: common.ChannelStatusEnabled,
	}
	if disableSleep {
		ch.SetOtherSettings(dto.ChannelOtherSettings{DisableTaskPollingSleep: true})
	}
	require.NoError(t, model.DB.Create(ch).Error)
}

func seedPollingTask(t *testing.T, channelID int, publicID string, upstreamID string) *model.Task {
	t.Helper()
	task := &model.Task{
		TaskID:    publicID,
		Platform:  constant.TaskPlatform("kling"),
		UserId:    1,
		ChannelId: channelID,
		Action:    constant.TaskActionGenerate,
		Status:    model.TaskStatusInProgress,
		Progress:  "30%",
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: upstreamID,
		},
	}
	require.NoError(t, model.DB.Create(task).Error)
	return task
}

func TestUpdateVideoTasksDefaultSleepWaitsBetweenTasks(t *testing.T) {
	truncate(t)

	const channelID = 101
	seedTaskPollingChannel(t, channelID, false)
	first := seedPollingTask(t, channelID, "task_public_1", "upstream_1")
	second := seedPollingTask(t, channelID, "task_public_2", "upstream_2")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), map[int][]string{
		channelID: {
			first.GetUpstreamTaskID(),
			second.GetUpstreamTaskID(),
		},
	}, map[string]*model.Task{
		first.GetUpstreamTaskID():  first,
		second.GetUpstreamTaskID(): second,
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 1, adaptor.fetchCount())
}

func TestUpdateVideoTasksCanSkipPollingSleepPerChannel(t *testing.T) {
	truncate(t)

	const channelID = 102
	seedTaskPollingChannel(t, channelID, true)
	first := seedPollingTask(t, channelID, "task_public_3", "upstream_3")
	second := seedPollingTask(t, channelID, "task_public_4", "upstream_4")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), map[int][]string{
		channelID: {
			first.GetUpstreamTaskID(),
			second.GetUpstreamTaskID(),
		},
	}, map[string]*model.Task{
		first.GetUpstreamTaskID():  first,
		second.GetUpstreamTaskID(): second,
	})

	require.NoError(t, err)
	assert.Equal(t, 2, adaptor.fetchCount())
}

func TestUpdateVideoTasksDefaultSleepDoesNotBlockOtherChannels(t *testing.T) {
	truncate(t)

	const firstChannelID = 201
	const secondChannelID = 202
	seedTaskPollingChannel(t, firstChannelID, false)
	seedTaskPollingChannel(t, secondChannelID, false)
	firstChannelFirst := seedPollingTask(t, firstChannelID, "task_public_5", "upstream_a_1")
	firstChannelSecond := seedPollingTask(t, firstChannelID, "task_public_6", "upstream_a_2")
	secondChannelFirst := seedPollingTask(t, secondChannelID, "task_public_7", "upstream_b_1")
	secondChannelSecond := seedPollingTask(t, secondChannelID, "task_public_8", "upstream_b_2")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), map[int][]string{
		firstChannelID: {
			firstChannelFirst.GetUpstreamTaskID(),
			firstChannelSecond.GetUpstreamTaskID(),
		},
		secondChannelID: {
			secondChannelFirst.GetUpstreamTaskID(),
			secondChannelSecond.GetUpstreamTaskID(),
		},
	}, map[string]*model.Task{
		firstChannelFirst.GetUpstreamTaskID():   firstChannelFirst,
		firstChannelSecond.GetUpstreamTaskID():  firstChannelSecond,
		secondChannelFirst.GetUpstreamTaskID():  secondChannelFirst,
		secondChannelSecond.GetUpstreamTaskID(): secondChannelSecond,
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ElementsMatch(t, []string{"upstream_a_1", "upstream_b_1"}, adaptor.fetchedTaskIDs())
}

func TestUpdateVideoTasksSlowChannelDoesNotBlockOtherChannels(t *testing.T) {
	truncate(t)

	const slowChannelID = 251
	const fastChannelID = 252
	seedTaskPollingChannel(t, slowChannelID, false)
	seedTaskPollingChannel(t, fastChannelID, true)
	slowTask := seedPollingTask(t, slowChannelID, "task_public_slow", "upstream_slow_1")
	fastFirst := seedPollingTask(t, fastChannelID, "task_public_fast_1", "upstream_fast_parallel_1")
	fastSecond := seedPollingTask(t, fastChannelID, "task_public_fast_2", "upstream_fast_parallel_2")

	adaptor := &taskPollingFetchAdaptor{
		fetched:      make(chan string, 4),
		blockTaskID:  slowTask.GetUpstreamTaskID(),
		blockStarted: make(chan struct{}),
		releaseBlock: make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseBlockedTask := func() {
		releaseOnce.Do(func() {
			close(adaptor.releaseBlock)
		})
	}
	t.Cleanup(releaseBlockedTask)
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	errCh := make(chan error, 1)
	gopool.Go(func() {
		errCh <- UpdateVideoTasks(context.Background(), constant.TaskPlatform("kling"), map[int][]string{
			slowChannelID: {
				slowTask.GetUpstreamTaskID(),
			},
			fastChannelID: {
				fastFirst.GetUpstreamTaskID(),
				fastSecond.GetUpstreamTaskID(),
			},
		}, map[string]*model.Task{
			slowTask.GetUpstreamTaskID():   slowTask,
			fastFirst.GetUpstreamTaskID():  fastFirst,
			fastSecond.GetUpstreamTaskID(): fastSecond,
		})
	})

	select {
	case <-adaptor.blockStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("slow channel did not start blocking")
	}

	require.Eventually(t, func() bool {
		fetchedTaskIDs := adaptor.fetchedTaskIDs()
		return len(fetchedTaskIDs) == 2 &&
			fetchedTaskIDs[0] == fastFirst.GetUpstreamTaskID() &&
			fetchedTaskIDs[1] == fastSecond.GetUpstreamTaskID()
	}, 500*time.Millisecond, 10*time.Millisecond)

	releaseBlockedTask()
	require.NoError(t, <-errCh)
	assert.ElementsMatch(t, []string{
		slowTask.GetUpstreamTaskID(),
		fastFirst.GetUpstreamTaskID(),
		fastSecond.GetUpstreamTaskID(),
	}, adaptor.fetchedTaskIDs())
}

func TestUpdateVideoTasksMixedChannelSleepSettings(t *testing.T) {
	truncate(t)

	const sleepyChannelID = 301
	const fastChannelID = 302
	seedTaskPollingChannel(t, sleepyChannelID, false)
	seedTaskPollingChannel(t, fastChannelID, true)
	sleepyFirst := seedPollingTask(t, sleepyChannelID, "task_public_9", "upstream_sleepy_1")
	sleepySecond := seedPollingTask(t, sleepyChannelID, "task_public_10", "upstream_sleepy_2")
	fastFirst := seedPollingTask(t, fastChannelID, "task_public_11", "upstream_fast_1")
	fastSecond := seedPollingTask(t, fastChannelID, "task_public_12", "upstream_fast_2")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), map[int][]string{
		sleepyChannelID: {
			sleepyFirst.GetUpstreamTaskID(),
			sleepySecond.GetUpstreamTaskID(),
		},
		fastChannelID: {
			fastFirst.GetUpstreamTaskID(),
			fastSecond.GetUpstreamTaskID(),
		},
	}, map[string]*model.Task{
		sleepyFirst.GetUpstreamTaskID():  sleepyFirst,
		sleepySecond.GetUpstreamTaskID(): sleepySecond,
		fastFirst.GetUpstreamTaskID():    fastFirst,
		fastSecond.GetUpstreamTaskID():   fastSecond,
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ElementsMatch(t, []string{"upstream_sleepy_1", "upstream_fast_1", "upstream_fast_2"}, adaptor.fetchedTaskIDs())
}

type terminalFailureTaskPollingAdaptor struct{}

func (terminalFailureTaskPollingAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (terminalFailureTaskPollingAdaptor) FetchTask(_ string, _ string, body map[string]any, _ string) (*http.Response, error) {
	taskID, _ := body["task_id"].(string)
	responseBody, err := common.Marshal(dto.TaskResponse[model.Task]{
		Code: dto.TaskSuccessCode,
		Data: model.Task{
			TaskID:     taskID,
			Status:     model.TaskStatusFailure,
			Progress:   "100%",
			FailReason: "upstream failure",
		},
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
	}, nil
}

func (terminalFailureTaskPollingAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return nil, nil
}

func (terminalFailureTaskPollingAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}

type sunoTerminalFailureTaskPollingAdaptor struct{}

func (sunoTerminalFailureTaskPollingAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (sunoTerminalFailureTaskPollingAdaptor) FetchTask(_ string, _ string, body map[string]any, _ string) (*http.Response, error) {
	ids, _ := body["ids"].([]string)
	responseBody, err := common.Marshal(dto.TaskResponse[[]dto.SunoDataResponse]{
		Code: dto.TaskSuccessCode,
		Data: []dto.SunoDataResponse{{
			TaskID:     ids[0],
			Status:     string(model.TaskStatusFailure),
			FailReason: "upstream failure",
		}},
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
	}, nil
}

func (sunoTerminalFailureTaskPollingAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return nil, nil
}

func (sunoTerminalFailureTaskPollingAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}

type sunoSuccessTaskPollingAdaptor struct{}

func (sunoSuccessTaskPollingAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (sunoSuccessTaskPollingAdaptor) FetchTask(_ string, _ string, body map[string]any, _ string) (*http.Response, error) {
	ids, _ := body["ids"].([]string)
	responseBody, err := common.Marshal(dto.TaskResponse[[]dto.SunoDataResponse]{
		Code: dto.TaskSuccessCode,
		Data: []dto.SunoDataResponse{{
			TaskID: ids[0],
			Status: string(model.TaskStatusSuccess),
		}},
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
	}, nil
}

func (sunoSuccessTaskPollingAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return nil, nil
}

func (sunoSuccessTaskPollingAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}

type successfulTaskPollingAdaptor struct {
	actualQuota int
}

func (a successfulTaskPollingAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (a successfulTaskPollingAdaptor) FetchTask(_ string, _ string, body map[string]any, _ string) (*http.Response, error) {
	taskID, _ := body["task_id"].(string)
	responseBody, err := common.Marshal(dto.TaskResponse[model.Task]{
		Code: dto.TaskSuccessCode,
		Data: model.Task{
			TaskID:   taskID,
			Status:   model.TaskStatusSuccess,
			Progress: "100%",
		},
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
	}, nil
}

func (a successfulTaskPollingAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return nil, nil
}

func (a successfulTaskPollingAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return a.actualQuota
}

func (a successfulTaskPollingAdaptor) ActualQuotaOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) (int, bool) {
	return a.actualQuota, true
}

type unknownQuotaTaskPollingAdaptor struct {
	taskcommon.BaseBilling
}

func (unknownQuotaTaskPollingAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (unknownQuotaTaskPollingAdaptor) FetchTask(baseURL string, key string, body map[string]any, proxy string) (*http.Response, error) {
	return successfulTaskPollingAdaptor{}.FetchTask(baseURL, key, body, proxy)
}

func (unknownQuotaTaskPollingAdaptor) ParseTaskResult(body []byte) (*relaycommon.TaskInfo, error) {
	return successfulTaskPollingAdaptor{}.ParseTaskResult(body)
}

func seedBrokenAetherIntegration(t *testing.T, channelID int, instanceID string) *model.AetherIntegration {
	t.Helper()
	integration := &model.AetherIntegration{
		ChannelID:                   channelID,
		InstanceID:                  instanceID,
		ExecutionMode:               model.AetherExecutionModeDirectChannel,
		Enabled:                     true,
		ConfigRevision:              1,
		ControlSecretEncrypted:      "invalid-secret",
		RelaySigningSecretEncrypted: "invalid-secret",
	}
	require.NoError(t, model.DB.Create(integration).Error)
	return integration
}

func repairAetherIntegrationSecrets(t *testing.T, integration *model.AetherIntegration) {
	t.Helper()
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, model.DB.Save(integration).Error)
}

func TestChannelLookupFailureLeavesSunoAndVideoTasksRetryable(t *testing.T) {
	truncate(t)

	const missingChannelID = 404
	sunoTask := makeTask(404, missingChannelID, 100, 0, BillingSourceWallet, 0)
	sunoTask.TaskID = "suno-channel-cache-miss"
	sunoTask.PrivateData.UpstreamTaskID = "upstream-suno-channel-cache-miss"
	videoTask := makeTask(405, missingChannelID, 100, 0, BillingSourceWallet, 0)
	videoTask.TaskID = "video-channel-cache-miss"
	videoTask.PrivateData.UpstreamTaskID = "upstream-video-channel-cache-miss"
	require.NoError(t, model.DB.Create(sunoTask).Error)
	require.NoError(t, model.DB.Create(videoTask).Error)

	err := updateSunoTasks(context.Background(), missingChannelID, []string{sunoTask.GetUpstreamTaskID()}, map[string]*model.Task{
		sunoTask.GetUpstreamTaskID(): sunoTask,
	})
	require.Error(t, err)
	require.NoError(t, UpdateVideoTasks(context.Background(), constant.TaskPlatform("kling"), map[int][]string{
		missingChannelID: {videoTask.GetUpstreamTaskID()},
	}, map[string]*model.Task{
		videoTask.GetUpstreamTaskID(): videoTask,
	}))

	var reloadedSuno, reloadedVideo model.Task
	require.NoError(t, model.DB.First(&reloadedSuno, sunoTask.ID).Error)
	require.NoError(t, model.DB.First(&reloadedVideo, videoTask.ID).Error)
	assert.EqualValues(t, model.TaskStatusInProgress, reloadedSuno.Status)
	assert.EqualValues(t, model.TaskStatusInProgress, reloadedVideo.Status)
}

func TestSunoPollDoesNotOverwriteConcurrentFailureTransition(t *testing.T) {
	truncate(t)

	const channelID = 406
	baseURL := "https://suno.example.test"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Type:    constant.ChannelTypeSunoAPI,
		Key:     "sk-test",
		BaseURL: &baseURL,
		Status:  common.ChannelStatusEnabled,
	}).Error)
	task := makeTask(406, channelID, 100, 0, BillingSourceWallet, 0)
	task.TaskID = "suno-stale-success"
	task.PrivateData.UpstreamTaskID = "upstream-suno-stale-success"
	require.NoError(t, model.DB.Create(task).Error)

	staleTask := *task
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("status", model.TaskStatusFailure).Error)
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return sunoSuccessTaskPollingAdaptor{} }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	require.NoError(t, updateSunoTasks(context.Background(), channelID, []string{staleTask.GetUpstreamTaskID()}, map[string]*model.Task{
		staleTask.GetUpstreamTaskID(): &staleTask,
	}))

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusFailure, reloaded.Status)
}

func TestVideoSuccessSettlementOutboxFailureKeepsTaskRetryable(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 407, 407, 407
	const preConsumed, actualQuota = 500, 300
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-video-success-outbox", 0)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", preConsumed).Error)
	baseURL := "https://video.example.test"
	channel := &model.Channel{Id: channelID, Type: constant.ChannelTypeKling, Key: "sk-test", BaseURL: &baseURL, Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(channel).Error)
	integration := seedBrokenAetherIntegration(t, channelID, "aether-video-success-outbox")
	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.TaskID = "video-success-outbox"
	task.PrivateData.UpstreamTaskID = "upstream-video-success-outbox"
	task.Progress = "30%"
	require.NoError(t, model.DB.Create(task).Error)

	err := updateVideoSingleTask(context.Background(), successfulTaskPollingAdaptor{actualQuota: actualQuota}, channel, task.GetUpstreamTaskID(), map[string]*model.Task{
		task.GetUpstreamTaskID(): task,
	})
	require.Error(t, err)
	var afterFailure model.Task
	require.NoError(t, model.DB.First(&afterFailure, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusInProgress, afterFailure.Status)
	assert.Equal(t, preConsumed, afterFailure.Quota)
	assert.Zero(t, getUserQuota(t, userID))
	assert.Zero(t, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, preConsumed, getTokenUsedQuota(t, tokenID))

	repairAetherIntegrationSecrets(t, integration)
	var retryTask model.Task
	require.NoError(t, model.DB.First(&retryTask, task.ID).Error)
	require.NoError(t, updateVideoSingleTask(context.Background(), successfulTaskPollingAdaptor{actualQuota: actualQuota}, channel, retryTask.GetUpstreamTaskID(), map[string]*model.Task{
		retryTask.GetUpstreamTaskID(): &retryTask,
	}))
	var completed model.Task
	require.NoError(t, model.DB.First(&completed, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusSuccess, completed.Status)
	assert.Equal(t, actualQuota, completed.Quota)
	assert.Equal(t, preConsumed-actualQuota, getUserQuota(t, userID))
	assert.Equal(t, preConsumed-actualQuota, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, actualQuota, getTokenUsedQuota(t, tokenID))
	var eventCount int64
	require.NoError(t, model.DB.Model(&model.AetherLedgerEvent{}).Where("event_type = ?", model.AetherLedgerEventFinancial).Count(&eventCount).Error)
	assert.Equal(t, int64(1), eventCount)
}

func TestVideoSuccessSettlesActualQuotaZero(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 408, 408, 408
	const preConsumed = 500
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-video-success-zero", 0)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", preConsumed).Error)
	baseURL := "https://video.example.test"
	channel := &model.Channel{Id: channelID, Type: constant.ChannelTypeKling, Key: "sk-test", BaseURL: &baseURL, Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(channel).Error)
	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.TaskID = "video-success-zero"
	task.PrivateData.UpstreamTaskID = "upstream-video-success-zero"
	task.Progress = "30%"
	require.NoError(t, model.DB.Create(task).Error)

	require.NoError(t, updateVideoSingleTask(context.Background(), successfulTaskPollingAdaptor{actualQuota: 0}, channel, task.GetUpstreamTaskID(), map[string]*model.Task{
		task.GetUpstreamTaskID(): task,
	}))

	var completed model.Task
	require.NoError(t, model.DB.First(&completed, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusSuccess, completed.Status)
	assert.Zero(t, completed.Quota)
	assert.Equal(t, preConsumed, getUserQuota(t, userID))
	assert.Equal(t, preConsumed, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
}

func TestVideoSuccessRejectsNegativeKnownActualQuota(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 410, 410, 410
	const preConsumed = 500
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-video-success-negative", 0)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", preConsumed).Error)
	baseURL := "https://video.example.test"
	channel := &model.Channel{Id: channelID, Type: constant.ChannelTypeKling, Key: "sk-test", BaseURL: &baseURL, Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(channel).Error)
	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.TaskID = "video-success-negative"
	task.PrivateData.UpstreamTaskID = "upstream-video-success-negative"
	task.Progress = "30%"
	require.NoError(t, model.DB.Create(task).Error)

	err := updateVideoSingleTask(context.Background(), successfulTaskPollingAdaptor{actualQuota: -1}, channel, task.GetUpstreamTaskID(), map[string]*model.Task{
		task.GetUpstreamTaskID(): task,
	})
	require.ErrorContains(t, err, "task actual quota cannot be negative")

	var afterFailure model.Task
	require.NoError(t, model.DB.First(&afterFailure, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusInProgress, afterFailure.Status)
	assert.Equal(t, preConsumed, afterFailure.Quota)
	assert.Zero(t, getUserQuota(t, userID))
	assert.Zero(t, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, preConsumed, getTokenUsedQuota(t, tokenID))
}

func TestVideoSuccessPerCallRejectsNegativeKnownActualQuota(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 411, 411, 411
	const preConsumed = 500
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-video-success-per-call-negative", 0)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", preConsumed).Error)
	baseURL := "https://video.example.test"
	channel := &model.Channel{Id: channelID, Type: constant.ChannelTypeKling, Key: "sk-test", BaseURL: &baseURL, Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(channel).Error)
	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.TaskID = "video-success-per-call-negative"
	task.PrivateData.UpstreamTaskID = "upstream-video-success-per-call-negative"
	task.PrivateData.BillingContext.PerCallBilling = true
	task.Progress = "30%"
	require.NoError(t, model.DB.Create(task).Error)

	err := updateVideoSingleTask(context.Background(), successfulTaskPollingAdaptor{actualQuota: -1}, channel, task.GetUpstreamTaskID(), map[string]*model.Task{
		task.GetUpstreamTaskID(): task,
	})
	require.ErrorContains(t, err, "task actual quota cannot be negative")

	var afterFailure model.Task
	require.NoError(t, model.DB.First(&afterFailure, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusInProgress, afterFailure.Status)
	assert.Equal(t, preConsumed, afterFailure.Quota)
	assert.Zero(t, getUserQuota(t, userID))
	assert.Zero(t, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, preConsumed, getTokenUsedQuota(t, tokenID))
}

func TestVideoSuccessKeepsPreConsumedQuotaWhenActualQuotaUnknown(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 409, 409, 409
	const preConsumed = 500
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-video-success-unknown", 0)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", preConsumed).Error)
	baseURL := "https://video.example.test"
	channel := &model.Channel{Id: channelID, Type: constant.ChannelTypeKling, Key: "sk-test", BaseURL: &baseURL, Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(channel).Error)
	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.TaskID = "video-success-unknown"
	task.PrivateData.UpstreamTaskID = "upstream-video-success-unknown"
	task.Progress = "30%"
	require.NoError(t, model.DB.Create(task).Error)

	require.NoError(t, updateVideoSingleTask(context.Background(), unknownQuotaTaskPollingAdaptor{}, channel, task.GetUpstreamTaskID(), map[string]*model.Task{
		task.GetUpstreamTaskID(): task,
	}))

	var completed model.Task
	require.NoError(t, model.DB.First(&completed, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusSuccess, completed.Status)
	assert.Equal(t, preConsumed, completed.Quota)
	assert.Zero(t, getUserQuota(t, userID))
	assert.Zero(t, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, preConsumed, getTokenUsedQuota(t, tokenID))
}

func TestSunoTerminalFailureRetriesRefundAfterOutboxFailureBeforeCommittingStatus(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID, quota = 403, 403, 403, 100
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-suno-terminal-refund-retry", 0)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", quota).Error)
	baseURL := "https://suno.example.test"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Type:    constant.ChannelTypeSunoAPI,
		Key:     "sk-test",
		BaseURL: &baseURL,
		Status:  common.ChannelStatusEnabled,
	}).Error)
	integration := seedBrokenAetherIntegration(t, channelID, "aether-suno-terminal-refund-retry")

	task := makeTask(userID, channelID, quota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "suno-terminal-refund-retry"
	task.PrivateData.UpstreamTaskID = "upstream-suno-terminal-refund-retry"
	task.Progress = "30%"
	require.NoError(t, model.DB.Create(task).Error)

	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor {
		return sunoTerminalFailureTaskPollingAdaptor{}
	}
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	err := updateSunoTasks(context.Background(), channelID, []string{task.GetUpstreamTaskID()}, map[string]*model.Task{
		task.GetUpstreamTaskID(): task,
	})
	require.Error(t, err, "failed outbox must leave Suno task retryable")

	var afterFailure model.Task
	require.NoError(t, model.DB.First(&afterFailure, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusInProgress, afterFailure.Status)
	assert.Equal(t, 0, getUserQuota(t, userID))
	assert.Equal(t, 0, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, quota, getTokenUsedQuota(t, tokenID))
	var claimCount, eventCount int64
	require.NoError(t, model.DB.Model(&model.BillingRefundClaim{}).Count(&claimCount).Error)
	require.NoError(t, model.DB.Model(&model.AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Zero(t, claimCount)
	assert.Zero(t, eventCount)

	repairAetherIntegrationSecrets(t, integration)
	var retryTask model.Task
	require.NoError(t, model.DB.First(&retryTask, task.ID).Error)
	require.NoError(t, updateSunoTasks(context.Background(), channelID, []string{retryTask.GetUpstreamTaskID()}, map[string]*model.Task{
		retryTask.GetUpstreamTaskID(): &retryTask,
	}))

	var completed model.Task
	require.NoError(t, model.DB.First(&completed, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusFailure, completed.Status)
	assert.Equal(t, quota, getUserQuota(t, userID))
	assert.Equal(t, quota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	require.NoError(t, model.DB.Model(&model.BillingRefundClaim{}).Count(&claimCount).Error)
	require.NoError(t, model.DB.Model(&model.AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Equal(t, int64(1), claimCount)
	assert.Equal(t, int64(1), eventCount)
}

func TestTerminalFailureRetriesRefundAfterOutboxFailureBeforeCommittingStatus(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID, quota = 401, 401, 401, 100
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-terminal-refund-retry", 0)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", quota).Error)
	seedTaskPollingChannel(t, channelID, true)
	integration := seedBrokenAetherIntegration(t, channelID, "aether-terminal-refund-retry")

	task := makeTask(userID, channelID, quota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "terminal-refund-retry"
	task.PrivateData.UpstreamTaskID = "upstream-terminal-refund-retry"
	task.Progress = "30%"
	require.NoError(t, model.DB.Create(task).Error)

	channel := &model.Channel{Id: channelID, Type: constant.ChannelTypeKling, Key: "sk-test"}
	adaptor := terminalFailureTaskPollingAdaptor{}
	err := updateVideoSingleTask(context.Background(), adaptor, channel, task.GetUpstreamTaskID(), map[string]*model.Task{
		task.GetUpstreamTaskID(): task,
	})
	require.Error(t, err, "failed outbox must keep the terminal transition retryable")

	var afterFailure model.Task
	require.NoError(t, model.DB.First(&afterFailure, task.ID).Error)
	require.EqualValues(t, model.TaskStatusInProgress, afterFailure.Status)
	assert.Equal(t, 0, getUserQuota(t, userID))
	assert.Equal(t, 0, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, quota, getTokenUsedQuota(t, tokenID))
	var claimCount, eventCount int64
	require.NoError(t, model.DB.Model(&model.BillingRefundClaim{}).Count(&claimCount).Error)
	require.NoError(t, model.DB.Model(&model.AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Zero(t, claimCount)
	assert.Zero(t, eventCount)

	repairAetherIntegrationSecrets(t, integration)
	var retryTask model.Task
	require.NoError(t, model.DB.First(&retryTask, task.ID).Error)
	require.NoError(t, updateVideoSingleTask(context.Background(), adaptor, channel, retryTask.GetUpstreamTaskID(), map[string]*model.Task{
		retryTask.GetUpstreamTaskID(): &retryTask,
	}))

	var completed model.Task
	require.NoError(t, model.DB.First(&completed, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusFailure, completed.Status)
	assert.Equal(t, quota, getUserQuota(t, userID))
	assert.Equal(t, quota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	require.NoError(t, model.DB.Model(&model.BillingRefundClaim{}).Count(&claimCount).Error)
	require.NoError(t, model.DB.Model(&model.AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Equal(t, int64(1), claimCount)
	assert.Equal(t, int64(1), eventCount)

	var duplicatePollTask model.Task
	require.NoError(t, model.DB.First(&duplicatePollTask, task.ID).Error)
	require.NoError(t, updateVideoSingleTask(context.Background(), adaptor, channel, duplicatePollTask.GetUpstreamTaskID(), map[string]*model.Task{
		duplicatePollTask.GetUpstreamTaskID(): &duplicatePollTask,
	}))
	assert.Equal(t, quota, getUserQuota(t, userID))
	assert.Equal(t, quota, getTokenRemainQuota(t, tokenID))
	require.NoError(t, model.DB.Model(&model.BillingRefundClaim{}).Count(&claimCount).Error)
	require.NoError(t, model.DB.Model(&model.AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Equal(t, int64(1), claimCount)
	assert.Equal(t, int64(1), eventCount)
}

func TestTimeoutRetriesRefundAfterOutboxFailureBeforeCommittingStatus(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID, quota = 402, 402, 402, 100
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-timeout-refund-retry", 0)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", quota).Error)
	seedTaskPollingChannel(t, channelID, true)
	integration := seedBrokenAetherIntegration(t, channelID, "aether-timeout-refund-retry")

	task := makeTask(userID, channelID, quota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "timeout-refund-retry"
	task.Progress = "30%"
	task.SubmitTime = time.Now().Add(-2 * time.Minute).Unix()
	require.NoError(t, model.DB.Create(task).Error)

	previousTimeout := constant.TaskTimeoutMinutes
	constant.TaskTimeoutMinutes = 1
	t.Cleanup(func() { constant.TaskTimeoutMinutes = previousTimeout })

	sweepTimedOutTasks(context.Background())

	var afterFailure model.Task
	require.NoError(t, model.DB.First(&afterFailure, task.ID).Error)
	require.EqualValues(t, model.TaskStatusInProgress, afterFailure.Status)
	assert.Equal(t, 0, getUserQuota(t, userID))
	assert.Equal(t, 0, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, quota, getTokenUsedQuota(t, tokenID))
	var claimCount, eventCount int64
	require.NoError(t, model.DB.Model(&model.BillingRefundClaim{}).Count(&claimCount).Error)
	require.NoError(t, model.DB.Model(&model.AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Zero(t, claimCount)
	assert.Zero(t, eventCount)

	repairAetherIntegrationSecrets(t, integration)
	sweepTimedOutTasks(context.Background())

	var completed model.Task
	require.NoError(t, model.DB.First(&completed, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusFailure, completed.Status)
	assert.Equal(t, quota, getUserQuota(t, userID))
	assert.Equal(t, quota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	require.NoError(t, model.DB.Model(&model.BillingRefundClaim{}).Count(&claimCount).Error)
	require.NoError(t, model.DB.Model(&model.AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Equal(t, int64(1), claimCount)
	assert.Equal(t, int64(1), eventCount)

	sweepTimedOutTasks(context.Background())
	assert.Equal(t, quota, getUserQuota(t, userID))
	assert.Equal(t, quota, getTokenRemainQuota(t, tokenID))
	require.NoError(t, model.DB.Model(&model.BillingRefundClaim{}).Count(&claimCount).Error)
	require.NoError(t, model.DB.Model(&model.AetherLedgerEvent{}).Count(&eventCount).Error)
	assert.Equal(t, int64(1), claimCount)
	assert.Equal(t, int64(1), eventCount)
}

func TestUpdateSunoTasksStalePollsRefundExactlyOnce(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 401, 401, 401
	const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 2_500
	const publicTaskID, upstreamTaskID = "suno_public_refund_once", "suno_upstream_refund_once"

	seedUser(t, userID, initialUserQuota)
	seedToken(t, tokenID, userID, "sk-suno-refund-once", initialTokenQuota)
	baseURL := "https://suno.invalid"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Type:    constant.ChannelTypeSunoAPI,
		Name:    "suno_refund_once",
		Key:     "sk-suno-channel",
		Status:  common.ChannelStatusEnabled,
		BaseURL: &baseURL,
	}).Error)

	task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = publicTaskID
	task.Platform = constant.TaskPlatformSuno
	task.Status = model.TaskStatusInProgress
	task.Progress = "50%"
	task.SubmitTime = model.TaskRefundLegacyCutoff
	task.PrivateData.UpstreamTaskID = upstreamTaskID
	require.NoError(t, model.DB.Create(task).Error)

	var firstPollTask model.Task
	var staleSecondPollTask model.Task
	require.NoError(t, model.DB.First(&firstPollTask, task.ID).Error)
	require.NoError(t, model.DB.First(&staleSecondPollTask, task.ID).Error)

	adaptor := &sunoFailurePollingAdaptor{failReason: "upstream failed"}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	require.NoError(t, updateSunoTasks(context.Background(), channelID, []string{upstreamTaskID}, map[string]*model.Task{
		upstreamTaskID: &firstPollTask,
	}))
	require.NoError(t, updateSunoTasks(context.Background(), channelID, []string{upstreamTaskID}, map[string]*model.Task{
		upstreamTaskID: &staleSecondPollTask,
	}))

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusFailure, reloaded.Status)
	assert.Zero(t, reloaded.Quota)
	assert.Equal(t, initialUserQuota+taskQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota+taskQuota, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, int64(1), countLogs(t))
}

func TestSweepUnrefundedFailedTasksRefundsModernTaskAndSkipsLegacy(t *testing.T) {
	truncate(t)

	const userID = 402
	const initialQuota, modernTaskQuota, legacyTaskQuota = 10_000, 1_200, 1_800
	seedUser(t, userID, initialQuota)

	modernTask := makeTask(userID, 0, modernTaskQuota, 0, BillingSourceWallet, 0)
	modernTask.TaskID = "modern_failed_pending_refund"
	modernTask.Status = model.TaskStatusFailure
	modernTask.Progress = "100%"
	modernTask.SubmitTime = model.TaskRefundLegacyCutoff
	modernTask.UpdatedAt = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, model.DB.Create(modernTask).Error)

	legacyTask := makeTask(userID, 0, legacyTaskQuota, 0, BillingSourceWallet, 0)
	legacyTask.TaskID = "legacy_failed_without_refund"
	legacyTask.Status = model.TaskStatusFailure
	legacyTask.Progress = "100%"
	legacyTask.SubmitTime = model.TaskRefundLegacyCutoff - 1
	legacyTask.UpdatedAt = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, model.DB.Create(legacyTask).Error)

	sweepUnrefundedFailedTasks(context.Background())
	sweepUnrefundedFailedTasks(context.Background())

	var reloadedModern model.Task
	var reloadedLegacy model.Task
	require.NoError(t, model.DB.First(&reloadedModern, modernTask.ID).Error)
	require.NoError(t, model.DB.First(&reloadedLegacy, legacyTask.ID).Error)
	assert.Zero(t, reloadedModern.Quota)
	assert.Equal(t, legacyTaskQuota, reloadedLegacy.Quota)
	assert.Equal(t, initialQuota+modernTaskQuota, getUserQuota(t, userID))
	assert.Equal(t, int64(1), countLogs(t))
}

func TestSweepUnrefundedFailedTasksRestoresMarkerAfterFundingFailure(t *testing.T) {
	truncate(t)

	const userID, subscriptionID, taskQuota = 404, 404, 900
	const subscriptionUsed int64 = 5_000
	seedUser(t, userID, 0)

	task := makeTask(userID, 0, taskQuota, 0, BillingSourceSubscription, subscriptionID)
	task.TaskID = "subscription_failed_pending_refund"
	task.Status = model.TaskStatusFailure
	task.Progress = "100%"
	task.SubmitTime = model.TaskRefundLegacyCutoff
	task.UpdatedAt = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, model.DB.Create(task).Error)

	sweepUnrefundedFailedTasks(context.Background())

	var afterFailedRefund model.Task
	require.NoError(t, model.DB.First(&afterFailedRefund, task.ID).Error)
	assert.Equal(t, taskQuota, afterFailedRefund.Quota)
	assert.Equal(t, int64(0), countLogs(t))

	seedSubscription(t, subscriptionID, userID, 10_000, subscriptionUsed)
	require.NoError(t, model.DB.Model(&model.Task{}).
		Where("id = ?", task.ID).
		UpdateColumn("updated_at", time.Now().Add(-time.Minute).Unix()).Error)

	sweepUnrefundedFailedTasks(context.Background())

	var afterSuccessfulRetry model.Task
	require.NoError(t, model.DB.First(&afterSuccessfulRetry, task.ID).Error)
	assert.Zero(t, afterSuccessfulRetry.Quota)
	assert.Equal(t, subscriptionUsed-int64(taskQuota), getSubscriptionUsed(t, subscriptionID))
	assert.Equal(t, int64(1), countLogs(t))
}
