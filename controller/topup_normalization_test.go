package controller

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/require"
)

func TestNormalizeTopUpCreditAmountRequiresWholeQuotaUnitsForTokenDisplay(t *testing.T) {
	originalDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	originalQuotaPerUnit := common.QuotaPerUnit
	t.Cleanup(func() {
		operation_setting.GetGeneralSetting().QuotaDisplayType = originalDisplayType
		common.QuotaPerUnit = originalQuotaPerUnit
	})
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeTokens

	normalized, err := normalizeTopUpCreditAmount(int64(common.QuotaPerUnit * 3))
	require.NoError(t, err)
	require.EqualValues(t, 3, normalized)

	_, err = normalizeTopUpCreditAmount(int64(common.QuotaPerUnit*3) + 1)
	require.Error(t, err)
}

func TestNormalizeTopUpCreditAmountRejectsAmountsOutsideTheSupportedQuotaRange(t *testing.T) {
	originalDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	originalQuotaPerUnit := common.QuotaPerUnit
	t.Cleanup(func() {
		operation_setting.GetGeneralSetting().QuotaDisplayType = originalDisplayType
		common.QuotaPerUnit = originalQuotaPerUnit
	})
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeTokens
	common.QuotaPerUnit = 0.5

	_, err := normalizeTopUpCreditAmount(math.MaxInt64)

	require.Error(t, err)
}
