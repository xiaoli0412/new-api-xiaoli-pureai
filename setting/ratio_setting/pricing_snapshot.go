package ratio_setting

import (
	"sync"

	"github.com/QuantumNous/new-api/common"
)

var pricingSnapshotMutex sync.RWMutex

type PricingSnapshot struct {
	QuotaPerUnit         float64
	GroupRatio           map[string]float64
	ModelRatio           map[string]float64
	CompletionRatio      map[string]float64
	CacheRatio           map[string]float64
	CreateCacheRatio     map[string]float64
	ImageRatio           map[string]float64
	AudioRatio           map[string]float64
	AudioCompletionRatio map[string]float64
	ModelPrice           map[string]float64
	BillingMode          map[string]string
	BillingExpr          map[string]string
}

func ReadPricingSettings(read func()) {
	pricingSnapshotMutex.RLock()
	defer pricingSnapshotMutex.RUnlock()
	read()
}

func UpdatePricingSettings(update func() error) error {
	pricingSnapshotMutex.Lock()
	defer pricingSnapshotMutex.Unlock()
	return update()
}

func UpdateQuotaPerUnit(quotaPerUnit float64) {
	pricingSnapshotMutex.Lock()
	defer pricingSnapshotMutex.Unlock()
	common.QuotaPerUnit = quotaPerUnit
}

func GetPricingSnapshot(readBilling func() (map[string]string, map[string]string)) PricingSnapshot {
	pricingSnapshotMutex.RLock()
	defer pricingSnapshotMutex.RUnlock()

	billingMode, billingExpr := readBilling()
	return PricingSnapshot{
		QuotaPerUnit:         common.QuotaPerUnit,
		GroupRatio:           groupRatioMap.ReadAll(),
		ModelRatio:           modelRatioMap.ReadAll(),
		CompletionRatio:      completionRatioMap.ReadAll(),
		CacheRatio:           cacheRatioMap.ReadAll(),
		CreateCacheRatio:     createCacheRatioMap.ReadAll(),
		ImageRatio:           imageRatioMap.ReadAll(),
		AudioRatio:           audioRatioMap.ReadAll(),
		AudioCompletionRatio: audioCompletionRatioMap.ReadAll(),
		ModelPrice:           modelPriceMap.ReadAll(),
		BillingMode:          billingMode,
		BillingExpr:          billingExpr,
	}
}
