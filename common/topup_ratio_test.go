package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateTopupGroupRatioRejectsNonPositiveValuesWithoutReplacingExistingSettings(t *testing.T) {
	previous := TopupGroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, UpdateTopupGroupRatioByJSONString(previous))
	})

	require.NoError(t, UpdateTopupGroupRatioByJSONString(`{"pricing-validation-test":1.2}`))
	require.Error(t, UpdateTopupGroupRatioByJSONString(`{"pricing-validation-test":0}`))

	require.Equal(t, 1.2, GetTopupGroupRatio("pricing-validation-test"))
}
