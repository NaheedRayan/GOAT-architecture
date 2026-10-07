// Package domain holds editable content pages.
package domain

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Page struct {
	ID        uuid.UUID
	Slug      string
	Title     string
	Body      string
	Published bool
	Position  int
	UpdatedAt time.Time
}

type Link struct{ Slug, Title string }

var (
	ErrNotFound  = errors.New("page not found")
	ErrSlugTaken = errors.New("another page already uses that URL")
)

type ValidationError string

func (e ValidationError) Error() string { return string(e) }

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,48}$`)

// reserved URLs are pages the shop already serves under /pages/.
var reserved = map[string]bool{}

const MaxBodyBytes = 50_000

// placeholder matches the [[LIKE THIS]] markers in the starter templates.
var placeholder = regexp.MustCompile(`\[\[[^\]]*\]\]`)

// Placeholders lists the template markers still present in text.
func Placeholders(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range placeholder.FindAllString(text, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

func (p Page) Validate() error {
	switch {
	case !slugRe.MatchString(p.Slug) || reserved[p.Slug]:
		return ValidationError("the URL must be 2-49 characters: lowercase letters, digits and dashes")
	case strings.TrimSpace(p.Title) == "":
		return ValidationError("a title is required")
	case len([]rune(p.Title)) > 120:
		return ValidationError("the title is too long (120 characters max)")
	case len(p.Body) > MaxBodyBytes:
		return ValidationError("the page is too long (50,000 characters max)")
	}
	if p.Published {
		if ph := Placeholders(p.Title + "\n" + p.Body); len(ph) > 0 {
			shown := ph
			if len(shown) > 3 {
				shown = shown[:3]
			}
			return ValidationError("replace the template placeholders before publishing, e.g. " + strings.Join(shown, ", "))
		}
	}
	return nil
}
