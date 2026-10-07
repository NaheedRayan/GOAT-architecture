// Package postgres implements the identity repository with sqlc-generated queries.
package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/identity/adapters/postgres/sqlcgen"
	"github.com/NaheedRayan/goat-architecture/internal/identity/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
)

type Repo struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) q(ctx context.Context) *sqlcgen.Queries { return sqlcgen.New(db.Q(ctx, r.pool)) }

func toUser(u sqlcgen.IdentityUser) domain.User {
	return domain.User{ID: u.ID, Email: u.Email, Name: u.Name, Role: u.Role, PasswordHash: u.PasswordHash, CreatedAt: u.CreatedAt,
		EmailVerified: u.EmailVerifiedAt != nil, Guest: u.Guest, Disabled: u.DisabledAt != nil, CartReminders: u.CartReminders}
}

func (r *Repo) CreateUser(ctx context.Context, u domain.User) error {
	err := r.q(ctx).CreateUser(ctx, sqlcgen.CreateUserParams{
		ID: u.ID, Email: u.Email, PasswordHash: u.PasswordHash, Name: u.Name, Role: u.Role, Guest: u.Guest, EmailVerifiedAt: verifiedAt(u),
	})
	if db.IsUniqueViolation(err) {
		return domain.ErrEmailTaken
	}
	return err
}

func (r *Repo) UserByEmail(ctx context.Context, email string) (domain.User, error) {
	u, err := r.q(ctx).GetUserByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, domain.ErrNotFound
	}
	return toUser(u), err
}

func (r *Repo) UserByID(ctx context.Context, userID uuid.UUID) (domain.User, error) {
	u, err := r.q(ctx).GetUserByID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, domain.ErrNotFound
	}
	return toUser(u), err
}

func (r *Repo) SaveRefreshToken(ctx context.Context, t domain.RefreshToken, hash []byte) error {
	return r.q(ctx).InsertRefreshToken(ctx, sqlcgen.InsertRefreshTokenParams{
		ID: t.ID, UserID: t.UserID, TokenHash: hash, ExpiresAt: t.ExpiresAt,
	})
}

func toToken(t sqlcgen.IdentityRefreshToken) domain.RefreshToken {
	return domain.RefreshToken{ID: t.ID, UserID: t.UserID, ExpiresAt: t.ExpiresAt, RevokedAt: t.RevokedAt, RotatedAt: t.RotatedAt}
}

func (r *Repo) ConsumeRefreshToken(ctx context.Context, hash []byte) (domain.RefreshToken, error) {
	t, err := r.q(ctx).ConsumeRefreshToken(ctx, hash)
	if err == nil {
		return toToken(t), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.RefreshToken{}, err
	}
	// Unknown, expired or already revoked: return what we know for reuse detection.
	old, lerr := r.q(ctx).GetRefreshTokenByHash(ctx, hash)
	if lerr != nil {
		return domain.RefreshToken{}, domain.ErrInvalidToken
	}
	tok := domain.RefreshToken{ID: old.ID, UserID: old.UserID, ExpiresAt: old.ExpiresAt, RevokedAt: old.RevokedAt, RotatedAt: old.RotatedAt, Expired: old.Expired}
	if old.RotatedAgeSecs >= 0 {
		age := time.Duration(old.RotatedAgeSecs * float64(time.Second))
		tok.RotatedAge = &age
	}
	return tok, domain.ErrInvalidToken
}

func (r *Repo) LookupRefreshToken(ctx context.Context, hash []byte) (domain.RefreshToken, error) {
	t, err := r.q(ctx).GetRefreshTokenByHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RefreshToken{}, domain.ErrInvalidToken
	}
	return domain.RefreshToken{ID: t.ID, UserID: t.UserID, ExpiresAt: t.ExpiresAt, RevokedAt: t.RevokedAt, RotatedAt: t.RotatedAt, Expired: t.Expired}, err
}

func (r *Repo) RevokeRefreshToken(ctx context.Context, hash []byte) error {
	return r.q(ctx).RevokeRefreshToken(ctx, hash)
}

func (r *Repo) RevokeAllRefreshTokens(ctx context.Context, userID uuid.UUID) error {
	return r.q(ctx).RevokeAllRefreshTokens(ctx, userID)
}

func (r *Repo) ListAddresses(ctx context.Context, userID uuid.UUID) ([]domain.Address, error) {
	rows, err := r.q(ctx).ListAddresses(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Address, len(rows))
	for i, a := range rows {
		out[i] = domain.Address{ID: a.ID, UserID: a.UserID, FullName: a.FullName, Phone: a.Phone, Line1: a.Line1,
			Line2: a.Line2, City: a.City, PostalCode: a.PostalCode, Country: a.Country, IsDefault: a.IsDefault}
	}
	return out, nil
}

