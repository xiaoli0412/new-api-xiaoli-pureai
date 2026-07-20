package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadFromJsonStringKeepsExistingValuesOnInvalidJSON(t *testing.T) {
	rates := NewRWMap[string, float64]()
	rates.Set("existing", 1.25)

	err := LoadFromJsonString(rates, `{"replacement":`)
	require.Error(t, err)

	value, ok := rates.Get("existing")
	require.True(t, ok)
	require.Equal(t, 1.25, value)
	require.Equal(t, 1, rates.Len())
}
