package service

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestCalcOpenRouterCacheCreateTokensSaturatesUnboundedUpstreamCost(t *testing.T) {
	usage := dto.Usage{Cost: math.MaxFloat64}
	priceData := types.PriceData{
		ModelRatio:         1,
		CompletionRatio:    1,
		CacheRatio:         0.1,
		CacheCreationRatio: 1.25,
	}

	require.Equal(t, common.MaxQuota, CalcOpenRouterCacheCreateTokens(usage, priceData))
}
