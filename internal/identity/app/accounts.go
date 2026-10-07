package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/identity/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
	"github.com/NaheedRayan/goat-architecture/internal/platform/mail"
)

// ---- email verification ----

func (s *Service) sendVerification(ctx context.Context, u domain.User) error {
	if u.EmailVerified {
		return nil
	}
	raw, err := s.newActionToken(ctx, u.ID, kindVerify, verifyTTL)
	if err != nil {
		return err
	}
	return s.Mail.Send(ctx, u.Email, "Confirm your email address", mail.Content{
		Heading:    "Confirm your email",
		Paragraphs: []string{"Hi " + firstName(u.Name) + ", please confirm that this is your email address so we can reach you about your orders."},
		Button:     &mail.Button{Label: "Confirm email", URL: s.Link("/verify?token=" + raw)},
		Footnote:   "If you did not create an account with " + s.SiteName + ", you can ignore this email.",
	})
}

// ResendVerification emails a fresh confirmation link to a signed-in user.
func (s *Service) ResendVerification(ctx context.Context, userID uuid.UUID) error {
	u, err := s.Repo.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	return s.sendVerification(ctx, u)
}

// VerifyEmail consumes a confirmation link.
func (s *Service) VerifyEmail(ctx context.Context, rawToken string) error {
	uid, err := s.Repo.ConsumeActionToken(ctx, kindVerify, hashToken(rawToken))
	if err != nil {
		return err
	}
	return s.Repo.MarkEmailVerified(ctx, uid)
}

// ---- password reset ----

// RequestPasswordReset emails a reset link if the address belongs to an active
// account. It reports nothing either way, so it cannot be used to discover which
// emails have accounts.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	u, err := s.Repo.UserByEmail(ctx, strings.TrimSpace(email))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	}
	if u.Disabled {
		return nil
	}
	return s.sendReset(ctx, u, resetTTL, "Reset your password",
		"We received a request to reset your password. This link works once and expires in 1 hour.")
}

func (s *Service) sendReset(ctx context.Context, u domain.User, ttl time.Duration, subject, intro string) error {
	if err := s.Repo.InvalidateActionTokens(ctx, u.ID, kindReset); err != nil { // only the newest link works
		return err
	}
	raw, err := s.newActionToken(ctx, u.ID, kindReset, ttl)
	if err != nil {
		return err
	}
	return s.Mail.Send(ctx, u.Email, subject, mail.Content{
		Heading:    subject,
		Paragraphs: []string{"Hi " + firstName(u.Name) + ",", intro},
		Button:     &mail.Button{Label: "Choose a new password", URL: s.Link("/reset?token=" + raw)},
		Footnote:   "If you did not ask for this, you can safely ignore this email; your password will not change.",
	})
}

// ResetPassword sets a new password from an emailed link and signs the user in.
func (s *Service) ResetPassword(ctx context.Context, rawToken, newPassword string) (Session, error) {
	if err := domain.ValidatePassword(newPassword); err != nil { // before consuming: a typo must not burn the link
		return Session{}, err
	}
	uid, err := s.Repo.ConsumeActionToken(ctx, kindReset, hashToken(rawToken))
	if err != nil {
		return Session{}, err
	}
	u, err := s.Repo.UserByID(ctx, uid)
	if err != nil || u.Disabled {
		return Session{}, domain.ErrInvalidToken
	}
	hash, err := s.Hasher.Hash(newPassword)
	if err != nil {
		return Session{}, err
	}
	if err := s.Repo.SetPassword(ctx, uid, hash); err != nil {
		return Session{}, err
	}
	_ = s.Repo.MarkEmailVerified(ctx, uid) // the link proved control of the mailbox
	if err := s.Repo.RevokeAllRefreshTokens(ctx, uid); err != nil {
		return Session{}, err
	}
	u.PasswordHash, u.Guest, u.EmailVerified = hash, false, true
	return s.startSession(ctx, u)
}

