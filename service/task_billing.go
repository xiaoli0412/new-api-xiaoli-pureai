package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// LogTaskConsumption writes only the task consumption audit log. Financial
// settlement and usage statistics are recorded by the successful caller.
func LogTaskConsumption(c *gin.Context, info *relaycommon.RelayInfo) {
	logContent := fmt.Sprintf("操作 %s", info.Action)
	// 支持任务仅按次计费
	if common.StringsContains(constant.TaskPricePatches, info.OriginModelName) {
		logContent = fmt.Sprintf("%s，按次计费", logContent)
	} else {
		if otherRatios := info.PriceData.OtherRatios(); len(otherRatios) > 0 {
			var contents []string
			for key, ra := range otherRatios {
				if 1.0 != ra {
					contents = append(contents, fmt.Sprintf("%s: %.2f", key, ra))
				}
			}
			if len(contents) > 0 {
				logContent = fmt.Sprintf("%s, 计算参数：%s", logContent, strings.Join(contents, ", "))
			}
		}
	}
	other := make(map[string]interface{})
	other["is_task"] = true
	other["request_path"] = c.Request.URL.Path
	other["model_price"] = info.PriceData.ModelPrice
	if info.PriceData.ModelRatio > 0 {
		other["model_ratio"] = info.PriceData.ModelRatio
	}
	other["group_ratio"] = info.PriceData.GroupRatioInfo.GroupRatio
	if info.PriceData.GroupRatioInfo.HasSpecialRatio {
		other["user_group_ratio"] = info.PriceData.GroupRatioInfo.GroupSpecialRatio
	}
	if info.IsModelMapped {
		other["is_model_mapped"] = true
		other["upstream_model_name"] = info.UpstreamModelName
	}
	attachQuotaSaturation(c, info, other)
	if err := model.RecordTaskBillingAuditLog(model.RecordTaskBillingLogParams{
		UserId:    info.UserId,
		LogType:   model.LogTypeConsume,
		ChannelId: info.ChannelId,
		ModelName: info.OriginModelName,
		Quota:     info.PriceData.Quota,
		Content:   logContent,
		TokenId:   info.TokenId,
		Group:     info.UsingGroup,
		Other:     other,
	}); err != nil {
		logger.LogWarn(c, fmt.Sprintf("记录任务消费审计日志失败: %s", err.Error()))
	}
}

// ---------------------------------------------------------------------------
// 异步任务计费辅助函数
// ---------------------------------------------------------------------------

// resolveTokenKey 通过 TokenId 运行时获取令牌 Key（用于 Redis 缓存操作）。
// 如果令牌已被删除或查询失败，返回空字符串。
func resolveTokenKey(ctx context.Context, tokenId int, taskID string) string {
	tokenKey, err := model.GetTokenKeyById(tokenId)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("获取令牌 key 失败 (tokenId=%d, task=%s): %s", tokenId, taskID, err.Error()))
		return ""
	}
	return tokenKey
}

// taskIsSubscription 判断任务是否通过订阅计费。
func taskIsSubscription(task *model.Task) bool {
	return task.PrivateData.BillingSource == BillingSourceSubscription && task.PrivateData.SubscriptionId > 0
}

// taskAdjustFunding 调整任务的资金来源（钱包或订阅），delta > 0 表示扣费，delta < 0 表示退还。
func taskAdjustFunding(task *model.Task, delta int) error {
	if taskIsSubscription(task) {
		return model.PostConsumeUserSubscriptionDelta(task.PrivateData.SubscriptionId, int64(delta))
	}
	if delta > 0 {
		return model.DecreaseUserQuota(task.UserId, delta, false)
	}
	return model.IncreaseUserQuota(task.UserId, -delta, false)
}

