package domain

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	centsPerUnit      = 100
	maxFractionDigits = 2
	maxParsableUnits  = (math.MaxInt64 - (centsPerUnit - 1)) / centsPerUnit
)

// Money is kept as integer cents. There is no float anywhere in the service,
// so an amount is always exactly what the client sent.
type Money struct {
	cents int64
}

func NewMoneyFromCents(cents int64) Money {
	return Money{cents: cents}
}

// ParseMoney rejects more than two decimals instead of rounding. Changing an
// amount silently is worse than returning an error.
func ParseMoney(value string) (Money, error) {
	unsigned, negative := strings.CutPrefix(value, "-")
	unitsText, fractionText, hasFraction := strings.Cut(unsigned, ".")

	if !isDigits(unitsText) {
		return Money{}, invalidMoneyFormat(value)
	}
	if hasFraction && (!isDigits(fractionText) || len(fractionText) > maxFractionDigits) {
		return Money{}, invalidMoneyFormat(value)
	}

	units, err := strconv.ParseInt(unitsText, 10, 64)
	if err != nil || units > maxParsableUnits {
		return Money{}, invalidMoneyFormat(value)
	}

	cents := units*centsPerUnit + fractionToCents(fractionText)
	if negative {
		cents = -cents
	}
	return Money{cents: cents}, nil
}

func (m Money) Cents() int64 {
	return m.cents
}

func (m Money) GreaterThan(other Money) bool {
	return m.cents > other.cents
}

func (m Money) String() string {
	sign := ""
	cents := m.cents
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/centsPerUnit, cents%centsPerUnit)
}

func fractionToCents(fraction string) int64 {
	if fraction == "" {
		return 0
	}
	padded := fraction + strings.Repeat("0", maxFractionDigits-len(fraction))
	cents, _ := strconv.ParseInt(padded, 10, 64)
	return cents
}

func isDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, char := range text {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func invalidMoneyFormat(value string) error {
	return fmt.Errorf("%w: %q", ErrInvalidMoneyFormat, value)
}