// ChangePassword updates the password of a signed-in user and replaces all sessions.
func (s *Service) ChangePassword(ctx context.Context, userID uuid.UUID, current, next string) (Session, error) {
	u, err := s.Repo.UserByID(ctx, userID)
	if err != nil {
		return Session{}, err
	}
	if u.PasswordHash == "" || !s.Hasher.Verify(current, u.PasswordHash) {
		return Session{}, domain.ErrWrongPassword
	}
	if err := domain.ValidatePassword(next); err != nil {
		return Session{}, err
	}
	hash, err := s.Hasher.Hash(next)
	if err != nil {
		return Session{}, err
	}
	if err := s.Repo.SetPassword(ctx, userID, hash); err != nil {
		return Session{}, err
	}
	if err := s.Repo.RevokeAllRefreshTokens(ctx, userID); err != nil { // sign out everywhere else
		return Session{}, err
	}
	if err := s.Mail.Send(ctx, u.Email, "Your password was changed", mail.Content{
		Heading:    "Your password was changed",
		Paragraphs: []string{"The password for your " + s.SiteName + " account was just changed. If this was not you, reset your password immediately and contact us."},
		Button:     &mail.Button{Label: "Reset password", URL: s.Link("/forgot")},
	}); err != nil {
		s.Log.Warn("password-change notice not queued", "user_id", userID, "err", err)
	}
	u.PasswordHash = hash
	return s.startSession(ctx, u)
}

// SetCartReminders turns "you left something in your cart" emails on or off for a user.
func (s *Service) SetCartReminders(ctx context.Context, userID uuid.UUID, on bool) error {
	return s.Repo.SetCartReminders(ctx, userID, on)
}

func (s *Service) UpdateProfile(ctx context.Context, userID uuid.UUID, name string) error {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return domain.ValidationError("name is required")
	}
	if len([]rune(name)) > 100 {
		return domain.ValidationError("name is too long (100 characters max)")
	}
	return s.Repo.UpdateName(ctx, userID, name)
}

// ---- guest checkout ----

// GuestCheckout creates a password-less account for someone buying without
// registering and signs them in. An email that already has an account is never
// reused: letting a stranger type an address and land in someone's session would
// expose that person's orders and addresses.
func (s *Service) GuestCheckout(ctx context.Context, email, name string) (Session, error) {
	email, name = strings.TrimSpace(email), strings.Join(strings.Fields(name), " ")
	if err := domain.ValidateEmail(email); err != nil {
		return Session{}, err
	}
	if name == "" {
		return Session{}, domain.ValidationError("name is required")
	}
	if len([]rune(name)) > 100 {
		return Session{}, domain.ValidationError("name is too long (100 characters max)")
	}
	if _, err := s.Repo.UserByEmail(ctx, email); err == nil {
		return Session{}, domain.ErrAccountExists
	} else if !errors.Is(err, domain.ErrNotFound) {
		return Session{}, err
	}
	u := domain.User{ID: id.New(), Email: email, Name: name, Role: auth.RoleCustomer, Guest: true}
	if err := s.Repo.CreateUser(ctx, u); err != nil {
		if errors.Is(err, domain.ErrEmailTaken) { // raced with another request
			return Session{}, domain.ErrAccountExists
		}
		return Session{}, err
	}
	if err := s.sendReset(ctx, u, claimTTL, "Set a password for your account",
		"Thanks for your order. You checked out without an account, so we created one for you. Choose a password to track your orders and check out faster next time."); err != nil {
		s.Log.Warn("guest claim email not queued", "user_id", u.ID, "err", err)
	}
	return s.startSession(ctx, u)
}

// ---- administration ----

func (s *Service) User(ctx context.Context, userID uuid.UUID) (domain.User, error) {
	return s.Repo.UserByID(ctx, userID)
}

func (s *Service) ListUsers(ctx context.Context, query, role string, limit, offset int) ([]domain.User, error) {
	return s.Repo.ListUsers(ctx, strings.TrimSpace(query), role, limit, max(offset, 0))
}

