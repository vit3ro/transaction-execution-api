package domain_test

import (
	"errors"
	"testing"

	"github.com/vit3ro/transaction-execution-api/internal/domain"
)

func TestParseMoney_Valid(t *testing.T) {
	tests := []struct {
		input     string
		wantCents int64
	}{
		{"1500.00", 150000},
		{"1500", 150000},
		{"1.5", 150},
		{"0.01", 1},
		{"0", 0},
		{"1.00", 100},
		{"1.01", 101},
		{"10000.00", 1000000},
		{"10000.01", 1000001},
		{"-25.10", -2510},
		{"-0.05", -5},
		{"999999999999.99", 99999999999999},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := domain.ParseMoney(tt.input)
			if err != nil {
				t.Fatalf("ParseMoney(%q) returned unexpected error: %v", tt.input, err)
			}
			if got.Cents() != tt.wantCents {
				t.Errorf("ParseMoney(%q) = %d cents, want %d", tt.input, got.Cents(), tt.wantCents)
			}
		})
	}
}

func TestParseMoney_Invalid(t *testing.T) {
	inputs := []string{
		"",
		"abc",
		"1.234",
		"1.",
		".5",
		"1e3",
		"1,000.00",
		" 1.00",
		"1.00 ",
		"+1.00",
		"-",
		"--1",
		"1.-5",
		"99999999999999999999",
		"92233720368547758.08",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			_, err := domain.ParseMoney(input)
			if !errors.Is(err, domain.ErrInvalidMoneyFormat) {
				t.Errorf("ParseMoney(%q) error = %v, want ErrInvalidMoneyFormat", input, err)
			}
		})
	}
}

func TestMoney_String(t *testing.T) {
	tests := []struct {
		cents int64
		want  string
	}{
		{150000, "1500.00"},
		{1, "0.01"},
		{5, "0.05"},
		{0, "0.00"},
		{-2510, "-25.10"},
		{-5, "-0.05"},
		{1000001, "10000.01"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := domain.NewMoneyFromCents(tt.cents).String(); got != tt.want {
				t.Errorf("Money(%d).String() = %q, want %q", tt.cents, got, tt.want)
			}
		})
	}
}

func TestMoney_GreaterThan(t *testing.T) {
	one := domain.NewMoneyFromCents(100)
	onePlusCent := domain.NewMoneyFromCents(101)

	if !onePlusCent.GreaterThan(one) {
		t.Error("1.01 should be greater than 1.00")
	}
	if one.GreaterThan(one) {
		t.Error("1.00 should not be greater than 1.00")
	}
	if one.GreaterThan(onePlusCent) {
		t.Error("1.00 should not be greater than 1.01")
	}
}
