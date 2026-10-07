package config

import (
	"strings"
	"testing"
	"time"
)

func valid() Config {
	return Config{
		Env: "development", Currency: "USD", AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: time.Hour,
		ReservationTTL: time.Minute, PaymentProvider: "mock", PaymentMethods: []string{"card", "cod"},
	}
}

func TestValidate(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("a sane dev config was rejected: %v", err)
	}

	prod := valid()
	prod.Env, prod.PaymentProvider, prod.PaymentWebhookSecret, prod.JWTSeed = "production", "stripe", "whsec_real", "seed"
	prod.SMTPAddr, prod.PublicURL = "smtp.example.com:587", "https://shop.example.com"
	if err := prod.Validate(); err != nil {
		t.Fatalf("a sane production config was rejected: %v", err)
	}

	cases := map[string]func(*Config){
		"TAX_RATE_PERCENT":         func(c *Config) { c.TaxRatePercent = 150 },
		"unknown method":           func(c *Config) { c.PaymentMethods = []string{"bitcoin"} },
		"at least one of":          func(c *Config) { c.PaymentMethods = nil },
		"cannot offer card":        func(c *Config) { c.PaymentProvider = "none" },
		"CURRENCY":                 func(c *Config) { c.Currency = "dollars" },
		"RESERVATION_TTL":          func(c *Config) { c.ReservationTTL = 0 },
		"ACCESS_TOKEN_TTL":         func(c *Config) { c.AccessTokenTTL = 2 * time.Hour },
		"ADMIN_EMAIL":              func(c *Config) { c.AdminEmail = "a@b.co" },
		"mock is not allowed":      func(c *Config) { *c = prod; c.PaymentProvider = "mock" },
		"PAYMENT_WEBHOOK_SECRET":   func(c *Config) { *c = prod; c.PaymentWebhookSecret = "" },
		"SMTP_ADDR":                func(c *Config) { *c = prod; c.SMTPAddr = "" },
		"PUBLIC_URL is required":   func(c *Config) { *c = prod; c.PublicURL = "" },
		"ALERT_EMAIL":              func(c *Config) { c.AlertEmail = "nope" },
		"JWT_SEED":                 func(c *Config) { *c = prod; c.JWTSeed = "" },
		"example secret":           func(c *Config) { *c = prod; c.AdminEmail, c.AdminPassword = "a@b.co", "change-me-please" },
		"at least 12":              func(c *Config) { *c = prod; c.AdminEmail, c.AdminPassword = "a@b.co", "short-pw" },
		"PUBLIC_URL must be https": func(c *Config) { *c = prod; c.PublicURL = "http://shop.example.com" },
		"PUBLIC_URL must look":     func(c *Config) { c.PublicURL = "shop" },
	}
	for want, mutate := range cases {
		c := valid()
		mutate(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want an error mentioning %q, got %v", want, err)
		}
	}
}
