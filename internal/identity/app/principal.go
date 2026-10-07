package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/identity/domain"
)

// Principal is the live standing of an account. Access tokens are stateless and
// stay valid for minutes, so on every request the middleware checks this to
// honour role changes, disabled accounts and deleted accounts straight away.
type Principal struct {
	Role   string
	Active bool
}

const principalTTL = 10 * time.Second

type cachedPrincipal struct {
	p     Principal
	until time.Time
}

// principalCache keeps lookups off the database for a few seconds. Changes made
// through this process are visible immediately (entries are evicted); other
// instances catch up within principalTTL.
type principalCache struct {
	mu sync.Mutex
	m  map[uuid.UUID]cachedPrincipal
}

func (c *principalCache) get(id uuid.UUID) (Principal, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	if !ok || time.Now().After(e.until) {
		return Principal{}, false
	}
	return e.p, true
}

func (c *principalCache) put(id uuid.UUID, p Principal) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 20000 { // bounded: drop everything rather than grow forever
		c.m = map[uuid.UUID]cachedPrincipal{}
	}
	c.m[id] = cachedPrincipal{p: p, until: time.Now().Add(principalTTL)}
}

func (c *principalCache) evict(id uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, id)
}

// Principal returns the account's current role and whether it may sign in.
// An unknown (e.g. deleted) account is inactive.
func (s *Service) Principal(ctx context.Context, userID uuid.UUID) (Principal, error) {
	if p, ok := s.principals.get(userID); ok {
		return p, nil
	}
	u, err := s.Repo.UserByID(ctx, userID)
	if errors.Is(err, domain.ErrNotFound) {
		p := Principal{}
		s.principals.put(userID, p)
		return p, nil
	}
	if err != nil {
		return Principal{}, err
	}
	p := Principal{Role: u.Role, Active: !u.Disabled}
	s.principals.put(userID, p)
	return p, nil
}
