// Package id generates time-ordered UUIDv7 identifiers used for every primary key.
package id

import "github.com/google/uuid"

type ID = uuid.UUID

// New returns a new UUIDv7. It panics only if the system entropy source fails.
func New() ID {
	return uuid.Must(uuid.NewV7())
}

func Parse(s string) (ID, error) {
	return uuid.Parse(s)
}
