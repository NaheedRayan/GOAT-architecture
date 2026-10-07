// Package mock is a fake payment provider for development and tests. Its
// checkout page lives in the payment HTTP adapter; the webhook is verified with
// an HMAC-SHA256 signature exactly as a real provider's would be.
package mock

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/payment/domain"
)

const SignatureHeader = "X-Signature"

type Gateway struct{ secret []byte }

func New(webhookSecret string) *Gateway { return &Gateway{secret: []byte(webhookSecret)} }

func (*Gateway) Name() string { return "mock" }

func (*Gateway) Checkout(_ context.Context, p domain.Payment) (domain.Session, error) {
	return domain.Session{URL: "/pay/" + p.ID.String() + "/mock"}, nil
}

// Refund always succeeds in the mock provider.
func (*Gateway) Refund(_ context.Context, p domain.Payment) (string, error) {
	return "mock_refund_" + p.ID.String(), nil
}

// Sign returns the signature a sender must put in SignatureHeader.
func (g *Gateway) Sign(body []byte) string {
	m := hmac.New(sha256.New, g.secret)
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func (g *Gateway) ParseWebhook(h http.Header, body []byte) (domain.WebhookEvent, error) {
	if len(g.secret) == 0 || !hmac.Equal([]byte(g.Sign(body)), []byte(h.Get(SignatureHeader))) {
		return domain.WebhookEvent{}, domain.ErrInvalidSignature
	}
	var in struct {
		PaymentID   uuid.UUID `json:"payment_id"`
		Status      string    `json:"status"`
		ProviderRef string    `json:"provider_ref"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return domain.WebhookEvent{}, err
	}
	return domain.WebhookEvent{PaymentID: in.PaymentID, Succeeded: in.Status == "succeeded", ProviderRef: in.ProviderRef}, nil
}
