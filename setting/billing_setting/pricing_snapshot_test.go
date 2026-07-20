package billing_setting

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBillingPricingSnapshotUsesSharedPricingState(t *testing.T) {
	originalMode, err := common.Marshal(GetBillingModeCopy())
	require.NoError(t, err)
	originalExpr, err := common.Marshal(GetBillingExprCopy())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, UpdatePricingSettings(map[string]string{
			BillingModeField: string(originalMode),
			BillingExprField: string(originalExpr),
		}))
	})

	require.NoError(t, UpdatePricingSettings(map[string]string{
		BillingModeField: `{"snapshot-tiered":"tiered_expr"}`,
		BillingExprField: `{"snapshot-tiered":"v1:tier(\"base\", p * 2 + c * 4)"}`,
	}))

	snapshot := GetPricingSnapshot()
	assert.Equal(t, BillingModeTieredExpr, snapshot.BillingMode["snapshot-tiered"])
	assert.Equal(t, `v1:tier("base", p * 2 + c * 4)`, snapshot.BillingExpr["snapshot-tiered"])
}
