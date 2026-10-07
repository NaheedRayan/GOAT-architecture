// Package media adapts the platform image pipeline to the catalog's ImageStore port.
package media

import (
	"context"
	"errors"
	"io"

	"github.com/NaheedRayan/goat-architecture/internal/catalog/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/media"
)

type Store struct{ store *media.Store }

func New(store *media.Store) *Store { return &Store{store: store} }

func (s *Store) Save(_ context.Context, r io.Reader) (string, string, error) {
	p, err := media.Process(r)
	if errors.Is(err, media.ErrInvalid) || errors.Is(err, media.ErrTooLarge) {
		return "", "", domain.ValidationError(err.Error())
	}
	if err != nil {
		return "", "", err
	}
	return s.store.Save(p)
}

func (s *Store) Delete(_ context.Context, urls ...string) error { return s.store.Delete(urls...) }
