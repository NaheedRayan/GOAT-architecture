// Package domain holds product reviews and ratings.
package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	StatusPublished = "published"
	StatusHidden    = "hidden"
)

type Review struct {
	ID        uuid.UUID
	ProductID uuid.UUID
	UserID    uuid.UUID
	Rating    int
	Title     string
	Body      string
	Author    string // "Ada L.": a first name and an initial, never the full name
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Summary is a product's average rating.
type Summary struct {
	Count   int
	Average float64
	Stars   [6]int // Stars[n] = reviews with n stars (index 0 unused)
}

var (
	ErrNotPurchased = errors.New("only customers who received this product can review it")
	ErrNotFound     = errors.New("review not found")
)

type ValidationError string

func (e ValidationError) Error() string { return string(e) }

func (r Review) Validate() error {
	switch {
	case r.Rating < 1 || r.Rating > 5:
		return ValidationError("choose a rating from 1 to 5 stars")
	case len([]rune(r.Title)) > 100:
		return ValidationError("the title is too long (100 characters max)")
	case len([]rune(r.Body)) > 2000:
		return ValidationError("the review is too long (2000 characters max)")
	case strings.TrimSpace(r.Title) == "" && strings.TrimSpace(r.Body) == "":
		return ValidationError("write a few words about the product")
	}
	return nil
}

// DisplayName shows a first name and last initial, which is enough for a review
// without exposing a customer's full name.
func DisplayName(full string) string {
	parts := strings.Fields(full)
	switch len(parts) {
	case 0:
		return "Customer"
	case 1:
		return parts[0]
	}
	last := []rune(parts[len(parts)-1])
	return parts[0] + " " + string(last[0]) + "."
}
