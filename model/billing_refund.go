package model

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BillingRefundClaim is the durable idempotency receipt for one billing refund.
// It stays in the same main-database transaction as the funding mutation and
// the AETHER financial outbox event.
type BillingRefundClaim struct {
	Id         int64  `json:"id"`
	ClaimKey   string `json:"-" gorm:"type:varchar(128);uniqueIndex"`
	ClaimToken string `json:"-" gorm:"type:varchar(64);not null"`
	CreatedAt  int64  `json:"created_at" gorm:"autoCreateTime"`
}

// ClaimBillingRefundTx atomically claims a refund key. A duplicate caller gets
// false without applying another financial mutation. The candidate token avoids
// relying on dialect-specific RowsAffected semantics for ON CONFLICT.
func ClaimBillingRefundTx(tx *gorm.DB, claimKey string) (bool, error) {
	if tx == nil {
		return false, errors.New("billing refund claim transaction is required")
	}
	claimKey = strings.TrimSpace(claimKey)
	if claimKey == "" {
		return false, errors.New("billing refund claim key is required")
	}
	candidate := &BillingRefundClaim{
		ClaimKey:   claimKey,
		ClaimToken: common.NewRequestId(),
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(candidate).Error; err != nil {
		return false, err
	}
	var stored BillingRefundClaim
	if err := lockForUpdate(tx).Where("claim_key = ?", claimKey).First(&stored).Error; err != nil {
		return false, err
	}
	return stored.ClaimToken == candidate.ClaimToken, nil
}
