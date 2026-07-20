package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertUserForPaymentGuardTest(t *testing.T, id int, quota int) {
	t.Helper()
	user := &User{
		Id:       id,
		Username: "payment_guard_user",
		Status:   common.UserStatusEnabled,
		Quota:    quota,
	}
	require.NoError(t, DB.Create(user).Error)
}

func insertSubscriptionPlanForPaymentGuardTest(t *testing.T, id int) *SubscriptionPlan {
	t.Helper()
	plan := &SubscriptionPlan{
		Id:            id,
		Title:         "Guard Plan",
		PriceAmount:   9.99,
		Currency:      "USD",
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		Enabled:       true,
		TotalAmount:   1000,
	}
	require.NoError(t, DB.Create(plan).Error)
	return plan
}

func insertSubscriptionOrderForPaymentGuardTest(t *testing.T, tradeNo string, userID int, planID int, paymentProvider string) {
	t.Helper()
	order := &SubscriptionOrder{
		UserId:          userID,
		PlanId:          planID,
		Money:           9.99,
		TradeNo:         tradeNo,
		PaymentMethod:   paymentProvider,
		PaymentProvider: paymentProvider,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, order.Insert())
}

func insertTopUpForPaymentGuardTest(t *testing.T, tradeNo string, userID int, paymentProvider string) {
	t.Helper()
	topUp := &TopUp{
		UserId:          userID,
		Amount:          2,
		Money:           9.99,
		TradeNo:         tradeNo,
		PaymentMethod:   paymentProvider,
		PaymentProvider: paymentProvider,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, topUp.Insert())
}

func getTopUpStatusForPaymentGuardTest(t *testing.T, tradeNo string) string {
	t.Helper()
	topUp := GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, topUp)
	return topUp.Status
}

func countUserSubscriptionsForPaymentGuardTest(t *testing.T, userID int) int64 {
	t.Helper()
	var count int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", userID).Count(&count).Error)
	return count
}

func getUserQuotaForPaymentGuardTest(t *testing.T, userID int) int {
	t.Helper()
	var user User
	require.NoError(t, DB.Select("quota").Where("id = ?", userID).First(&user).Error)
	return user.Quota
}

func TestRechargeWaffoPancake_RejectsMismatchedPaymentMethod(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 101, 0)
	insertTopUpForPaymentGuardTest(t, "waffo-pancake-guard", 101, PaymentProviderStripe)

	err := RechargeWaffoPancake("waffo-pancake-guard")
	require.Error(t, err)

	topUp := GetTopUpByTradeNo("waffo-pancake-guard")
	require.NotNil(t, topUp)
	assert.Equal(t, common.TopUpStatusPending, topUp.Status)
	assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, 101))
}

func TestTopUpFinalizationRejectsQuotaOverflow(t *testing.T) {
	quotaPerUnit := int64(common.QuotaPerUnit)
	require.Positive(t, quotaPerUnit)
	overflowAmount := int64(common.MaxQuota)/quotaPerUnit + 1

	testCases := []struct {
		name     string
		provider string
		complete func(tradeNo string) error
		topUp    func(tradeNo string) *TopUp
	}{
		{
			name:     "stripe",
			provider: PaymentProviderStripe,
			complete: func(tradeNo string) error { return Recharge(tradeNo, "", "") },
			topUp: func(tradeNo string) *TopUp {
				return &TopUp{Amount: 1, Money: float64(overflowAmount), TradeNo: tradeNo}
			},
		},
		{
			name:     "epay manual completion",
			provider: PaymentProviderEpay,
			complete: func(tradeNo string) error { return ManualCompleteTopUp(tradeNo, "") },
			topUp: func(tradeNo string) *TopUp {
				return &TopUp{Amount: overflowAmount, Money: 1, TradeNo: tradeNo}
			},
		},
		{
			name:     "creem",
			provider: PaymentProviderCreem,
			complete: func(tradeNo string) error { return RechargeCreem(tradeNo, "", "", "") },
			topUp: func(tradeNo string) *TopUp {
				return &TopUp{Amount: int64(common.MaxQuota) + 1, Money: 1, TradeNo: tradeNo}
			},
		},
		{
			name:     "waffo",
			provider: PaymentProviderWaffo,
			complete: func(tradeNo string) error { return RechargeWaffo(tradeNo, "") },
			topUp: func(tradeNo string) *TopUp {
				return &TopUp{Amount: overflowAmount, Money: 1, TradeNo: tradeNo}
			},
		},
		{
			name:     "waffo pancake",
			provider: PaymentProviderWaffoPancake,
			complete: func(tradeNo string) error { return RechargeWaffoPancake(tradeNo) },
			topUp: func(tradeNo string) *TopUp {
				return &TopUp{Amount: overflowAmount, Money: 1, TradeNo: tradeNo}
			},
		},
	}

	for index, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			userID := 500 + index
			insertUserForPaymentGuardTest(t, userID, 0)

			tradeNo := "topup-overflow-" + tc.name
			topUp := tc.topUp(tradeNo)
			topUp.UserId = userID
			topUp.PaymentMethod = tc.provider
			topUp.PaymentProvider = tc.provider
			topUp.Status = common.TopUpStatusPending
			topUp.CreateTime = time.Now().Unix()
			require.NoError(t, topUp.Insert())

			require.Error(t, tc.complete(tradeNo))
			assert.Equal(t, common.TopUpStatusPending, getTopUpStatusForPaymentGuardTest(t, tradeNo))
			assert.Zero(t, getUserQuotaForPaymentGuardTest(t, userID))
		})
	}
}