// taskAdjustTokenQuota 调整任务的令牌额度，delta > 0 表示扣费，delta < 0 表示退还。
// 需要通过 resolveTokenKey 运行时获取 key（不从 PrivateData 中读取）。
func taskAdjustTokenQuota(ctx context.Context, task *model.Task, delta int) {
	if task.PrivateData.TokenId <= 0 || delta == 0 {
		return
	}
	tokenKey := resolveTokenKey(ctx, task.PrivateData.TokenId, task.TaskID)
	if tokenKey == "" {
		return
	}
	var err error
	if delta > 0 {
		err = model.DecreaseTokenQuota(task.PrivateData.TokenId, tokenKey, delta)
	} else {
		err = model.IncreaseTokenQuota(task.PrivateData.TokenId, tokenKey, -delta)
	}
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("调整令牌额度失败 (delta=%d, task=%s): %s", delta, task.TaskID, err.Error()))
	}
}

func taskAdjustFundingTx(tx *gorm.DB, task *model.Task, delta int) error {
	if taskIsSubscription(task) {
		return model.PostConsumeUserSubscriptionDeltaTx(tx, task.PrivateData.SubscriptionId, int64(delta))
	}
	if delta > 0 {
		return model.DecreaseUserQuotaTx(tx, task.UserId, delta)
	}
	return model.IncreaseUserQuotaTx(tx, task.UserId, -delta)
}

func taskAdjustTokenQuotaTx(tx *gorm.DB, task *model.Task, delta int) error {
	if task.PrivateData.TokenId <= 0 || delta == 0 {
		return nil
	}
	if delta > 0 {
		return model.DecreaseTokenQuotaTx(tx, task.PrivateData.TokenId, delta)
	}
	return model.IncreaseTokenQuotaTx(tx, task.PrivateData.TokenId, -delta)
}

func updateTaskQuotaTx(tx *gorm.DB, task *model.Task, expectedQuota int, actualQuota int) error {
	if task.ID <= 0 {
		return nil
	}
	result := tx.Model(&model.Task{}).
		Where("id = ? AND quota = ?", task.ID, expectedQuota).
		Update("quota", actualQuota)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("task quota changed concurrently: task %d", task.ID)
	}
	return nil
}

func updateTaskTokenQuotaCache(ctx context.Context, task *model.Task, quotaDelta int) {
	if task.PrivateData.TokenId <= 0 || quotaDelta == 0 {
		return
	}
	tokenKey := resolveTokenKey(ctx, task.PrivateData.TokenId, task.TaskID)
	if tokenKey == "" {
		return
	}
	if quotaDelta > 0 {
		model.DecreaseTokenQuotaCache(tokenKey, quotaDelta)
		return
	}
	model.IncreaseTokenQuotaCache(tokenKey, -quotaDelta)
}

func updateTaskQuotaCaches(ctx context.Context, task *model.Task, quotaDelta int) {
	if quotaDelta > 0 {
		if !taskIsSubscription(task) {
			model.DecreaseUserQuotaCache(task.UserId, quotaDelta)
		}
		updateTaskTokenQuotaCache(ctx, task, quotaDelta)
		return
	}

	refundQuota := -quotaDelta
	if !taskIsSubscription(task) {
		model.IncreaseUserQuotaCache(task.UserId, refundQuota)
	}
	updateTaskTokenQuotaCache(ctx, task, quotaDelta)
}

// taskBillingOther 从 task 的 BillingContext 构建日志 Other 字段。
func taskBillingOther(task *model.Task) map[string]interface{} {
	other := make(map[string]interface{})
	if bc := task.PrivateData.BillingContext; bc != nil {
		other["model_price"] = bc.ModelPrice
		if bc.ModelRatio > 0 {
			other["model_ratio"] = bc.ModelRatio
		}
		other["group_ratio"] = bc.GroupRatio
		if priceData := taskBillingContextPriceData(bc); priceData != nil {
			for k, v := range priceData.OtherRatios() {
				other[k] = v
			}
		}
	}
	props := task.Properties
	if props.UpstreamModelName != "" && props.UpstreamModelName != props.OriginModelName {
		other["is_model_mapped"] = true
		other["upstream_model_name"] = props.UpstreamModelName
	}
	return other
}

