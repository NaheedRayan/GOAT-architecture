// Package identity is the public surface of the identity module. Other modules
// import only this package, never its subpackages.
package identity

import (
	"context"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/identity/domain"
)

type Address = domain.Address

type API interface {
	EnsureAdmin(ctx context.Context, email, password string) error
	ListAddresses(ctx context.Context, userID uuid.UUID) ([]Address, error)
}
