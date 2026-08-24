// Package jsonnumber provides exact JSON number conversion helpers.
package jsonnumber

import (
	"encoding/json"
	"fmt"
	"math/big"
)

// ExactInteger converts one JSON number without binary floating-point
// rounding. The result must be integral and inside the inclusive absolute
// limit.
func ExactInteger(number json.Number, maxAbsolute int64) (int64, error) {
	rational, ok := new(big.Rat).SetString(number.String())
	if !ok || rational.Denom().Cmp(big.NewInt(1)) != 0 {
		return 0, fmt.Errorf("JSON number %q is not an exact supported integer", number)
	}
	limit := big.NewInt(maxAbsolute)
	minimum := new(big.Int).Neg(new(big.Int).Set(limit))
	numerator := rational.Num()
	if numerator.Cmp(minimum) < 0 || numerator.Cmp(limit) > 0 {
		return 0, fmt.Errorf("JSON number %q is not an exact supported integer", number)
	}
	return numerator.Int64(), nil
}