func TestRechargeWaffoPancakeRejectsUserQuotaOverflow(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 601, common.MaxQuota)
	topUp := &TopUp{
		UserId:          601,
		Amount:          1,
		Money:           1,
		TradeNo:         "topup-user-overflow",
		PaymentMethod:   PaymentMethodWaffoPancake,
		PaymentProvider: PaymentProviderWaffoPancake,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, topUp.Insert())

	require.Error(t, RechargeWaffoPancake(topUp.TradeNo))
	assert.Equal(t, common.TopUpStatusPending, getTopUpStatusForPaymentGuardTest(t, topUp.TradeNo))
	assert.Equal(t, common.MaxQuota, getUserQuotaForPaymentGuardTest(t, 601))
}

func TestIncreaseUserQuotaRejectsUserQuotaOverflow(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 602, common.MaxQuota)

	require.Error(t, IncreaseUserQuota(602, 1, true))
	assert.Equal(t, common.MaxQuota, getUserQuotaForPaymentGuardTest(t, 602))
}

func TestUpdatePendingTopUpStatus_RejectsMismatchedPaymentProvider(t *testing.T) {
	testCases := []struct {
		name                    string
		tradeNo                 string
		storedPaymentProvider   string
		expectedPaymentProvider string
		targetStatus            string
	}{
		{
			name:                    "stripe expire",
			tradeNo:                 "stripe-expire-guard",
			storedPaymentProvider:   PaymentProviderCreem,
			expectedPaymentProvider: PaymentProviderStripe,
			targetStatus:            common.TopUpStatusExpired,
		},
		{
			name:                    "waffo failed",
			tradeNo:                 "waffo-failed-guard",
			storedPaymentProvider:   PaymentProviderStripe,
			expectedPaymentProvider: PaymentProviderWaffo,
			targetStatus:            common.TopUpStatusFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			insertUserForPaymentGuardTest(t, 150, 0)
			insertTopUpForPaymentGuardTest(t, tc.tradeNo, 150, tc.storedPaymentProvider)

			err := UpdatePendingTopUpStatus(tc.tradeNo, tc.expectedPaymentProvider, tc.targetStatus)
			require.ErrorIs(t, err, ErrPaymentMethodMismatch)
			assert.Equal(t, common.TopUpStatusPending, getTopUpStatusForPaymentGuardTest(t, tc.tradeNo))
		})
	}
}

func TestCompleteSubscriptionOrder_RejectsMismatchedPaymentProvider(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 202, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 301)
	insertSubscriptionOrderForPaymentGuardTest(t, "sub-guard-order", 202, plan.Id, PaymentProviderStripe)

	err := CompleteSubscriptionOrder("sub-guard-order", `{"provider":"epay"}`, PaymentProviderEpay, "alipay")
	require.ErrorIs(t, err, ErrPaymentMethodMismatch)

	order := GetSubscriptionOrderByTradeNo("sub-guard-order")
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
	assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, 202))

	topUp := GetTopUpByTradeNo("sub-guard-order")
	assert.Nil(t, topUp)
}

func TestExpireSubscriptionOrder_RejectsMismatchedPaymentProvider(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 303, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 401)
	insertSubscriptionOrderForPaymentGuardTest(t, "sub-expire-guard", 303, plan.Id, PaymentProviderStripe)

	err := ExpireSubscriptionOrder("sub-expire-guard", PaymentProviderCreem)
	require.ErrorIs(t, err, ErrPaymentMethodMismatch)

	order := GetSubscriptionOrderByTradeNo("sub-expire-guard")
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
}
