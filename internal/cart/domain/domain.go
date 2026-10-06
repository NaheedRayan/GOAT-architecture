// Package domain holds cart entities and rules.
package domain

import (
	"errors"

	"github.com/google/uuid"
)

const (
	MaxQuantity = 20
	MaxLines    = 50 // distinct variants per cart
)

type Item struct {
	VariantID uuid.UUID
	Quantity  int
}

// Line is a cart item joined with current variant/product data for display and pricing.
type Line struct {
	VariantID      uuid.UUID
	ProductID      uuid.UUID
	Slug           string
	Name           string
	Label          string // variant name, "" for single-form products
	SKU            string
	ImageURL       string
	UnitPriceCents int64
	Currency       string
	Quantity       int
}

func (l Line) SubtotalCents() int64 { return l.UnitPriceCents * int64(l.Quantity) }

type View struct {
	Lines      []Line
	TotalCents int64
	Currency   string
	Count      int
}

func (v View) Empty() bool { return len(v.Lines) == 0 }

var (
	ErrProductUnavailable = errors.New("that item is not available")
	ErrInvalidQuantity    = errors.New("quantity must be between 1 and 20")
	ErrCartFull           = errors.New("your cart is full; remove something before adding more")
)

func UserOwner(id uuid.UUID) string  { return "user:" + id.String() }
func GuestOwner(id uuid.UUID) string { return "guest:" + id.String() }

// ClampQuantity bounds a quantity to the per-line maximum.
func ClampQuantity(q int) int { return min(q, MaxQuantity) }
