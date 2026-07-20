package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestCalcSubscriptionBalanceQuotaRejectsOverflow(t *testing.T) {
	quota, err := calcSubscriptionBalanceQuota(float64(common.MaxQuota))

	require.Error(t, err)
	require.Zero(t, quota)
}
