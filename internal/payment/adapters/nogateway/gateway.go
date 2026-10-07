// Package nogateway is the "no card payments" provider, for shops that only take
// cash on delivery. Card checkout is never offered, so these methods are a backstop.
package nogateway

import (
	"context"
	"errors"
	"net/http"

	"github.com/NaheedRayan/goat-architecture/internal/payment/domain"
)

var errNoCards = errors.New("card payments are not configured")

type Gateway struct{}

func (Gateway) Name() string { return "none" }

func (Gateway) Checkout(context.Context, domain.Payment) (domain.Session, error) {
	return domain.Session{}, errNoCards
}

func (Gateway) ParseWebhook(http.Header, []byte) (domain.WebhookEvent, error) {
	return domain.WebhookEvent{}, domain.ErrInvalidSignature
}

func (Gateway) Refund(context.Context, domain.Payment) (string, error) { return "", errNoCards }
