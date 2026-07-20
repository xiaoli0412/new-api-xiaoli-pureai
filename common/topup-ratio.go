package common

import (
	"fmt"
	"math"
	"sync"
)

var topupGroupRatio = map[string]float64{
	"default": 1,
	"vip":     1,
	"svip":    1,
}
var topupGroupRatioMutex sync.RWMutex

func TopupGroupRatio2JSONString() string {
	topupGroupRatioMutex.RLock()
	defer topupGroupRatioMutex.RUnlock()
	jsonBytes, err := Marshal(topupGroupRatio)
	if err != nil {
		SysError("error marshalling topup group ratio: " + err.Error())
	}
	return string(jsonBytes)
}

func parseTopupGroupRatioJSON(jsonStr string) (map[string]float64, error) {
	next := make(map[string]float64)
	if err := UnmarshalJsonStr(jsonStr, &next); err != nil {
		return nil, err
	}
	if next == nil {
		return nil, fmt.Errorf("topup group ratio settings must be a JSON object")
	}
	for name, ratio := range next {
		if name == "" || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio <= 0 {
			return nil, fmt.Errorf("topup group ratio for %q must be finite and greater than 0", name)
		}
	}
	return next, nil
}

// ValidateTopupGroupRatioJSON checks an update without changing the active
// settings, so options can be validated before they are persisted.
func ValidateTopupGroupRatioJSON(jsonStr string) error {
	_, err := parseTopupGroupRatioJSON(jsonStr)
	return err
}

func UpdateTopupGroupRatioByJSONString(jsonStr string) error {
	next, err := parseTopupGroupRatioJSON(jsonStr)
	if err != nil {
		return err
	}

	topupGroupRatioMutex.Lock()
	defer topupGroupRatioMutex.Unlock()
	topupGroupRatio = next
	return nil
}

func GetTopupGroupRatio(name string) float64 {
	topupGroupRatioMutex.RLock()
	defer topupGroupRatioMutex.RUnlock()
	ratio, ok := topupGroupRatio[name]
	if !ok {
		SysError("topup group ratio not found: " + name)
		return 1
	}
	return ratio
}
