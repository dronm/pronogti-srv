package checkout

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// MaxMoney keeps every integer amount exactly representable in the frontend's
// JSON number type. All calculations still use checked integer arithmetic.
const MaxMoney int64 = 9007199254740991

const quantityScale int64 = 1000
const maxQuantityMilli int64 = 1000000 * quantityScale

// ParseMoney converts an exact decimal ruble amount to integer kopecks. Extra
// fractional zeroes are accepted; a nonzero fraction below one kopeck is not.
func ParseMoney(value string) (int64, error) {
	amount, err := parseScaled(value, 2, MaxMoney)
	if err != nil {
		return 0, fmt.Errorf("invalid ruble amount %q: %w", value, err)
	}
	return amount, nil
}

// MoneyFromFloat is the boundary adapter for legacy YClients models. It parses
// the shortest exact decimal representation instead of rounding price*100.
func MoneyFromFloat(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, fmt.Errorf("money must be finite and nonnegative")
	}
	return ParseMoney(strconv.FormatFloat(value, 'f', -1, 64))
}

// QuantityFromFloat adapts a legacy catalog/visit quantity without silent
// rounding. New checkout requests send the corresponding decimal string.
func QuantityFromFloat(value float64) (string, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return "", fmt.Errorf("quantity must be finite and positive")
	}
	canonical, _, err := normalizeQuantity(strconv.FormatFloat(value, 'f', -1, 64))
	return canonical, err
}

// ParseQuantity returns the canonical checkout quantity and its exact
// representation in thousandths of a unit.
func ParseQuantity(value string) (string, int64, error) {
	return normalizeQuantity(value)
}

func normalizeQuantity(value string) (string, int64, error) {
	milli, err := parseScaled(value, 3, maxQuantityMilli)
	if err != nil || milli <= 0 {
		return "", 0, fmt.Errorf("quantity must be positive, at most 1000000, and have at most three nonzero decimal places")
	}
	whole := milli / quantityScale
	fraction := milli % quantityScale
	canonical := strconv.FormatInt(whole, 10)
	if fraction != 0 {
		canonical += "." + strings.TrimRight(fmt.Sprintf("%03d", fraction), "0")
	}
	return canonical, milli, nil
}

func parseScaled(value string, digits int, maximum int64) (int64, error) {
	if len(value) == 0 || len(value) > 64 {
		return 0, fmt.Errorf("expected an unsigned decimal")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, fmt.Errorf("expected an unsigned decimal")
	}
	for _, part := range parts {
		if part == "" {
			return 0, fmt.Errorf("expected an unsigned decimal")
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("expected an unsigned decimal")
			}
		}
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > digits {
		if strings.Trim(fraction[digits:], "0") != "" {
			return 0, fmt.Errorf("fraction is smaller than the supported unit")
		}
		fraction = fraction[:digits]
	}
	fraction += strings.Repeat("0", digits-len(fraction))
	integer := new(big.Int)
	if _, ok := integer.SetString(parts[0]+fraction, 10); !ok || !integer.IsInt64() || integer.Sign() < 0 || integer.Int64() > maximum {
		return 0, fmt.Errorf("amount exceeds the supported range")
	}
	return integer.Int64(), nil
}

// Catalog goods may have fractional quantities. Round a line once, half up, to
// the nearest kopeck using integers, then sum already rounded line amounts.
func lineAmount(unitPrice, milli int64) (int64, error) {
	if unitPrice <= 0 || unitPrice > MaxMoney || milli <= 0 || milli > maxQuantityMilli {
		return 0, fmt.Errorf("invalid price or quantity")
	}
	amount := new(big.Int).Mul(big.NewInt(unitPrice), big.NewInt(milli))
	amount.Add(amount, big.NewInt(quantityScale/2))
	amount.Div(amount, big.NewInt(quantityScale))
	if !amount.IsInt64() || amount.Sign() <= 0 || amount.Int64() > MaxMoney {
		return 0, fmt.Errorf("line amount exceeds the supported range or rounds to zero")
	}
	return amount.Int64(), nil
}

func addMoney(left, right int64) (int64, error) {
	if left < 0 || right < 0 || left > MaxMoney-right {
		return 0, fmt.Errorf("checkout total exceeds the supported range")
	}
	return left + right, nil
}
