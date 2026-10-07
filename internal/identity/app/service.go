// Package app contains identity use cases and the ports they depend on.
package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/identity/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
	"github.com/NaheedRayan/goat-architecture/internal/platform/mail"
)

type Repository interface {
	CreateUser(ctx context.Context, u domain.User) error // returns domain.ErrEmailTaken on duplicates
	UserByEmail(ctx context.Context, email string) (domain.User, error)
	UserByID(ctx context.Context, id uuid.UUID) (domain.User, error)
	ListUsers(ctx context.Context, query, role string, limit, offset int) ([]domain.User, error)
	MarkEmailVerified(ctx context.Context, userID uuid.UUID) error
	SetPassword(ctx context.Context, userID uuid.UUID, hash string) error // also ends guest status
	UpdateName(ctx context.Context, userID uuid.UUID, name string) error
	SetCartReminders(ctx context.Context, userID uuid.UUID, on bool) error
	SetDisabled(ctx context.Context, userID uuid.UUID, disabled bool) error
	SetRole(ctx context.Context, userID uuid.UUID, role string) error
	CountActiveAdmins(ctx context.Context) (int, error)
	DeleteUser(ctx context.Context, userID uuid.UUID) error

	SaveRefreshToken(ctx context.Context, t domain.RefreshToken, hash []byte) error
	// ConsumeRefreshToken atomically revokes a valid token. When the token was
	// already revoked it returns domain.ErrInvalidToken along with the stored record.
	ConsumeRefreshToken(ctx context.Context, hash []byte) (domain.RefreshToken, error)
	LookupRefreshToken(ctx context.Context, hash []byte) (domain.RefreshToken, error)
	RevokeRefreshToken(ctx context.Context, hash []byte) error
	RevokeAllRefreshTokens(ctx context.Context, userID uuid.UUID) error
	PurgeRefreshTokens(ctx context.Context, before time.Time) (int64, error)

	SaveActionToken(ctx context.Context, userID uuid.UUID, kind string, hash []byte, expires time.Time) error
	ConsumeActionToken(ctx context.Context, kind string, hash []byte) (uuid.UUID, error)
	InvalidateActionTokens(ctx context.Context, userID uuid.UUID, kind string) error
	PurgeActionTokens(ctx context.Context, before time.Time) (int64, error)

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

// Mailer queues an email for delivery.
type Mailer interface {
	Send(ctx context.Context, to, subject string, c mail.Content) error
}

type Events interface {
	Publish(ctx context.Context, kind string, payload any) error
}

type Tx interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// EventAccountDeleted is published (payload {"user_id": uuid}) when an account is
// deleted, so other modules can erase or anonymise the user's data.
const EventAccountDeleted = "account.deleted"

const (
	kindReset  = "password_reset"
	kindVerify = "verify_email"

	resetTTL  = time.Hour
	verifyTTL = 48 * time.Hour
	claimTTL  = 72 * time.Hour // guest "set your password" link
)

type Session struct {
	User             domain.User
	AccessToken      string
	RefreshToken     string
	RefreshExpiresAt time.Time
}

// Deps are the collaborators of the identity service.
type Deps struct {
	Repo       Repository
	Hasher     Hasher
	Tokens     TokenIssuer
	RefreshTTL time.Duration
	Mail       Mailer
	Link       func(path string) string // absolute URL for a site path, used in emails
	Events     Events
	Tx         Tx
	SiteName   string
	Log        *slog.Logger
}

type Service struct {
	Deps
	dummyHash  string
	principals principalCache
}

// ErrTooManyAttempts is returned when a rate limit refuses an action.
var ErrTooManyAttempts = errors.New("too many attempts, please wait a moment")

// reuseGrace tolerates parallel requests racing on one refresh token.
const reuseGrace = 30 * time.Second

func NewService(d Deps) *Service {
	dummy, _ := d.Hasher.Hash("not-a-real-password")
	return &Service{Deps: d, dummyHash: dummy}
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
	if err := s.Repo.CreateUser(ctx, u); err != nil {
		if errors.Is(err, domain.ErrEmailTaken) {
			if existing, lerr := s.Repo.UserByEmail(ctx, email); lerr == nil && existing.Guest {
				return Session{}, domain.ErrGuestAccount
			}
		}
		return Session{}, err
	}
	// A failed verification email must not fail the registration; the user can request another.
	if err := s.sendVerification(ctx, u); err != nil {
		s.Log.Warn("verification email not queued", "user_id", u.ID, "err", err)
	}
	return s.startSession(ctx, u)
}

func (s *Service) Login(ctx context.Context, email, password string) (Session, error) {
	u, err := s.Repo.UserByEmail(ctx, strings.TrimSpace(email))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.Hasher.Verify(password, s.dummyHash) // equalise timing
			return Session{}, domain.ErrInvalidCredentials
		}
		return Session{}, err
	}
	if !s.Hasher.Verify(password, u.PasswordHash) {
		return Session{}, domain.ErrInvalidCredentials
	}
	if u.Disabled { // only revealed to someone who knows the password
		return Session{}, domain.ErrAccountDisabled
	}
	return s.startSession(ctx, u)
}