func (r *Repo) AddAddress(ctx context.Context, a domain.Address) error {
	if a.ID == uuid.Nil {
		a.ID = id.New()
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := sqlcgen.New(tx)
	if a.IsDefault {
		if err := q.ClearDefaultAddress(ctx, a.UserID); err != nil {
			return err
		}
	}
	if err := q.InsertAddress(ctx, sqlcgen.InsertAddressParams{
		ID: a.ID, UserID: a.UserID, FullName: a.FullName, Phone: a.Phone, Line1: a.Line1, Line2: a.Line2,
		City: a.City, PostalCode: a.PostalCode, Country: a.Country, IsDefault: a.IsDefault,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repo) DeleteAddress(ctx context.Context, userID, addressID uuid.UUID) error {
	return r.q(ctx).DeleteAddress(ctx, sqlcgen.DeleteAddressParams{ID: addressID, UserID: userID})
}

func (r *Repo) PurgeRefreshTokens(ctx context.Context, before time.Time) (int64, error) {
	return r.q(ctx).PurgeRefreshTokens(ctx, before)
}

func verifiedAt(u domain.User) *time.Time {
	if !u.EmailVerified {
		return nil
	}
	now := time.Now()
	return &now
}

func (r *Repo) MarkEmailVerified(ctx context.Context, userID uuid.UUID) error {
	return r.q(ctx).MarkEmailVerified(ctx, userID)
}

func (r *Repo) SetPassword(ctx context.Context, userID uuid.UUID, hash string) error {
	return r.q(ctx).SetPassword(ctx, sqlcgen.SetPasswordParams{ID: userID, PasswordHash: hash})
}

func (r *Repo) UpdateName(ctx context.Context, userID uuid.UUID, name string) error {
	return r.q(ctx).UpdateUserName(ctx, sqlcgen.UpdateUserNameParams{ID: userID, Name: name})
}

func (r *Repo) SetDisabled(ctx context.Context, userID uuid.UUID, disabled bool) error {
	var at *time.Time
	if disabled {
		now := time.Now()
		at = &now
	}
	return r.q(ctx).SetUserDisabled(ctx, sqlcgen.SetUserDisabledParams{ID: userID, DisabledAt: at})
}

func (r *Repo) SetRole(ctx context.Context, userID uuid.UUID, role string) error {
	return r.q(ctx).SetUserRole(ctx, sqlcgen.SetUserRoleParams{ID: userID, Role: role})
}

func (r *Repo) CountActiveAdmins(ctx context.Context) (int, error) {
	n, err := r.q(ctx).CountActiveAdmins(ctx)
	return int(n), err
}

func (r *Repo) ListUsers(ctx context.Context, query, role string, limit, offset int) ([]domain.User, error) {
	rows, err := r.q(ctx).ListUsers(ctx, sqlcgen.ListUsersParams{
		Query: query, LikePattern: likePattern(query), Role: role, PageLimit: int32(limit), PageOffset: int32(offset),
	})
	if err != nil {
		return nil, err
	}
	out := make([]domain.User, len(rows))
	for i, u := range rows {
		out[i] = toUser(u)
	}
	return out, nil
}

func (r *Repo) DeleteUser(ctx context.Context, userID uuid.UUID) error {
	return r.q(ctx).DeleteUser(ctx, userID)
}

func (r *Repo) SaveActionToken(ctx context.Context, userID uuid.UUID, kind string, hash []byte, expires time.Time) error {
	return r.q(ctx).InsertActionToken(ctx, sqlcgen.InsertActionTokenParams{ID: id.New(), UserID: userID, Kind: kind, TokenHash: hash, ExpiresAt: expires})
}

// ConsumeActionToken burns a valid token and returns its owner; domain.ErrInvalidToken otherwise.
func (r *Repo) ConsumeActionToken(ctx context.Context, kind string, hash []byte) (uuid.UUID, error) {
	t, err := r.q(ctx).ConsumeActionToken(ctx, sqlcgen.ConsumeActionTokenParams{TokenHash: hash, Kind: kind})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, domain.ErrInvalidToken
	}
	return t.UserID, err
}

func (r *Repo) InvalidateActionTokens(ctx context.Context, userID uuid.UUID, kind string) error {
	return r.q(ctx).InvalidateActionTokens(ctx, sqlcgen.InvalidateActionTokensParams{UserID: userID, Kind: kind})
}

func (r *Repo) PurgeActionTokens(ctx context.Context, before time.Time) (int64, error) {
	return r.q(ctx).PurgeActionTokens(ctx, before)
}

// likePattern escapes LIKE wildcards so a search for "50%" is literal.
func likePattern(q string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
}

func (r *Repo) SetCartReminders(ctx context.Context, userID uuid.UUID, on bool) error {
	return r.q(ctx).SetCartReminders(ctx, sqlcgen.SetCartRemindersParams{ID: userID, CartReminders: on})
}
