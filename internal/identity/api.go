// Package identity is the public surface of the identity module. Other modules
// import only this package, never its subpackages.
package identity

import (
	"context"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/identity/app"
	"github.com/NaheedRayan/goat-architecture/internal/identity/domain"
)

type (
	Address    = domain.Address
	User       = domain.User
	UserExport = app.UserExport
)

const (
	RoleCustomer = domain.RoleCustomer
	RoleStaff    = domain.RoleStaff
	RoleAdmin    = domain.RoleAdmin
)

var (
	ErrNotFound      = domain.ErrNotFound
	ErrLastAdmin     = domain.ErrLastAdmin
	ErrSelfChange    = domain.ErrSelfChange
	ErrWrongPassword = domain.ErrWrongPassword
	ErrAccountExists = domain.ErrAccountExists

	// ErrTooManyAttempts is returned when a rate limit refuses guest account creation.
	ErrTooManyAttempts = app.ErrTooManyAttempts
)

type ValidationError = domain.ValidationError

// EventAccountDeleted is published (payload {"user_id": uuid}) when an account is deleted.
const EventAccountDeleted = app.EventAccountDeleted

type API interface {
	EnsureAdmin(ctx context.Context, email, password string) error
	ListAddresses(ctx context.Context, userID uuid.UUID) ([]Address, error)
	User(ctx context.Context, id uuid.UUID) (User, error)
	ListUsers(ctx context.Context, query, role string, limit, offset int) ([]User, error)
	SetCartReminders(ctx context.Context, id uuid.UUID, on bool) error
	SetRole(ctx context.Context, actorID, targetID uuid.UUID, role string) error
	SetDisabled(ctx context.Context, actorID, targetID uuid.UUID, disabled bool) error
	Export(ctx context.Context, id uuid.UUID) (UserExport, error)
	DeleteAccount(ctx context.Context, id uuid.UUID, password string) error
}
