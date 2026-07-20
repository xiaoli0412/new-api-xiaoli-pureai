package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewBillingSessionRejectsBlankRequestIDWithoutBillingMutation(t *testing.T) {
	db := openBillingRequestStateDB(t, "billing_request_state_blank_request_id")
	require.NoError(t, db.Create(&model.User{Id: 6114, Username: "billing-state-blank-request-id", Quota: 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 6114, UserId: 6114, Key: "billing-state-blank-request-id-token", RemainQuota: 1000}).Error)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(context, newWalletBillingRelayInfo(6114, 6114, "billing-state-blank-request-id-token", ""), 100)

	require.Nil(t, session)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeInvalidRequest, apiErr.GetErrorCode())
	assert.Contains(t, apiErr.Error(), "billing request ID is required")

	var user model.User
	var token model.Token
	var stateCount int64
	require.NoError(t, db.First(&user, 6114).Error)
	require.NoError(t, db.First(&token, 6114).Error)
	require.NoError(t, db.Model(&model.BillingRequestState{}).Count(&stateCount).Error)
	assert.Equal(t, 1000, user.Quota)
	assert.Equal(t, 1000, token.RemainQuota)
	assert.Zero(t, stateCount)
}
