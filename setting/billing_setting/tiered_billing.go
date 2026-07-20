package billing_setting

import (
	"fmt"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/samber/lo"
)

const (
	BillingModeRatio      = "ratio"
	BillingModeTieredExpr = "tiered_expr"
	BillingModeField      = "billing_mode"
	BillingExprField      = "billing_expr"
)

// BillingSetting is managed by config.GlobalConfig.Register.
// DB keys: billing_setting.billing_mode, billing_setting.billing_expr
type BillingSetting struct {
	BillingMode map[string]string `json:"billing_mode"`
	BillingExpr map[string]string `json:"billing_expr"`
}

var billingSetting = BillingSetting{
	BillingMode: make(map[string]string),
	BillingExpr: make(map[string]string),
}

func init() {
	config.GlobalConfig.Register("billing_setting", &billingSetting)
}

// ---------------------------------------------------------------------------
// Read accessors (hot path, must be fast)
// ---------------------------------------------------------------------------

func GetBillingMode(model string) string {
	mode := BillingModeRatio
	ratio_setting.ReadPricingSettings(func() {
		if configuredMode, ok := billingSetting.BillingMode[model]; ok {
			mode = configuredMode
		}
	})
	return mode
}

func GetBillingExpr(model string) (string, bool) {
	var expr string
	var ok bool
	ratio_setting.ReadPricingSettings(func() {
		expr, ok = billingSetting.BillingExpr[model]
	})
	return expr, ok
}

func GetBillingModeCopy() map[string]string {
	var modes map[string]string
	ratio_setting.ReadPricingSettings(func() {
		modes = lo.Assign(billingSetting.BillingMode)
	})
	return modes
}

func GetBillingExprCopy() map[string]string {
	var expressions map[string]string
	ratio_setting.ReadPricingSettings(func() {
		expressions = lo.Assign(billingSetting.BillingExpr)
	})
	return expressions
}

func GetPricingSnapshot() ratio_setting.PricingSnapshot {
	return ratio_setting.GetPricingSnapshot(func() (map[string]string, map[string]string) {
		return lo.Assign(billingSetting.BillingMode), lo.Assign(billingSetting.BillingExpr)
	})
}

func UpdatePricingSettings(values map[string]string) error {
	return ratio_setting.UpdatePricingSettings(func() error {
		return config.UpdateConfigFromMap(&billingSetting, values)
	})
}

func GetPricingSyncData(base map[string]any) map[string]any {
	extra := make(map[string]any, 2)
	if modes := GetBillingModeCopy(); len(modes) > 0 {
		extra[BillingModeField] = modes
	}
	if exprs := GetBillingExprCopy(); len(exprs) > 0 {
		extra[BillingExprField] = exprs
	}
	return lo.Assign(base, extra)
}

// ---------------------------------------------------------------------------
// Smoke test (called externally for validation before save)
// ---------------------------------------------------------------------------

func SmokeTestExpr(exprStr string) error {
	return smokeTestExpr(exprStr)
}

func smokeTestExpr(exprStr string) error {
	vectors := []billingexpr.TokenParams{
		{P: 0, C: 0, Len: 0},
		{P: 1000, C: 1000, Len: 1000},
		{P: 100000, C: 100000, Len: 100000},
		{P: 1000000, C: 1000000, Len: 1000000},
	}
	requests := []billingexpr.RequestInput{
		{},
		{
			Headers: map[string]string{
				"anthropic-beta": "fast-mode-2026-02-01",
			},
			Body: []byte(`{"service_tier":"fast","stream_options":{"include_usage":true},"messages":[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21]}`),
		},
	}

	for _, v := range vectors {
		for _, request := range requests {
			result, _, err := billingexpr.RunExprWithRequest(exprStr, v, request)
			if err != nil {
				return fmt.Errorf("vector {p=%g, c=%g}: run failed: %w", v.P, v.C, err)
			}
			if result < 0 {
				return fmt.Errorf("vector {p=%g, c=%g}: result %f < 0", v.P, v.C, result)
			}
		}
	}
	return nil
}
