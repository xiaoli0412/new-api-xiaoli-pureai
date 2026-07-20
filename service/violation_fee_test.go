package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestCalcViolationFeeQuotaSaturatesOversizedFee(t *testing.T) {
	feeQuota, clamp := calcViolationFeeQuota(float64(common.MaxQuota), 2)

	require.Equal(t, common.MaxQuota, feeQuota)
	require.NotNil(t, clamp)
}
