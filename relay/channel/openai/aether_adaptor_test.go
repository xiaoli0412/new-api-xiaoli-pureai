package openai

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAetherChannelPreservesStreamOptions(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	request := &dto.GeneralOpenAIRequest{
		Model:         "test-model",
		StreamOptions: &dto.StreamOptions{IncludeUsage: true},
	}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType: constant.ChannelTypeAether,
	}}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(ctx, info, request)

	require.NoError(t, err)
	require.Same(t, request, converted)
	require.NotNil(t, request.StreamOptions)
}
