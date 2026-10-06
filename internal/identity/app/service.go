// Package app contains identity use cases and the ports they depend on.
package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/identity/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
)

type Repository interface {
	CreateUser(ctx context.Context, u domain.User) error // returns domain.ErrEmailTaken on duplicates
	UserByEmail(ctx context.Context, email string) (domain.User, error)
	UserByID(ctx context.Context, id uuid.UUID) (domain.User, error)

	SaveRefreshToken(ctx context.Context, t domain.RefreshToken, hash []byte) error
	// ConsumeRefreshToken atomically revokes a valid token. When the token was
	// already revoked it returns domain.ErrInvalidToken along with the stored record.
	ConsumeRefreshToken(ctx context.Context, hash []byte) (domain.RefreshToken, error)
	LookupRefreshToken(ctx context.Context, hash []byte) (domain.RefreshToken, error)
	RevokeRefreshToken(ctx context.Context, hash []byte) error
	RevokeAllRefreshTokens(ctx context.Context, userID uuid.UUID) error
	PurgeRefreshTokens(ctx context.Context, before time.Time) (int64, error)

	ListAddresses(ctx context.Context, userID uuid.UUID) ([]domain.Address, error)
	AddAddress(ctx context.Context, a domain.Address) error
	DeleteAddress(ctx context.Context, userID, addressID uuid.UUID) error
}

type Hasher interface {
	Hash(password string) (string, error)
	Verify(password, hash string) bool
}

type TokenIssuer interface {
	Issue(userID uuid.UUID, role string) (string, error)
}

type Session struct {
	User             domain.User
	AccessToken      string
	RefreshToken     string
	RefreshExpiresAt time.Time
}

type Service struct {
	repo       Repository
	hasher     Hasher
	tokens     TokenIssuer
	refreshTTL time.Duration
	dummyHash  string
}

// reuseGrace tolerates parallel requests racing on one refresh token.
const reuseGrace = 30 * time.Second

func NewService(repo Repository, hasher Hasher, tokens TokenIssuer, refreshTTL time.Duration) *Service {
	dummy, _ := hasher.Hash("not-a-real-password")
	return &Service{repo: repo, hasher: hasher, tokens: tokens, refreshTTL: refreshTTL, dummyHash: dummy}
}

func (s *Service) Register(ctx context.Context, email, name, password string) (Session, error) {
	email, name = strings.TrimSpace(email), strings.TrimSpace(name)
	if err := domain.ValidateRegistration(email, name, password); err != nil {
		return Session{}, err
	}
	u, err := s.newUser(email, name, password, auth.RoleCustomer)
	if err != nil {
		return Session{}, err
	}
	if err := s.repo.CreateUser(ctx, u); err != nil {
		return Session{}, err
	}
	return s.startSession(ctx, u)
}

func (s *Service) Login(ctx context.Context, email, password string) (Session, error) {
	u, err := s.repo.UserByEmail(ctx, strings.TrimSpace(email))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.hasher.Verify(password, s.dummyHash) // equalise timing
			return Session{}, domain.ErrInvalidCredentials
		}
		return Session{}, err
	}
	if !s.hasher.Verify(password, u.PasswordHash) {
		return Session{}, domain.ErrInvalidCredentials
	}
	return s.startSession(ctx, u)
}

// Refresh rotates the refresh token and issues a new access token.
func (s *Service) Refresh(ctx context.Context, raw string) (Session, error) {
	hash := hashToken(raw)
	t, err := s.repo.ConsumeRefreshToken(ctx, hash)
	if err != nil {
		// Unknown, expired, or revoked by logout/theft response: never honoured.
		if !errors.Is(err, domain.ErrInvalidToken) || t.RotatedAt == nil || !t.ExpiresAt.After(time.Now()) {
			return Session{}, domain.ErrInvalidToken
		}
		if time.Since(*t.RotatedAt) > reuseGrace {
			// A token that was exchanged long ago is being replayed: assume theft, end all sessions.
			_ = s.repo.RevokeAllRefreshTokens(ctx, t.UserID)
			return Session{}, domain.ErrInvalidToken
		}
		// A parallel request just rotated this token. The caller holds a token
		// that was valid seconds ago, so give it an access token only; the browser
		// will receive the new refresh token from the request that won the race.
		u, err := s.repo.UserByID(ctx, t.UserID)
		if err != nil {
			return Session{}, domain.ErrInvalidToken
		}
		access, err := s.tokens.Issue(u.ID, u.Role)
		if err != nil {
			return Session{}, err
		}
		return Session{User: u, AccessToken: access}, nil
	}
	u, err := s.repo.UserByID(ctx, t.UserID)
	if err != nil {
		return Session{}, domain.ErrInvalidToken
	}
	return s.startSession(ctx, u)
}

func (s *Service) Logout(ctx context.Context, raw string) error {
	if raw == "" {
		return nil
	}
	return s.repo.RevokeRefreshToken(ctx, hashToken(raw))
}

// EnsureAdmin creates the bootstrap admin account if it does not exist.
func (s *Service) EnsureAdmin(ctx context.Context, email, password string) error {
	if _, err := s.repo.UserByEmail(ctx, email); err == nil {
		return nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if err := domain.ValidateRegistration(email, "Admin", password); err != nil {
		return err
	}
	u, err := s.newUser(email, "Admin", password, auth.RoleAdmin)
	if err != nil {
		return err
	}
	if err := s.repo.CreateUser(ctx, u); err != nil && !errors.Is(err, domain.ErrEmailTaken) {
		return err
	}
	return nil
}

func (s *Service) ListAddresses(ctx context.Context, userID uuid.UUID) ([]domain.Address, error) {
	return s.repo.ListAddresses(ctx, userID)
}

func (s *Service) AddAddress(ctx context.Context, a domain.Address) error {
	if err := a.Validate(); err != nil {
		return err
	}
	a.ID = id.New()
	return s.repo.AddAddress(ctx, a)
}

func (s *Service) DeleteAddress(ctx context.Context, userID, addressID uuid.UUID) error {
	return s.repo.DeleteAddress(ctx, userID, addressID)
}

func (s *Service) newUser(email, name, password, role string) (domain.User, error) {
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return domain.User{}, err
	}
	return domain.User{ID: id.New(), Email: email, Name: name, Role: role, PasswordHash: hash}, nil
}

func (s *Service) startSession(ctx context.Context, u domain.User) (Session, error) {
	access, err := s.tokens.Issue(u.ID, u.Role)
	if err != nil {
		return Session{}, err
	}
	raw, hash, err := newRefreshToken()
	if err != nil {
		return Session{}, err
	}
	exp := time.Now().Add(s.refreshTTL)
	if err := s.repo.SaveRefreshToken(ctx, domain.RefreshToken{ID: id.New(), UserID: u.ID, ExpiresAt: exp}, hash); err != nil {
		return Session{}, err
	}
	return Session{User: u, AccessToken: access, RefreshToken: raw, RefreshExpiresAt: exp}, nil
}

func newRefreshToken() (raw string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", nil, err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, hashToken(raw), nil
}

func hashToken(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}

// PurgeTokens deletes refresh tokens that expired or were revoked more than a
// week ago (long enough for theft detection to still see rotated tokens).
func (s *Service) PurgeTokens(ctx context.Context) (int64, error) {
	return s.repo.PurgeRefreshTokens(ctx, time.Now().Add(-7*24*time.Hour))
}
