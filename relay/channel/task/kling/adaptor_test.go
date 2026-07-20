package kling

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskAdaptorCompletionActualQuota(t *testing.T) {
	adaptor := &TaskAdaptor{}
	provider, ok := any(adaptor).(service.TaskCompletionActualQuotaProvider)
	require.True(t, ok, "Kling task adaptor must expose whether final_unit_deduction is known")

	raw := func(value string) *string { return &value }
	tests := []struct {
		name               string
		finalUnitDeduction *string
		wantQuota          int
		wantKnown          bool
	}{
		{
			name:               "explicit zero string",
			finalUnitDeduction: raw(`"0"`),
			wantQuota:          0,
			wantKnown:          true,
		},
		{
			name:               "negative whole deduction",
			finalUnitDeduction: raw(`"-1"`),
			wantQuota:          -1,
			wantKnown:          true,
		},
		{
			name:               "negative fractional deduction rounds to invalid negative quota",
			finalUnitDeduction: raw(`"-0.1"`),
			wantQuota:          -1,
			wantKnown:          true,
		},
		{
			name:               "positive fractional deduction rounds up",
			finalUnitDeduction: raw(`"1.2"`),
			wantQuota:          2,
			wantKnown:          true,
		},
		{
			name:      "missing final deduction",
			wantQuota: 0,
			wantKnown: false,
		},
		{
			name:               "null final deduction",
			finalUnitDeduction: raw(`null`),
			wantQuota:          0,
			wantKnown:          false,
		},
		{
			name:               "number final deduction",
			finalUnitDeduction: raw(`0`),
			wantQuota:          0,
			wantKnown:          false,
		},
		{
			name:               "boolean final deduction",
			finalUnitDeduction: raw(`true`),
			wantQuota:          0,
			wantKnown:          false,
		},
		{
			name:               "array final deduction",
			finalUnitDeduction: raw(`[]`),
			wantQuota:          0,
			wantKnown:          false,
		},
		{
			name:               "object final deduction",
			finalUnitDeduction: raw(`{}`),
			wantQuota:          0,
			wantKnown:          false,
		},
		{
			name:               "empty final deduction",
			finalUnitDeduction: raw(`""`),
			wantQuota:          0,
			wantKnown:          false,
		},
		{
			name:               "whitespace final deduction",
			finalUnitDeduction: raw(`"  \t "`),
			wantQuota:          0,
			wantKnown:          false,
		},
		{
			name:               "invalid final deduction",
			finalUnitDeduction: raw(`"not-a-number"`),
			wantQuota:          0,
			wantKnown:          false,
		},
		{
			name:               "nan final deduction",
			finalUnitDeduction: raw(`"NaN"`),
			wantQuota:          0,
			wantKnown:          false,
		},
		{
			name:               "infinite final deduction",
			finalUnitDeduction: raw(`"Inf"`),
			wantQuota:          0,
			wantKnown:          false,
		},
		{
			name:               "range overflow final deduction",
			finalUnitDeduction: raw(`"1e309"`),
			wantQuota:          0,
			wantKnown:          false,
		},
		{
			name:               "largest in-range deduction",
			finalUnitDeduction: raw(`"2147483646"`),
			wantQuota:          common.MaxQuota - 1,
			wantKnown:          true,
		},
		{
			name:               "finite deduction saturates at quota boundary",
			finalUnitDeduction: raw(`"2147483648"`),
			wantQuota:          common.MaxQuota,
			wantKnown:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := `{"code":0,"data":{"task_id":"upstream-task","task_status":"succeed"`
			if tt.finalUnitDeduction != nil {
				response += `,"final_unit_deduction":` + *tt.finalUnitDeduction
			}
			response += `}}`
			taskResult, err := adaptor.ParseTaskResult([]byte(response))
			require.NoError(t, err)
			assert.Equal(t, model.TaskStatusSuccess, taskResult.Status)
			if tt.wantKnown {
				require.NotNil(t, taskResult.ActualQuota)
			} else {
				assert.Nil(t, taskResult.ActualQuota)
			}

			actualQuota, known := provider.ActualQuotaOnComplete(nil, taskResult)
			assert.Equal(t, tt.wantKnown, known)
			assert.Equal(t, tt.wantQuota, actualQuota)
		})
	}
}
