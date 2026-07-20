package common

import (
	"testing"

	appcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAetherChannelUsesOpenAICompatibilityAndStreamOptions(t *testing.T) {
	apiType, ok := appcommon.ChannelType2APIType(constant.ChannelTypeAether)

	require.True(t, ok)
	assert.Equal(t, constant.APITypeOpenAI, apiType)
	assert.True(t, streamSupportedChannels[constant.ChannelTypeAether])
}
