package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type billingFundingReceipt struct {
	source         string
	subscriptionID int
	consumed       int
	preConsumed    int
	trusted        bool
}

func newBillingSessionFromRequestState(c *gin.Context, relayInfo *relaycommon.RelayInfo, requestedQuota int) (*BillingSession, *types.NewAPIError) {
	if relayInfo == nil {
		return nil, types.NewError(fmt.Errorf("relayInfo is nil"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if requestedQuota < 0 {
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("pre-consume quota cannot be negative: %d", requestedQuota),
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if model.DB == nil {
		return nil, types.NewError(errors.New("billing database is not initialized"), types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}

	requestKey, err := model.BillingRequestStateKey(relayInfo.UserId, relayInfo.TokenId, relayInfo.RequestId)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	pendingFundingKey, err := model.BillingFundingStateKey(relayInfo.UserId, relayInfo.TokenId, "pending", relayInfo.RequestId)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	var state model.BillingRequestState
	created := false
	var walletDebited int
	var tokenDebited int
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		candidate := model.BillingRequestState{
			RequestKey: requestKey,
			FundingKey: pendingFundingKey,
			ClaimToken: common.NewRequestId(),
			UserID:     relayInfo.UserId,
			TokenID:    relayInfo.TokenId,
			State:      model.BillingRequestStatePreconsumed,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
			return err
		}
		if err := billingStateLock(tx).Where("request_key = ?", requestKey).First(&state).Error; err != nil {
			return err
		}
		if state.ClaimToken != candidate.ClaimToken {
			return validateExistingBillingRequestState(&state, relayInfo, requestedQuota)
		}
		created = true

		receipt, err := preConsumeBillingFundingTx(tx, c, relayInfo, requestKey, requestedQuota)
		if err != nil {
			return err
		}
		fundingKey, err := model.BillingFundingStateKey(relayInfo.UserId, relayInfo.TokenId, receipt.source, relayInfo.RequestId)
		if err != nil {
			return err
		}
		tokenConsumed := 0
		if !relayInfo.IsPlayground && !relayInfo.TokenUnlimited && receipt.preConsumed > 0 {
			if err := model.DecreaseTokenQuotaTx(tx, relayInfo.TokenId, receipt.preConsumed); err != nil {
				return err
			}
			tokenConsumed = receipt.preConsumed
		}

		state.FundingSource = receipt.source
		state.FundingKey = fundingKey
		state.SubscriptionID = receipt.subscriptionID
		state.RequestedQuota = requestedQuota
		state.PreConsumedQuota = receipt.preConsumed
		state.FundingConsumedQuota = receipt.consumed
		state.TokenConsumedQuota = tokenConsumed
		state.Trusted = receipt.trusted
		state.State = model.BillingRequestStatePreconsumed
		if err := tx.Save(&state).Error; err != nil {
			return err
		}
		if receipt.source == BillingSourceWallet {
			walletDebited = receipt.consumed
		}
		tokenDebited = tokenConsumed
		return nil
	})
	if err != nil {
		return nil, billingRequestStateAPIError(err)
	}

	if created {
		if walletDebited > 0 {
			model.DecreaseUserQuotaCache(relayInfo.UserId, walletDebited)
		}
		if tokenDebited > 0 {
			model.DecreaseTokenQuotaCache(relayInfo.TokenKey, tokenDebited)
		}
	}
	return hydrateBillingSessionFromState(relayInfo, &state)
}

func billingStateLock(tx *gorm.DB) *gorm.DB {
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		return tx
	}
	return tx.Clauses(clause.Locking{Strength: "UPDATE"})
}

func validateExistingBillingRequestState(state *model.BillingRequestState, relayInfo *relaycommon.RelayInfo, requestedQuota int) error {
	if state == nil {
		return errors.New("billing request state is required")
	}
	if state.UserID != relayInfo.UserId || state.TokenID != relayInfo.TokenId {
		return errors.New("billing request principal does not match existing state")
	}
	if state.RequestedQuota != requestedQuota {
		return errors.New("billing request quota does not match existing state")
	}
	switch state.State {
	case model.BillingRequestStatePreconsumed, model.BillingRequestStateSettled:
		return nil
	case model.BillingRequestStateRefunded:
		return errors.New("billing request was already refunded")
	default:
		return errors.New("billing request state is invalid")
	}
}

func preConsumeBillingFundingTx(tx *gorm.DB, c *gin.Context, relayInfo *relaycommon.RelayInfo, requestKey string, requestedQuota int) (billingFundingReceipt, error) {
	tryWallet := func() (billingFundingReceipt, error) {
		var user model.User
		if err := billingStateLock(tx).Select("id", "quota").Where("id = ?", relayInfo.UserId).First(&user).Error; err != nil {
			return billingFundingReceipt{}, err
		}
		relayInfo.UserQuota = user.Quota
		trusted := billingWalletTrusted(c, relayInfo)
		consumed := requestedQuota
		if trusted {
			consumed = 0
		}
		if err := model.DecreaseUserQuotaTx(tx, relayInfo.UserId, consumed); err != nil {
			return billingFundingReceipt{}, err
		}
		return billingFundingReceipt{
			source:      BillingSourceWallet,
			consumed:    consumed,
			preConsumed: consumed,
			trusted:     trusted,
		}, nil
	}
	trySubscription := func() (billingFundingReceipt, error) {
		subConsume := int64(requestedQuota)
		if subConsume <= 0 {
			subConsume = 1
		}
		result, err := model.PreConsumeUserSubscriptionTx(tx, requestKey, relayInfo.UserId, relayInfo.OriginModelName, 0, subConsume)
		if err != nil {
			return billingFundingReceipt{}, err
		}
		return billingFundingReceipt{
			source:         BillingSourceSubscription,
			subscriptionID: result.UserSubscriptionId,
			consumed:       int(result.PreConsumed),
			preConsumed:    int(result.PreConsumed),
		}, nil
	}

	switch common.NormalizeBillingPreference(relayInfo.UserSetting.BillingPreference) {
	case "wallet_only":
		return tryWallet()
	case "subscription_only":
		return trySubscription()
	case "wallet_first":
		wallet, err := tryWallet()
		if err == nil || !billingFundingInsufficient(err) {
			return wallet, err
		}
		return trySubscription()
	case "subscription_first":
		fallthrough
	default:
		subscription, err := trySubscription()
		if err == nil {
			return subscription, nil
		}
		if !billingFundingInsufficient(err) {
			return billingFundingReceipt{}, err
		}
		if !subscriptionWalletFallbackAllowedTx(tx, relayInfo.UserId) {
			return billingFundingReceipt{}, err
		}
		return tryWallet()
	}
}

func billingWalletTrusted(c *gin.Context, relayInfo *relaycommon.RelayInfo) bool {
	if relayInfo.ForcePreConsume {
		return false
	}
	trustQuota := common.GetTrustQuota()
	if trustQuota <= 0 {
		return false
	}
	tokenTrusted := relayInfo.TokenUnlimited || c.GetInt("token_quota") > trustQuota
	return tokenTrusted && relayInfo.UserQuota > trustQuota
}

func billingFundingInsufficient(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "额度不足") ||
		strings.Contains(message, "quota insufficient") ||
		strings.Contains(message, "no active subscription")
}

func subscriptionWalletFallbackAllowedTx(tx *gorm.DB, userID int) bool {
	var strictCount int64
	err := tx.Model(&model.UserSubscription{}).
		Where("user_id = ? AND status = ? AND end_time > ? AND allow_wallet_overflow = ?", userID, "active", common.GetTimestamp(), false).
		Count(&strictCount).Error
	return err == nil && strictCount == 0
}

func hydrateBillingSessionFromState(relayInfo *relaycommon.RelayInfo, state *model.BillingRequestState) (*BillingSession, *types.NewAPIError) {
	if state == nil {
		return nil, types.NewError(errors.New("billing request state is required"), types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	var funding FundingSource
	switch state.FundingSource {
	case BillingSourceWallet:
		funding = &WalletFunding{userId: state.UserID, consumed: state.FundingConsumedQuota}
	case BillingSourceSubscription:
		funding = &SubscriptionFunding{
			requestId:      state.RequestKey,
			userId:         state.UserID,
			amount:         int64(state.FundingConsumedQuota),
			subscriptionId: state.SubscriptionID,
			preConsumed:    int64(state.FundingConsumedQuota),
		}
	default:
		return nil, types.NewError(errors.New("billing request funding source is invalid"), types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	session := &BillingSession{
		relayInfo:        relayInfo,
		funding:          funding,
		preConsumedQuota: state.PreConsumedQuota,
		tokenConsumed:    state.TokenConsumedQuota,
		extraReserved:    state.ExtraReservedQuota,
		trusted:          state.Trusted,
		fundingSettled:   state.State == model.BillingRequestStateSettled,
		settled:          state.State == model.BillingRequestStateSettled,
		refunded:         state.State == model.BillingRequestStateRefunded,
		requestKey:       state.RequestKey,
	}
	session.syncRelayInfo()
	return session, nil
}

func billingRequestStateAPIError(err error) *types.NewAPIError {
	if strings.Contains(strings.ToLower(err.Error()), "token quota") {
		return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	if billingFundingInsufficient(err) {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
}

func (s *BillingSession) reserveRequestStateLocked(targetQuota int) error {
	if s == nil || s.relayInfo == nil || s.requestKey == "" {
		return errors.New("billing request state session is invalid")
	}
	var state model.BillingRequestState
	walletDebited := 0
	tokenDebited := 0
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := billingStateLock(tx).Where("request_key = ?", s.requestKey).First(&state).Error; err != nil {
			return err
		}
		if state.UserID != s.relayInfo.UserId || state.TokenID != s.relayInfo.TokenId {
			return errors.New("billing request principal does not match existing state")
		}
		if state.State != model.BillingRequestStatePreconsumed || state.Trusted || targetQuota <= state.PreConsumedQuota {
			return nil
		}
		delta := targetQuota - state.PreConsumedQuota
		switch state.FundingSource {
		case BillingSourceWallet:
			if err := model.DecreaseUserQuotaTx(tx, state.UserID, delta); err != nil {
				return err
			}
			state.FundingConsumedQuota += delta
			walletDebited = delta
		case BillingSourceSubscription:
			if err := model.PostConsumeUserSubscriptionDeltaTx(tx, state.SubscriptionID, int64(delta)); err != nil {
				return err
			}
		default:
			return errors.New("billing request funding source is invalid")
		}
		if !s.relayInfo.IsPlayground && !s.relayInfo.TokenUnlimited {
			if err := model.DecreaseTokenQuotaTx(tx, state.TokenID, delta); err != nil {
				return err
			}
			state.TokenConsumedQuota += delta
			tokenDebited = delta
		}
		state.PreConsumedQuota += delta
		state.ExtraReservedQuota += delta
		return tx.Save(&state).Error
	})
	if err != nil {
		return billingRequestStateAPIError(err)
	}
	if walletDebited > 0 {
		model.DecreaseUserQuotaCache(s.relayInfo.UserId, walletDebited)
	}
	if tokenDebited > 0 {
		model.DecreaseTokenQuotaCache(s.relayInfo.TokenKey, tokenDebited)
	}
	applyBillingRequestStateToSessionLocked(s, &state)
	return nil
}

func applyBillingRequestStateToSessionLocked(session *BillingSession, state *model.BillingRequestState) {
	if session == nil || state == nil {
		return
	}
	session.preConsumedQuota = state.PreConsumedQuota
	session.tokenConsumed = state.TokenConsumedQuota
	session.extraReserved = state.ExtraReservedQuota
	session.trusted = state.Trusted
	session.fundingSettled = state.State == model.BillingRequestStateSettled
	session.settled = state.State == model.BillingRequestStateSettled
	session.refunded = state.State == model.BillingRequestStateRefunded
	switch funding := session.funding.(type) {
	case *WalletFunding:
		funding.consumed = state.FundingConsumedQuota
	case *SubscriptionFunding:
		funding.subscriptionId = state.SubscriptionID
		funding.preConsumed = int64(state.FundingConsumedQuota)
	}
	session.syncRelayInfo()
}

func (s *BillingSession) settleRequestStateLocked(actualQuota int) error {
	if s == nil || s.relayInfo == nil || s.requestKey == "" {
		return errors.New("billing request state session is invalid")
	}
	if actualQuota < 0 {
		return errors.New("billing actual quota cannot be negative")
	}
	var state model.BillingRequestState
	walletDelta := 0
	tokenDelta := 0
	subscriptionDelta := 0
	channelID := 0
	if s.relayInfo.ChannelMeta != nil {
		channelID = s.relayInfo.ChannelId
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := billingStateLock(tx).Where("request_key = ?", s.requestKey).First(&state).Error; err != nil {
			return err
		}
		if state.UserID != s.relayInfo.UserId || state.TokenID != s.relayInfo.TokenId {
			return errors.New("billing request principal does not match existing state")
		}
		switch state.State {
		case model.BillingRequestStateSettled:
			if state.SettledQuota != actualQuota {
				return errors.New("billing request final quota does not match settled state")
			}
			return nil
		case model.BillingRequestStateRefunded:
			return errors.New("billing request was already refunded")
		case model.BillingRequestStatePreconsumed:
		default:
			return errors.New("billing request state is invalid")
		}

		delta := actualQuota - state.PreConsumedQuota
		switch state.FundingSource {
		case BillingSourceWallet:
			if delta > 0 {
				if err := model.DecreaseUserQuotaTx(tx, state.UserID, delta); err != nil {
					return err
				}
			} else if delta < 0 {
				if err := model.IncreaseUserQuotaTx(tx, state.UserID, -delta); err != nil {
					return err
				}
			}
			walletDelta = delta
		case BillingSourceSubscription:
			if err := model.PostConsumeUserSubscriptionDeltaTx(tx, state.SubscriptionID, int64(delta)); err != nil {
				return err
			}
			subscriptionDelta = delta
		default:
			return errors.New("billing request funding source is invalid")
		}
		if !s.relayInfo.IsPlayground && !s.relayInfo.TokenUnlimited && delta != 0 {
			if delta > 0 {
				if err := model.DecreaseTokenQuotaTx(tx, state.TokenID, delta); err != nil {
					return err
				}
			} else if err := model.IncreaseTokenQuotaTx(tx, state.TokenID, -delta); err != nil {
				return err
			}
			tokenDelta = delta
		}
		state.FundingConsumedQuota += delta
		state.TokenConsumedQuota += tokenDelta
		state.SettledQuota = actualQuota
		state.State = model.BillingRequestStateSettled
		if err := tx.Save(&state).Error; err != nil {
			return err
		}
		// LogTaskConsumption may later persist its auxiliary audit record, but the
		// authoritative usage event belongs to this main-database settlement.
		return model.RecordAetherUsageEventTx(tx, &model.Log{
			UserId:    s.relayInfo.UserId,
			Type:      model.LogTypeConsume,
			CreatedAt: common.GetTimestamp(),
			ModelName: s.relayInfo.OriginModelName,
			Quota:     actualQuota,
			TokenId:   s.relayInfo.TokenId,
			Group:     s.relayInfo.UsingGroup,
			ChannelId: channelID,
			RequestId: s.relayInfo.RequestId,
		})
	})
	if err != nil {
		return billingRequestStateAPIError(err)
	}
	if walletDelta > 0 {
		model.DecreaseUserQuotaCache(s.relayInfo.UserId, walletDelta)
	} else if walletDelta < 0 {
		model.IncreaseUserQuotaCache(s.relayInfo.UserId, -walletDelta)
	}
	if tokenDelta > 0 {
		model.DecreaseTokenQuotaCache(s.relayInfo.TokenKey, tokenDelta)
	} else if tokenDelta < 0 {
		model.IncreaseTokenQuotaCache(s.relayInfo.TokenKey, -tokenDelta)
	}
	applyBillingRequestStateToSessionLocked(s, &state)
	if subscriptionDelta != 0 {
		s.relayInfo.SubscriptionPostDelta += int64(subscriptionDelta)
	}
	return nil
}

func (s *BillingSession) refundRequestState() error {
	if s == nil || s.relayInfo == nil || s.requestKey == "" {
		return errors.New("billing request state session is invalid")
	}
	var state model.BillingRequestState
	walletRefund := 0
	tokenRefund := 0
	refunded := false
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := billingStateLock(tx).Where("request_key = ?", s.requestKey).First(&state).Error; err != nil {
			return err
		}
		if state.UserID != s.relayInfo.UserId || state.TokenID != s.relayInfo.TokenId {
			return errors.New("billing request principal does not match existing state")
		}
		switch state.State {
		case model.BillingRequestStateRefunded, model.BillingRequestStateSettled:
			return nil
		case model.BillingRequestStatePreconsumed:
		default:
			return errors.New("billing request state is invalid")
		}
		switch state.FundingSource {
		case BillingSourceWallet:
			if err := model.IncreaseUserQuotaTx(tx, state.UserID, state.FundingConsumedQuota); err != nil {
				return err
			}
			walletRefund = state.FundingConsumedQuota
		case BillingSourceSubscription:
			if err := model.RefundSubscriptionPreConsumeTx(tx, state.RequestKey); err != nil {
				return err
			}
			if state.ExtraReservedQuota > 0 {
				if err := model.PostConsumeUserSubscriptionDeltaTx(tx, state.SubscriptionID, -int64(state.ExtraReservedQuota)); err != nil {
					return err
				}
			}
		default:
			return errors.New("billing request funding source is invalid")
		}
		if state.TokenConsumedQuota > 0 && !s.relayInfo.IsPlayground && !s.relayInfo.TokenUnlimited {
			if err := model.IncreaseTokenQuotaTx(tx, state.TokenID, state.TokenConsumedQuota); err != nil {
				return err
			}
			tokenRefund = state.TokenConsumedQuota
		}
		if state.PreConsumedQuota > 0 {
			if err := model.RecordAetherFinancialEventTx(tx, model.AetherFinancialEventInput{
				UserID:          state.UserID,
				SourceType:      "usage_refund",
				SourceID:        "billing:" + state.RequestKey,
				DedupeKeyID:     "billing:" + state.RequestKey,
				QuotaDelta:      state.PreConsumedQuota,
				PaymentCategory: state.FundingSource,
				OccurredAt:      common.GetTimestamp(),
			}); err != nil {
				return err
			}
		}
		state.State = model.BillingRequestStateRefunded
		if err := tx.Save(&state).Error; err != nil {
			return err
		}
		refunded = true
		return nil
	})
	if err != nil {
		return err
	}
	if walletRefund > 0 {
		model.IncreaseUserQuotaCache(s.relayInfo.UserId, walletRefund)
	}
	if tokenRefund > 0 {
		model.IncreaseTokenQuotaCache(s.relayInfo.TokenKey, tokenRefund)
	}
	if refunded {
		s.mu.Lock()
		applyBillingRequestStateToSessionLocked(s, &state)
		s.mu.Unlock()
	}
	return nil
}
