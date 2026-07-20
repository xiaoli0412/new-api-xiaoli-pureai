package ratio_setting

import (
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
)

func parseNonNegativeRatioMap(jsonStr string) (map[string]float64, error) {
	ratios := make(map[string]float64)
	if err := common.UnmarshalJsonStr(jsonStr, &ratios); err != nil {
		return nil, err
	}
	if ratios == nil {
		return nil, fmt.Errorf("ratio settings must be a JSON object")
	}
	for name, ratio := range ratios {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("ratio name must not be empty")
		}
		if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 {
			return nil, fmt.Errorf("ratio for %q must be finite and not less than 0", name)
		}
	}
	return ratios, nil
}

// ValidateNonNegativeRatioMapJSON checks a ratio map without changing the
// active settings, so option persistence can reject unsafe values first.
func ValidateNonNegativeRatioMapJSON(jsonStr string) error {
	_, err := parseNonNegativeRatioMap(jsonStr)
	return err
}

func updateNonNegativeRatioMap(m *types.RWMap[string, float64], jsonStr string, onSuccess func()) error {
	ratios, err := parseNonNegativeRatioMap(jsonStr)
	if err != nil {
		return err
	}
	pricingSnapshotMutex.Lock()
	defer pricingSnapshotMutex.Unlock()
	m.Replace(ratios)
	if onSuccess != nil {
		onSuccess()
	}
	return nil
}

func parseNonNegativeGroupRatioMap(jsonStr string) (map[string]map[string]float64, error) {
	ratios := make(map[string]map[string]float64)
	if err := common.UnmarshalJsonStr(jsonStr, &ratios); err != nil {
		return nil, err
	}
	if ratios == nil {
		return nil, fmt.Errorf("group ratio settings must be a JSON object")
	}
	for userGroup, groupRatios := range ratios {
		if strings.TrimSpace(userGroup) == "" || groupRatios == nil {
			return nil, fmt.Errorf("group ratio settings contain an invalid group")
		}
		for usingGroup, ratio := range groupRatios {
			if strings.TrimSpace(usingGroup) == "" {
				return nil, fmt.Errorf("group ratio settings contain an empty group")
			}
			if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 {
				return nil, fmt.Errorf("group ratio for %q/%q must be finite and not less than 0", userGroup, usingGroup)
			}
		}
	}
	return ratios, nil
}

// ValidateNonNegativeGroupRatioMapJSON checks group overrides without
// changing the active settings.
func ValidateNonNegativeGroupRatioMapJSON(jsonStr string) error {
	_, err := parseNonNegativeGroupRatioMap(jsonStr)
	return err
}