func taskBillingContextPriceData(bc *model.TaskBillingContext) *types.PriceData {
	if bc == nil || len(bc.OtherRatios) == 0 {
		return nil
	}
	priceData := &types.PriceData{}
	if !priceData.ReplaceOtherRatios(bc.OtherRatios) {
		return nil
	}
	return priceData
}

// taskModelName 从 BillingContext 或 Properties 中获取模型名称。
func taskModelName(task *model.Task) string {
	if bc := task.PrivateData.BillingContext; bc != nil && bc.OriginModelName != "" {
		return bc.OriginModelName
	}
	return task.Properties.OriginModelName
}

// taskRefundReference is persisted only as an opaque refund identity. Prefer the
// task primary key so retries across task ID formats resolve to the same claim.
func taskRefundReference(task *model.Task) string {
	identity := fmt.Sprintf("task:%d", task.ID)
	if task.ID <= 0 {
		identity = fmt.Sprintf("fallback:user:%d:channel:%d:task:%s", task.UserId, task.ChannelId, task.TaskID)
	}
	return fmt.Sprintf("task-refund:v1:%x", common.Sha256Raw([]byte(identity)))
}

func taskRecalculationReference(task *model.Task, preConsumedQuota int, actualQuota int) string {
	identity := fmt.Sprintf("task:%d:recalculate:%d:%d", task.ID, preConsumedQuota, actualQuota)
	if task.ID <= 0 {
		identity = fmt.Sprintf(
			"fallback:user:%d:channel:%d:task:%s:recalculate:%d:%d",
			task.UserId,
			task.ChannelId,
			task.TaskID,
			preConsumedQuota,
			actualQuota,
		)
	}
	return fmt.Sprintf("task-recalculate:v1:%x", common.Sha256Raw([]byte(identity)))
}

func taskRefundFundingSource(task *model.Task) string {
	if taskIsSubscription(task) {
		return BillingSourceSubscription
	}
	return BillingSourceWallet
}

// refundTaskQuotaTx claims, refunds, and enqueues the financial event in one
// main-database transaction. When transitionFrom is present, it also applies
// the task status CAS in that transaction so an outbox failure leaves the task
// retryable. It returns whether a refund was newly applied and whether the
// optional task transition won its CAS.
func refundTaskQuotaTx(ctx context.Context, task *model.Task, quota int, transitionFrom *model.TaskStatus) (refunded bool, transitioned bool, err error) {
	if model.DB == nil {
		return false, false, fmt.Errorf("task refund database is not initialized")
	}

	claimKey := taskRefundReference(task)
	fundingSource := taskRefundFundingSource(task)
	claimed := false
	walletRefund := 0
	tokenRefund := 0
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if transitionFrom != nil {
			result := tx.Model(task).Where("status = ?", *transitionFrom).Select("*").Updates(task)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return nil
			}
			transitioned = true
		}
		if quota == 0 {
			return nil
		}

		var err error
		claimed, err = model.ClaimBillingRefundTx(tx, claimKey)
		if err != nil {
			return err
		}
		if !claimed {
			return nil
		}

		if taskIsSubscription(task) {
			if err := model.PostConsumeUserSubscriptionDeltaTx(tx, task.PrivateData.SubscriptionId, -int64(quota)); err != nil {
				return err
			}
		} else {
			if err := model.IncreaseUserQuotaTx(tx, task.UserId, quota); err != nil {
				return err
			}
			walletRefund = quota
		}

		if task.PrivateData.TokenId > 0 {
			if err := model.IncreaseTokenQuotaTx(tx, task.PrivateData.TokenId, quota); err != nil {
				return err
			}
			tokenRefund = quota
		}

		return model.RecordAetherFinancialEventTx(tx, model.AetherFinancialEventInput{
			UserID:          task.UserId,
			SourceType:      "usage_refund",
			SourceID:        claimKey,
			DedupeKeyID:     claimKey,
			QuotaDelta:      quota,
			PaymentCategory: fundingSource,
			OccurredAt:      common.GetTimestamp(),
		})
	})
	if err != nil {
		return false, false, err
	}
	if transitionFrom != nil && !transitioned {
		return false, false, nil
	}
	if !claimed {
		return false, transitioned, nil
	}

	if walletRefund > 0 {
		model.IncreaseUserQuotaCache(task.UserId, walletRefund)
	}
	updateTaskTokenQuotaCache(ctx, task, -tokenRefund)
	return true, transitioned, nil
}

