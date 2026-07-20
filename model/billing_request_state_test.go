package model

import (
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBillingRequestStateKeyIsScopedAndOpaque(t *testing.T) {
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "billing-request-state-test-secret"
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
	})

	requestID := "req_private_billing_request_123"
	first, err := BillingRequestStateKey(42, 7, requestID)
	require.NoError(t, err)
	second, err := BillingRequestStateKey(42, 7, requestID)
	require.NoError(t, err)
	differentPrincipal, err := BillingRequestStateKey(43, 7, requestID)
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.NotEqual(t, first, differentPrincipal)
	assert.NotContains(t, first, requestID)
	assert.False(t, strings.Contains(first, "42"))
	assert.Len(t, first, 64)
}

func TestBillingRequestStateCarriesOnlyOpaqueRequestIdentity(t *testing.T) {
	typeOfState := reflect.TypeOf(BillingRequestState{})
	_, persistsRequestID := typeOfState.FieldByName("RequestID")
	_, persistsTokenKey := typeOfState.FieldByName("TokenKey")

	assert.False(t, persistsRequestID)
	assert.False(t, persistsTokenKey)
}

func TestBillingRequestStateQuotaFieldsHaveSeparatePersistenceTags(t *testing.T) {
	typeOfState := reflect.TypeOf(BillingRequestState{})
	field, found := typeOfState.FieldByName("RequestedQuota")
	require.True(t, found)

	assert.Equal(t, "requested_quota", field.Tag.Get("json"))
	assert.Equal(t, "not null;default:0", field.Tag.Get("gorm"))
}

func TestBillingFundingStateKeyScopesTheOpaqueIdentityToSource(t *testing.T) {
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "billing-funding-state-test-secret"
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
	})

	requestID := "req_private_billing_funding_123"
	walletKey, err := BillingFundingStateKey(42, 7, "wallet", requestID)
	require.NoError(t, err)
	subscriptionKey, err := BillingFundingStateKey(42, 7, "subscription", requestID)
	require.NoError(t, err)

	assert.NotEqual(t, walletKey, subscriptionKey)
	assert.NotContains(t, walletKey, requestID)
	assert.NotContains(t, walletKey, "wallet")
	assert.Len(t, walletKey, 64)

	typeOfState := reflect.TypeOf(BillingRequestState{})
	field, found := typeOfState.FieldByName("FundingKey")
	require.True(t, found)
	assert.Contains(t, field.Tag.Get("gorm"), "uniqueIndex")
}
