// Package auth issues and verifies JWT access tokens and carries the
// authenticated identity through the request context. Tokens are signed with
// Ed25519 so other services can verify them with only the public key.
package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	RoleCustomer = "customer"
	RoleStaff    = "staff"
	RoleAdmin    = "admin"
	issuer       = "goat"
)

type Claims struct {
	UserID uuid.UUID
	Role   string
}

type tokenClaims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

type Signer struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
	ttl  time.Duration
}

// NewSigner builds a signer from a base64 32-byte seed. An empty seed creates
// an ephemeral key and reports ephemeral=true (tokens die on restart).
func NewSigner(seedB64 string, ttl time.Duration) (s *Signer, ephemeral bool, err error) {
	var seed []byte
	if seedB64 == "" {
		seed = make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, false, err
		}
		ephemeral = true
	} else {
		seed, err = base64.StdEncoding.DecodeString(seedB64)
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, false, errors.New("JWT_SEED must be base64 of 32 bytes")
		}
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return &Signer{priv: priv, pub: priv.Public().(ed25519.PublicKey), ttl: ttl}, ephemeral, nil
}

func (s *Signer) Issue(userID uuid.UUID, role string) (string, error) {
	now := time.Now()
	t := jwt.NewWithClaims(jwt.SigningMethodEdDSA, tokenClaims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
		},
	})
	return t.SignedString(s.priv)
}

func (s *Signer) Verify(token string) (Claims, error) {
	var tc tokenClaims
	_, err := jwt.ParseWithClaims(token, &tc, func(*jwt.Token) (any, error) { return s.pub, nil },
		jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(issuer), jwt.WithExpirationRequired())
	if err != nil {
		return Claims{}, err
	}
	id, err := uuid.Parse(tc.Subject)
	if err != nil {
		return Claims{}, fmt.Errorf("bad subject: %w", err)
	}
	return Claims{UserID: id, Role: tc.Role}, nil
}

type ctxKey struct{}

func WithClaims(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

func FromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(Claims)
	return c, ok
}

// Require redirects anonymous visitors to the login page.
func Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := FromContext(r.Context()); !ok {
			target := "/login"
			if r.Method == http.MethodGet {
				target += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAnyRole allows users holding any of the given roles (after Require).
func RequireAnyRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, _ := FromContext(r.Context())
			for _, role := range roles {
				if c.Role == role {
					next.ServeHTTP(w, r)
					return
				}
			}
			http.Error(w, "forbidden", http.StatusForbidden)
		}))
	}
}

// RequireRole allows only users with the given role (after Require).
func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c, _ := FromContext(r.Context()); c.Role != role {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}