func recordTaskRefundAuditLog(ctx context.Context, task *model.Task, reason string) {
	other := taskBillingOther(task)
	other["task_id"] = task.TaskID
	other["reason"] = reason
	if err := model.RecordTaskBillingAuditLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   model.LogTypeRefund,
		Content:   "",
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     task.Quota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
	}); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("记录任务退款审计日志失败 task %s: %s", task.TaskID, err.Error()))
	}
}

// transitionTaskFailureWithRefund atomically makes a task terminal and refunds
// its reservation. A failed financial outbox write rolls back both mutations,
// leaving the previous task status eligible for a subsequent polling retry.
func transitionTaskFailureWithRefund(ctx context.Context, task *model.Task, fromStatus model.TaskStatus, reason string) (bool, error) {
	refunded, transitioned, err := refundTaskQuotaTx(ctx, task, task.Quota, &fromStatus)
	if err != nil {
		return false, err
	}
	if refunded {
		recordTaskRefundAuditLog(ctx, task, reason)
	}
	return transitioned, nil
}

// RefundTaskQuota 统一的任务失败退款逻辑。
// 当异步任务失败时，将预扣的 quota 退还给用户（支持钱包和订阅），并退还令牌额度。
func RefundTaskQuota(ctx context.Context, task *model.Task, reason string) {
	quota := task.Quota
	if quota == 0 {
		return
	}

	refunded, _, err := refundTaskQuotaTx(ctx, task, quota, nil)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("退还任务额度失败 task %s: %s", task.TaskID, err.Error()))
		return
	}
	if !refunded {
		return
	}

	recordTaskRefundAuditLog(ctx, task, reason)
}

type taskQuotaAdjustment struct {
	preConsumedQuota int
	actualQuota      int
	quotaDelta       int
	logParams        model.RecordTaskBillingLogParams
}

func validateTaskActualQuota(actualQuota int) error {
	if actualQuota < 0 {
		return fmt.Errorf("task actual quota cannot be negative: %d", actualQuota)
	}
	return nil
}

func prepareTaskQuotaAdjustment(task *model.Task, actualQuota int, reason string, clamps ...*common.QuotaClamp) (*taskQuotaAdjustment, error) {
	if err := validateTaskActualQuota(actualQuota); err != nil {
		return nil, err
	}
	preConsumedQuota := task.Quota
	quotaDelta := actualQuota - preConsumedQuota
	if quotaDelta == 0 {
		return nil, nil
	}

	logType := model.LogTypeRefund
	logQuota := -quotaDelta
	if quotaDelta > 0 {
		logType = model.LogTypeConsume
		logQuota = quotaDelta
	}
	other := taskBillingOther(task)
	other["task_id"] = task.TaskID
	other["pre_consumed_quota"] = preConsumedQuota
	other["actual_quota"] = actualQuota
	for _, clamp := range clamps {
		attachQuotaSaturationToOther(other, clamp)
	}
	recalculationReference := taskRecalculationReference(task, preConsumedQuota, actualQuota)
	return &taskQuotaAdjustment{
		preConsumedQuota: preConsumedQuota,
		actualQuota:      actualQuota,
		quotaDelta:       quotaDelta,
		logParams: model.RecordTaskBillingLogParams{
			UserId:      task.UserId,
			LogType:     logType,
			SourceID:    recalculationReference,
			DedupeKeyID: recalculationReference,
			Content:     reason,
			ChannelId:   task.ChannelId,
			ModelName:   taskModelName(task),
			Quota:       logQuota,
			TokenId:     task.PrivateData.TokenId,
			Group:       task.Group,
			Other:       other,
			NodeName:    task.PrivateData.NodeName,
		},
	}, nil
}

