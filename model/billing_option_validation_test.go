package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateBillingOptionRejectsUnsafePricingSettings(t *testing.T) {
	testCases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "non-finite quota unit", key: "QuotaPerUnit", value: "NaN"},
		{name: "negative payment price", key: "Price", value: "-1"},
		{name: "zero Stripe unit price", key: "StripeUnitPrice", value: "0"},
		{name: "fractional preset amount", key: "payment_setting.amount_options", value: `[10, 20.5]`},
		{name: "negative preset amount", key: "payment_setting.amount_options", value: `[-10]`},
		{name: "discount over one", key: "payment_setting.amount_discount", value: `{"100":1.1}`},
		{name: "non-positive discount", key: "payment_setting.amount_discount", value: `{"100":0}`},
		{name: "negative tool price", key: "tool_price_setting.prices", value: `{"web_search":-1}`},
		{name: "negative model ratio", key: "ModelRatio", value: `{"gpt-test":-1}`},
		{name: "negative group ratio", key: "GroupRatio", value: `{"default":-1}`},
		{name: "negative group override", key: "GroupGroupRatio", value: `{"vip":{"default":-1}}`},
		{name: "non-positive topup group ratio", key: "TopupGroupRatio", value: `{"default":0}`},
		{name: "non-finite Claude thinking percentage", key: "claude.thinking_adapter_budget_tokens_percentage", value: "NaN"},
		{name: "Gemini thinking percentage over one", key: "gemini.thinking_adapter_budget_tokens_percentage", value: "1.1"},
		{name: "negative Grok violation fee", key: "grok.violation_deduction_amount", value: "-1"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, validateBillingOption(tc.key, tc.value))
		})
	}
}

func TestValidateBillingOptionAcceptsValidPricingSettings(t *testing.T) {
	testCases := []struct {
		key   string
		value string
	}{
		{key: "QuotaPerUnit", value: "500000"},
		{key: "Price", value: "1.25"},
		{key: "payment_setting.amount_options", value: `[10,20,50]`},
		{key: "payment_setting.amount_discount", value: `{"100":0.9}`},
		{key: "tool_price_setting.prices", value: `{"web_search":10,"file_search":0}`},
	}

	for _, tc := range testCases {
		t.Run(tc.key, func(t *testing.T) {
			require.NoError(t, validateBillingOption(tc.key, tc.value))
		})
	}
}
