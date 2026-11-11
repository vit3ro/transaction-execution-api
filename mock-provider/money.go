package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	centsPerUnit      = 100
	maxFractionDigits = 2
	maxUnitDigits     = 15
)

var errInvalidAmount = errors.New("amount must be a positive number with at most two decimal places")

func parseCents(value string) (int64, error) {
	unitsText, fractionText, hasFraction := strings.Cut(value, ".")
	if !isDigits(unitsText) || len(unitsText) > maxUnitDigits {
		return 0, errInvalidAmount
	}
	if hasFraction && (!isDigits(fractionText) || len(fractionText) > maxFractionDigits) {
		return 0, errInvalidAmount
	}

	units, err := strconv.ParseInt(unitsText, 10, 64)
	if err != nil {
		return 0, errInvalidAmount
	}
	fraction := int64(0)
	if hasFraction {
		padded := fractionText + strings.Repeat("0", maxFractionDigits-len(fractionText))
		fraction, _ = strconv.ParseInt(padded, 10, 64)
	}
	return units*centsPerUnit + fraction, nil
}

func formatCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/centsPerUnit, cents%centsPerUnit)
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