// SetRole changes a user's role. Admins cannot change their own role (so the last
// admin cannot lock everyone out by accident).
func (s *Service) SetRole(ctx context.Context, actorID, targetID uuid.UUID, role string) error {
	if !domain.ValidRole(role) {
		return domain.ValidationError("unknown role")
	}
	if actorID == targetID {
		return domain.ErrSelfChange
	}
	target, err := s.Repo.UserByID(ctx, targetID)
	if err != nil {
		return err
	}
	if target.Role == domain.RoleAdmin && role != domain.RoleAdmin {
		if err := s.requireAnotherAdmin(ctx); err != nil {
			return err
		}
	}
	err = s.Repo.SetRole(ctx, targetID, role)
	s.principals.evict(targetID)
	return err
}

// SetDisabled blocks or unblocks sign-in for an account and ends its sessions.
func (s *Service) SetDisabled(ctx context.Context, actorID, targetID uuid.UUID, disabled bool) error {
	if actorID == targetID {
		return domain.ErrSelfChange
	}
	target, err := s.Repo.UserByID(ctx, targetID)
	if err != nil {
		return err
	}
	if disabled && target.Role == domain.RoleAdmin {
		if err := s.requireAnotherAdmin(ctx); err != nil {
			return err
		}
	}
	err = s.Repo.SetDisabled(ctx, targetID, disabled)
	s.principals.evict(targetID)
	if err != nil {
		return err
	}
	if disabled {
		return s.Repo.RevokeAllRefreshTokens(ctx, targetID)
	}
	return nil
}

func (s *Service) requireAnotherAdmin(ctx context.Context) error {
	n, err := s.Repo.CountActiveAdmins(ctx)
	if err != nil {
		return err
	}
	if n <= 1 {
		return domain.ErrLastAdmin
	}
	return nil
}

// ---- privacy ----

// UserExport is everything identity holds about a person.
type UserExport struct {
	ID            uuid.UUID
	Email         string
	Name          string
	Role          string
	CreatedAt     time.Time
	EmailVerified bool
	Addresses     []domain.Address
}

func (s *Service) Export(ctx context.Context, userID uuid.UUID) (UserExport, error) {
	u, err := s.Repo.UserByID(ctx, userID)
	if err != nil {
		return UserExport{}, err
	}
	addrs, err := s.Repo.ListAddresses(ctx, userID)
	if err != nil {
		return UserExport{}, err
	}
	return UserExport{ID: u.ID, Email: u.Email, Name: u.Name, Role: u.Role, CreatedAt: u.CreatedAt, EmailVerified: u.EmailVerified, Addresses: addrs}, nil
}

// DeleteAccount permanently removes an account after confirming the password.
// Other modules erase or anonymise their data in response to the published event.
func (s *Service) DeleteAccount(ctx context.Context, userID uuid.UUID, password string) error {
	u, err := s.Repo.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if u.PasswordHash == "" || !s.Hasher.Verify(password, u.PasswordHash) {
		return domain.ErrWrongPassword
	}
	if u.Role == domain.RoleAdmin {
		if err := s.requireAnotherAdmin(ctx); err != nil {
			return err
		}
	}
	err = s.Tx.WithTx(ctx, func(ctx context.Context) error {
		if err := s.Events.Publish(ctx, EventAccountDeleted, map[string]uuid.UUID{"user_id": userID}); err != nil {
			return err
		}
		return s.Repo.DeleteUser(ctx, userID) // sessions, addresses and tokens cascade
	})
	s.principals.evict(userID)
	return err
}

// PurgeActionTokens deletes used or expired emailed-link tokens older than a day.
func (s *Service) PurgeActionTokens(ctx context.Context) (int64, error) {
	return s.Repo.PurgeActionTokens(ctx, time.Now().Add(-24*time.Hour))
}

// ---- helpers ----

func (s *Service) newActionToken(ctx context.Context, userID uuid.UUID, kind string, ttl time.Duration) (string, error) {
	raw, hash, err := newRefreshToken() // 256 random bits, same generator as refresh tokens
	if err != nil {
		return "", err
	}
	if err := s.Repo.SaveActionToken(ctx, userID, kind, hash, time.Now().Add(ttl)); err != nil {
		return "", err
	}
	return raw, nil
}

func firstName(full string) string {
	if f := strings.Fields(full); len(f) > 0 {
		return f[0]
	}
	return "there"
}