// Refresh rotates the refresh token and issues a new access token.
func (s *Service) Refresh(ctx context.Context, raw string) (Session, error) {
	hash := hashToken(raw)
	t, err := s.Repo.ConsumeRefreshToken(ctx, hash)
	if err != nil {
		// Unknown, expired, or revoked by logout/theft response: never honoured.
		if !errors.Is(err, domain.ErrInvalidToken) || t.RotatedAt == nil || t.RotatedAge == nil || t.Expired {
			return Session{}, domain.ErrInvalidToken
		}
		if *t.RotatedAge > reuseGrace {
			// A token that was exchanged long ago is being replayed: assume theft, end all sessions.
			_ = s.Repo.RevokeAllRefreshTokens(ctx, t.UserID)
			return Session{}, domain.ErrInvalidToken
		}
		// A parallel request just rotated this token. The caller holds a token
		// that was valid seconds ago, so give it an access token only; the browser
		// will receive the new refresh token from the request that won the race.
		u, err := s.Repo.UserByID(ctx, t.UserID)
		if err != nil || u.Disabled {
			return Session{}, domain.ErrInvalidToken
		}
		access, err := s.Tokens.Issue(u.ID, u.Role)
		if err != nil {
			return Session{}, err
		}
		return Session{User: u, AccessToken: access}, nil
	}
	u, err := s.Repo.UserByID(ctx, t.UserID)
	if err != nil || u.Disabled {
		return Session{}, domain.ErrInvalidToken
	}
	return s.startSession(ctx, u)
}

func (s *Service) Logout(ctx context.Context, raw string) error {
	if raw == "" {
		return nil
	}
	return s.Repo.RevokeRefreshToken(ctx, hashToken(raw))
}

// EnsureAdmin creates the bootstrap admin account if it does not exist.
func (s *Service) EnsureAdmin(ctx context.Context, email, password string) error {
	if _, err := s.Repo.UserByEmail(ctx, email); err == nil {
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
	u.EmailVerified = true
	if err := s.Repo.CreateUser(ctx, u); err != nil && !errors.Is(err, domain.ErrEmailTaken) {
		return err
	}
	return nil
}

func (s *Service) ListAddresses(ctx context.Context, userID uuid.UUID) ([]domain.Address, error) {
	return s.Repo.ListAddresses(ctx, userID)
}

func (s *Service) AddAddress(ctx context.Context, a domain.Address) error {
	if err := a.Validate(); err != nil {
		return err
	}
	a.ID = id.New()
	return s.Repo.AddAddress(ctx, a)
}

func (s *Service) DeleteAddress(ctx context.Context, userID, addressID uuid.UUID) error {
	return s.Repo.DeleteAddress(ctx, userID, addressID)
}

func (s *Service) newUser(email, name, password, role string) (domain.User, error) {
	hash, err := s.Hasher.Hash(password)
	if err != nil {
		return domain.User{}, err
	}
	return domain.User{ID: id.New(), Email: email, Name: name, Role: role, PasswordHash: hash}, nil
}

func (s *Service) startSession(ctx context.Context, u domain.User) (Session, error) {
	access, err := s.Tokens.Issue(u.ID, u.Role)
	if err != nil {
		return Session{}, err
	}
	raw, hash, err := newRefreshToken()
	if err != nil {
		return Session{}, err
	}
	exp := time.Now().Add(s.RefreshTTL)
	if err := s.Repo.SaveRefreshToken(ctx, domain.RefreshToken{ID: id.New(), UserID: u.ID, ExpiresAt: exp}, hash); err != nil {
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
	return s.Repo.PurgeRefreshTokens(ctx, time.Now().Add(-7*24*time.Hour))
}
