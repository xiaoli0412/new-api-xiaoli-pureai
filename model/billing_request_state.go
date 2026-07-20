package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const (
	BillingRequestStatePreconsumed = "preconsumed"
	BillingRequestStateSettled     = "settled"
	BillingRequestStateRefunded    = "refunded"
)

// BillingRequestState is the durable financial receipt for one relay request.
// RequestKey is an HMAC; raw request IDs and token keys are intentionally not
// persisted because this table is an internal accounting authority.
type BillingRequestState struct {
	Id                   int64  `json:"id"`
	RequestKey           string `json:"-" gorm:"type:char(64);uniqueIndex"`
	FundingKey           string `json:"-" gorm:"type:char(64);uniqueIndex"`
	ClaimToken           string `json:"-" gorm:"type:varchar(64);not null"`
	UserID               int    `json:"user_id" gorm:"index"`
	TokenID              int    `json:"token_id" gorm:"index"`
	FundingSource        string `json:"funding_source" gorm:"type:varchar(32);not null"`
	SubscriptionID       int    `json:"subscription_id" gorm:"index"`
	RequestedQuota       int    `json:"requested_quota" gorm:"not null;default:0"`
	PreConsumedQuota     int    `json:"pre_consumed_quota" gorm:"not null;default:0"`
	FundingConsumedQuota int    `json:"funding_consumed_quota" gorm:"not null;default:0"`
	TokenConsumedQuota   int    `json:"token_consumed_quota" gorm:"not null;default:0"`
	ExtraReservedQuota   int    `json:"extra_reserved_quota" gorm:"not null;default:0"`
	SettledQuota         int    `json:"settled_quota" gorm:"not null;default:0"`
	Trusted              bool   `json:"trusted"`
	State                string `json:"state" gorm:"type:varchar(32);index;not null"`
	CreatedAt            int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt            int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

// BillingRequestStateKey returns the stable, opaque identity for a billing
// request. It scopes an otherwise client-visible request ID to its user and
// token so records cannot collide across principals.
func BillingRequestStateKey(userID int, tokenID int, requestID string) (string, error) {
	if userID <= 0 {
		return "", errors.New("billing request user ID is required")
	}
	if tokenID < 0 {
		return "", errors.New("invalid billing request token ID")
	}
	if strings.TrimSpace(requestID) == "" || requestID != strings.TrimSpace(requestID) {
		return "", errors.New("billing request ID is required")
	}
	return common.GenerateHMAC(fmt.Sprintf("billing-request-state:v1:user:%d:token:%d:request:%s", userID, tokenID, requestID)), nil
}

// BillingFundingStateKey adds the selected funding source to the opaque
// request identity. The persisted state retains both this source-scoped key
// and the request key, without storing the raw request ID or token key.
func BillingFundingStateKey(userID int, tokenID int, fundingSource string, requestID string) (string, error) {
	if _, err := BillingRequestStateKey(userID, tokenID, requestID); err != nil {
		return "", err
	}
	if strings.TrimSpace(fundingSource) == "" || fundingSource != strings.TrimSpace(fundingSource) {
		return "", errors.New("billing funding source is required")
	}
	return common.GenerateHMAC(fmt.Sprintf("billing-funding-state:v1:user:%d:token:%d:source:%s:request:%s", userID, tokenID, fundingSource, requestID)), nil
}
