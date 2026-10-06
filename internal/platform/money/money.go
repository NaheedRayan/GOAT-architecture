// Package money formats integer minor-unit amounts for display.
package money

import (
	"fmt"
	"strconv"
	"strings"
)

var symbols = map[string]string{"USD": "$", "BDT": "৳", "EUR": "€", "GBP": "£"}

func Format(cents int64, currency string) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	num := fmt.Sprintf("%d.%02d", cents/100, cents%100)
	if s, ok := symbols[currency]; ok {
		return sign + s + num
	}
	return sign + currency + " " + num
}

// ParseCents converts a decimal string such as "12.5" or "12.50" to minor units.
func ParseCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" && frac == "" || len(frac) > 2 || strings.HasPrefix(whole, "-") {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	frac += strings.Repeat("0", 2-len(frac))
	w, err := strconv.ParseInt(or0(whole), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	return w*100 + f, nil
}

func or0(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// Decimal renders minor units as a plain decimal such as "12.50" (for form inputs).
func Decimal(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }
