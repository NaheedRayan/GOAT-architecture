// Package domain holds identity entities and rules. It imports nothing outside the standard library and uuid.
package domain

import (
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID            uuid.UUID
	Email         string
	Name          string
	Role          string
	PasswordHash  string
	CreatedAt     time.Time
	EmailVerified bool
	Guest         bool // created at checkout; no password until the owner claims the account
	Disabled      bool
	CartReminders bool // wants an email when a cart is left behind
}

// IsStaff reports whether the user may enter the back office.
func (u User) IsStaff() bool { return u.Role == "admin" || u.Role == "staff" }

type Address struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	FullName   string
	Phone      string
	Line1      string
	Line2      string
	City       string
	PostalCode string
	Country    string
	IsDefault  bool
}

type RefreshToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	ExpiresAt time.Time
	RevokedAt *time.Time
	RotatedAt *time.Time // set when the token was exchanged for a new one (not when revoked)
	// Expired and RotatedAge are measured by the database clock (see GetRefreshTokenByHash).
	Expired    bool
	RotatedAge *time.Duration
}

var (
	ErrEmailTaken         = errors.New("an account with this email already exists")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrInvalidToken       = errors.New("invalid or expired session")
	ErrNotFound           = errors.New("not found")
	ErrAccountExists      = errors.New("an account with this email already exists; please sign in")
	ErrGuestAccount       = errors.New("you checked out as a guest with this email before; use \"Forgot password\" to set a password and claim your account")
	ErrAccountDisabled    = errors.New("this account has been disabled; please contact support")
	ErrWrongPassword      = errors.New("the current password is not correct")
	ErrLastAdmin          = errors.New("there must be at least one active administrator")
	ErrSelfChange         = errors.New("you cannot change your own role or disable your own account")
)

// ValidationError is a user-correctable input problem.
type ValidationError string

func (e ValidationError) Error() string { return string(e) }

const MinPasswordLen = 8

func ValidateRegistration(email, name, password string) error {
	if _, err := mail.ParseAddress(email); err != nil || strings.ContainsAny(email, " <>") {
		return ValidationError("enter a valid email address")
	}
	if len(email) > 254 {
		return ValidationError("email address is too long")
	}
	if strings.TrimSpace(name) == "" {
		return ValidationError("name is required")
	}
	if len([]rune(name)) > 100 {
		return ValidationError("name is too long (100 characters max)")
	}
	if len(password) < MinPasswordLen {
		return ValidationError("password must be at least 8 characters")
	}
	if len(password) > 128 {
		return ValidationError("password is too long")
	}
	return nil
}

func (a Address) Validate() error {
	for _, f := range []struct{ v, n string }{
		{a.FullName, "full name"}, {a.Phone, "phone"}, {a.Line1, "address line 1"},
		{a.City, "city"}, {a.PostalCode, "postal code"}, {a.Country, "country"},
	} {
		if strings.TrimSpace(f.v) == "" {
			return ValidationError(f.n + " is required")
		}
	}
	return checkLengths(a.FullName, a.Phone, a.Line1, a.Line2, a.City, a.PostalCode, a.Country)
}

// checkLengths keeps address fields to a sane size (they are stored per row and shown in emails/labels).
func checkLengths(fullName, phone, line1, line2, city, postal, country string) error {
	for _, f := range []struct {
		v   string
		max int
		n   string
	}{{fullName, 100, "full name"}, {phone, 30, "phone"}, {line1, 200, "address line 1"}, {line2, 200, "address line 2"},
		{city, 100, "city"}, {postal, 20, "postal code"}, {country, 100, "country"}} {
		if len([]rune(f.v)) > f.max {
			return ValidationError(f.n + " is too long")
		}
	}
	return nil
}

const (
	RoleCustomer = "customer"
	RoleStaff    = "staff"
	RoleAdmin    = "admin"
)

func ValidRole(r string) bool { return r == RoleCustomer || r == RoleStaff || r == RoleAdmin }

// ValidatePassword applies the password policy for new passwords.
func ValidatePassword(p string) error {
	switch {
	case len(p) < MinPasswordLen:
		return ValidationError("password must be at least 8 characters")
	case len(p) > 128:
		return ValidationError("password is too long")
	}
	return nil
}

// ValidateEmail checks an address for checkout/registration.
func ValidateEmail(email string) error {
	if len(email) > 254 {
		return ValidationError("email address is too long")
	}
	if _, err := mail.ParseAddress(email); err != nil || strings.ContainsAny(email, " <>\r\n") {
		return ValidationError("enter a valid email address")
	}
	return nil
}
