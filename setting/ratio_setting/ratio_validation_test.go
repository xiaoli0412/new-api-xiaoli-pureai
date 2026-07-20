package ratio_setting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateModelRatioRejectsNegativeValueWithoutReplacingExistingSettings(t *testing.T) {
	previous := ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, UpdateModelRatioByJSONString(previous))
	})

	require.NoError(t, UpdateModelRatioByJSONString(`{"pricing-validation-test":1.5}`))
	require.Error(t, UpdateModelRatioByJSONString(`{"pricing-validation-test":-1}`))

	ratio, ok, _ := GetModelRatio("pricing-validation-test")
	require.True(t, ok)
	require.Equal(t, 1.5, ratio)
}
