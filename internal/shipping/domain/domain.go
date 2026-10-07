// Package domain holds delivery methods and their pricing rules.
package domain

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type Method struct {
	ID            uuid.UUID
	Name          string
	Description   string
	PriceCents    int64
	FreeOverCents *int64 // free when the order subtotal reaches this amount
	MinDays       int
	MaxDays       int
	Active        bool
	Position      int
}

// Option is a method as offered for a particular basket.
type Option struct {
	ID          uuid.UUID
	Name        string
	Description string
	PriceCents  int64 // 0 when the free-shipping threshold is met
	Free        bool
	Eta         string // "3–5 days", "" when unspecified
}

var (
	ErrNotFound     = errors.New("shipping method not found")
	ErrNotAvailable = errors.New("that delivery method is not available")
)

type ValidationError string

func (e ValidationError) Error() string { return string(e) }

// OptionFor prices the method for a basket with the given subtotal.
func (m Method) OptionFor(subtotalCents int64) Option {
	price := m.PriceCents
	free := false
	if m.FreeOverCents != nil && subtotalCents >= *m.FreeOverCents {
		price, free = 0, true
	}
	return Option{ID: m.ID, Name: m.Name, Description: m.Description, PriceCents: price, Free: free, Eta: m.Eta()}
}

// Eta describes the delivery window.
func (m Method) Eta() string {
	switch {
	case m.MaxDays == 0:
		return ""
	case m.MinDays == m.MaxDays:
		if m.MaxDays == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", m.MaxDays)
	default:
		return fmt.Sprintf("%d–%d days", m.MinDays, m.MaxDays)
	}
}

func (m Method) Validate() error {
	switch {
	case strings.TrimSpace(m.Name) == "":
		return ValidationError("name is required")
	case len([]rune(m.Name)) > 80:
		return ValidationError("name is too long (80 characters max)")
	case len([]rune(m.Description)) > 200:
		return ValidationError("description is too long (200 characters max)")
	case m.PriceCents < 0:
		return ValidationError("price cannot be negative")
	case m.FreeOverCents != nil && *m.FreeOverCents <= 0:
		return ValidationError("the free-shipping threshold must be more than zero (or empty for none)")
	case m.MinDays < 0 || m.MaxDays < m.MinDays || m.MaxDays > 90:
		return ValidationError("delivery days must satisfy 0 ≤ min ≤ max ≤ 90")
	}
	return nil
}