func applyTaskQuotaAdjustmentTx(tx *gorm.DB, task *model.Task, adjustment *taskQuotaAdjustment) error {
	if adjustment == nil {
		return nil
	}
	if err := taskAdjustFundingTx(tx, task, adjustment.quotaDelta); err != nil {
		return err
	}
	if err := taskAdjustTokenQuotaTx(tx, task, adjustment.quotaDelta); err != nil {
		return err
	}
	if err := updateTaskQuotaTx(tx, task, adjustment.preConsumedQuota, adjustment.actualQuota); err != nil {
		return err
	}
	return model.RecordTaskBillingFinancialEventTx(tx, adjustment.logParams)
}

func finishTaskQuotaAdjustment(ctx context.Context, task *model.Task, adjustment *taskQuotaAdjustment) {
	if adjustment == nil {
		return
	}
	task.Quota = adjustment.actualQuota
	updateTaskQuotaCaches(ctx, task, adjustment.quotaDelta)
	if adjustment.quotaDelta > 0 {
		model.UpdateUserUsedQuotaAndRequestCount(task.UserId, adjustment.quotaDelta)
		model.UpdateChannelUsedQuota(task.ChannelId, adjustment.quotaDelta)
	}
	if err := model.RecordTaskBillingAuditLog(adjustment.logParams); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("记录任务结算审计日志失败 task %s: %s", task.TaskID, err.Error()))
	}
}

func applyTaskQuotaAdjustment(ctx context.Context, task *model.Task, adjustment *taskQuotaAdjustment) error {
	if adjustment == nil {
		return nil
	}
	logger.LogInfo(ctx, fmt.Sprintf("任务 %s 差额结算：delta=%s（实际：%s，预扣：%s，%s）", task.TaskID, logger.LogQuota(adjustment.quotaDelta), logger.LogQuota(adjustment.actualQuota), logger.LogQuota(adjustment.preConsumedQuota), adjustment.logParams.Content))
	if model.DB == nil {
		return fmt.Errorf("task recalculation database is not initialized")
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		return applyTaskQuotaAdjustmentTx(tx, task, adjustment)
	}); err != nil {
		return fmt.Errorf("recalculate task quota %s: %w", task.TaskID, err)
	}
	finishTaskQuotaAdjustment(ctx, task, adjustment)
	return nil
}

func taskQuotaByTokens(task *model.Task, totalTokens int) (actualQuota int, clamp *common.QuotaClamp, reason string, shouldRecalculate bool) {
	if totalTokens <= 0 {
		return 0, nil, "", false
	}
	modelName := taskModelName(task)
	modelRatio, hasRatioSetting, _ := ratio_setting.GetModelRatio(modelName)
	if !hasRatioSetting || modelRatio <= 0 {
		return 0, nil, "", false
	}
	group := task.Group
	if group == "" {
		user, err := model.GetUserById(task.UserId, false)
		if err == nil {
			group = user.Group
		}
	}
	if group == "" {
		return 0, nil, "", false
	}
	groupRatio := ratio_setting.GetGroupRatio(group)
	userGroupRatio, hasUserGroupRatio := ratio_setting.GetGroupGroupRatio(group, group)
	finalGroupRatio := groupRatio
	if hasUserGroupRatio {
		finalGroupRatio = userGroupRatio
	}
	otherMultiplier := 1.0
	if priceData := taskBillingContextPriceData(task.PrivateData.BillingContext); priceData != nil {
		otherMultiplier = priceData.OtherRatioMultiplier()
	}
	actualQuota, clamp = common.QuotaFromFloatChecked(float64(totalTokens) * modelRatio * finalGroupRatio * otherMultiplier)
	reason = fmt.Sprintf("token重算：tokens=%d, modelRatio=%.2f, groupRatio=%.2f, otherMultiplier=%.4f", totalTokens, modelRatio, finalGroupRatio, otherMultiplier)
	return actualQuota, clamp, reason, true
}

func taskCompletionKnownActualQuota(adaptor TaskPollingAdaptor, task *model.Task, taskResult *relaycommon.TaskInfo) (actualQuota int, known bool) {
	provider, ok := adaptor.(TaskCompletionActualQuotaProvider)
	if !ok {
		return 0, false
	}
	return provider.ActualQuotaOnComplete(task, taskResult)
}

func taskCompletionActualQuota(adaptor TaskPollingAdaptor, task *model.Task, taskResult *relaycommon.TaskInfo) (actualQuota int, known bool) {
	if actualQuota, known = taskCompletionKnownActualQuota(adaptor, task, taskResult); known {
		return actualQuota, true
	}
	if actualQuota = adaptor.AdjustBillingOnComplete(task, taskResult); actualQuota > 0 {
		return actualQuota, true
	}
	return 0, false
}

func taskCompletionQuotaAdjustment(adaptor TaskPollingAdaptor, task *model.Task, taskResult *relaycommon.TaskInfo) (*taskQuotaAdjustment, error) {
	if bc := task.PrivateData.BillingContext; bc != nil && bc.PerCallBilling {
		if actualQuota, known := taskCompletionKnownActualQuota(adaptor, task, taskResult); known {
			if err := validateTaskActualQuota(actualQuota); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	if actualQuota, known := taskCompletionActualQuota(adaptor, task, taskResult); known {
		return prepareTaskQuotaAdjustment(task, actualQuota, "adaptor计费调整")
	}
	actualQuota, clamp, reason, shouldRecalculate := taskQuotaByTokens(task, taskResult.TotalTokens)
	if !shouldRecalculate {
		return nil, nil
	}
	return prepareTaskQuotaAdjustment(task, actualQuota, reason, clamp)
}

// RecalculateTaskQuota 通用的异步差额结算。
// actualQuota 是任务完成后的实际应扣额度，与预扣额度 (task.Quota) 做差额结算。
// reason 用于日志记录（例如 "token重算" 或 "adaptor调整"）。
// clamps 可选：若计算 actualQuota 时发生额度饱和，将其记入日志 admin_info（仅管理员可见）。
func RecalculateTaskQuota(ctx context.Context, task *model.Task, actualQuota int, reason string, clamps ...*common.QuotaClamp) error {
	adjustment, err := prepareTaskQuotaAdjustment(task, actualQuota, reason, clamps...)
	if err != nil {
		return err
	}
	if adjustment == nil {
		logger.LogInfo(ctx, fmt.Sprintf("任务 %s 预扣费准确（%s，%s）", task.TaskID, logger.LogQuota(actualQuota), reason))
		return nil
	}
	return applyTaskQuotaAdjustment(ctx, task, adjustment)
}

// RecalculateTaskQuotaByTokens 根据实际 token 消耗重新计费（异步差额结算）。
// 当任务成功且返回了 totalTokens 时，根据模型倍率和分组倍率重新计算实际扣费额度，
// 与预扣费的差额进行补扣或退还。支持钱包和订阅计费来源。
func RecalculateTaskQuotaByTokens(ctx context.Context, task *model.Task, totalTokens int) error {
	actualQuota, clamp, reason, shouldRecalculate := taskQuotaByTokens(task, totalTokens)
	if !shouldRecalculate {
		return nil
	}
	return RecalculateTaskQuota(ctx, task, actualQuota, reason, clamp)
}

func transitionTaskSuccessWithSettlement(ctx context.Context, adaptor TaskPollingAdaptor, task *model.Task, fromStatus model.TaskStatus, taskResult *relaycommon.TaskInfo) (bool, error) {
	adjustment, err := taskCompletionQuotaAdjustment(adaptor, task, taskResult)
	if err != nil {
		return false, err
	}
	if model.DB == nil {
		return false, fmt.Errorf("task success database is not initialized")
	}
	transitioned := false
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(task).Where("status = ?", fromStatus).Select("*").Updates(task)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		transitioned = true
		return applyTaskQuotaAdjustmentTx(tx, task, adjustment)
	}); err != nil {
		return false, err
	}
	if !transitioned {
		return false, nil
	}
	finishTaskQuotaAdjustment(ctx, task, adjustment)
	return true, nil
}
