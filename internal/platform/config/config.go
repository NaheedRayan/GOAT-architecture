// Package config loads application configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	Env      string `env:"APP_ENV" envDefault:"development"`
	HTTPAddr string `env:"HTTP_ADDR" envDefault:":8080"`
	// PublicURL is the externally visible site URL (https://shop.example.com). Needed when a
	// proxy rewrites the Host header, so form posts from the real site are not rejected.
	PublicURL string `env:"PUBLIC_URL"`
	// TrustProxy honours X-Forwarded-For / X-Real-IP. Enable only behind a proxy you control.
	TrustProxy  bool   `env:"TRUST_PROXY" envDefault:"false"`
	DatabaseURL string `env:"DATABASE_URL,required"`
	DBMaxConns  int32  `env:"DB_MAX_CONNS" envDefault:"20"`

	// JWTSeed is a base64 32-byte Ed25519 seed (`openssl rand -base64 32`).
	// Empty in development generates an ephemeral key; required in production.
	JWTSeed         string        `env:"JWT_SEED"`
	AccessTokenTTL  time.Duration `env:"ACCESS_TOKEN_TTL" envDefault:"15m"`
	RefreshTokenTTL time.Duration `env:"REFRESH_TOKEN_TTL" envDefault:"720h"`

	AdminEmail    string `env:"ADMIN_EMAIL"`
	AdminPassword string `env:"ADMIN_PASSWORD"`

	Currency       string        `env:"CURRENCY" envDefault:"USD"`
	ReservationTTL time.Duration `env:"RESERVATION_TTL" envDefault:"15m"`

	WorkersEnabled bool `env:"WORKERS_ENABLED" envDefault:"true"`

	PaymentProvider      string `env:"PAYMENT_PROVIDER" envDefault:"mock"`
	PaymentWebhookSecret string `env:"PAYMENT_WEBHOOK_SECRET"`
}

func (c Config) Production() bool { return c.Env == "production" }

func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

// insecureExamples are values copied from .env.example that must never reach production.
var insecureExamples = []string{"change-me-please", "dev-webhook-secret"}

// Validate rejects configurations that would misbehave or be unsafe, with a message saying what to fix.
func (c Config) Validate() error {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	if !currencyRe.MatchString(c.Currency) {
		add("CURRENCY must be a 3-letter ISO code such as USD (got %q)", c.Currency)
	}
	for name, d := range map[string]time.Duration{
		"ACCESS_TOKEN_TTL": c.AccessTokenTTL, "REFRESH_TOKEN_TTL": c.RefreshTokenTTL, "RESERVATION_TTL": c.ReservationTTL,
	} {
		if d <= 0 {
			add("%s must be positive (got %s)", name, d)
		}
	}
	if c.AccessTokenTTL > 0 && c.RefreshTokenTTL > 0 && c.AccessTokenTTL >= c.RefreshTokenTTL {
		add("ACCESS_TOKEN_TTL must be shorter than REFRESH_TOKEN_TTL")
	}
	if (c.AdminEmail == "") != (c.AdminPassword == "") {
		add("set both ADMIN_EMAIL and ADMIN_PASSWORD, or neither")
	}
	if c.PublicURL != "" {
		if u, err := url.Parse(c.PublicURL); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			add("PUBLIC_URL must look like https://shop.example.com (got %q)", c.PublicURL)
		} else if c.Production() && u.Scheme != "https" {
			add("PUBLIC_URL must be https in production")
		}
	}
	if c.Production() {
		if c.PaymentProvider == "mock" {
			add("PAYMENT_PROVIDER=mock is not allowed in production: its checkout page lets anyone mark an order paid")
		} else if c.PaymentWebhookSecret == "" {
			add("PAYMENT_WEBHOOK_SECRET is required in production")
		}
		if c.JWTSeed == "" {
			add("JWT_SEED is required in production (openssl rand -base64 32)")
		}
		if c.AdminPassword != "" && len(c.AdminPassword) < 12 {
			add("ADMIN_PASSWORD must be at least 12 characters in production")
		}
		for _, v := range []string{c.AdminPassword, c.PaymentWebhookSecret} {
			if slices.Contains(insecureExamples, v) {
				add("an example secret from .env.example is still in use; set a real value")
			}
		}
	}
	if len(problems) > 0 {
		return errors.New("invalid configuration:\n  - " + strings.Join(problems, "\n  - "))
	}
	return nil
}
