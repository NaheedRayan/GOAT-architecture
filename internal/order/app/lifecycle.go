package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/order/domain"
)

// ---- cancel ----

// CancelByCustomer lets a customer cancel until the warehouse has picked the order up.
// A card payment already taken is refunded and the stock goes back on the shelf.
func (s *Service) CancelByCustomer(ctx context.Context, userID, orderID uuid.UUID) error {
	return s.Tx.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.Repo.GetForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		if o.UserID != userID {
			return domain.ErrNotFound
		}
		if !o.CanCustomerCancel() {
			return domain.ErrInvalidTransition
		}
		return s.cancelLocked(ctx, &o, "Cancelled by you", nil)
	})
}

// CancelByStaff cancels an order that has not been picked up yet.
func (s *Service) CancelByStaff(ctx context.Context, actor, orderID uuid.UUID, reason string) error {
	return s.Tx.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.Repo.GetForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		detail := "Cancelled by the store"
		if r := strings.TrimSpace(reason); r != "" {
			detail += ": " + clip(r, 300)
		}
		return s.cancelLocked(ctx, &o, detail, &actor)
	})
}

// cancelLocked cancels an awaiting_payment or paid order. The caller holds the row lock and a transaction.
// It returns ErrPaymentReceived when the shopper paid but that has not been processed yet: the order
// must then complete normally rather than be cancelled underneath a successful payment.
func (s *Service) cancelLocked(ctx context.Context, o *domain.Order, detail string, actor *uuid.UUID) error {
	switch o.Status {
	case domain.StatusAwaitingPayment:
		if o.PaymentID != nil {
			status, err := s.Payments.Fail(ctx, *o.PaymentID)
			if err != nil {
				return err
			}
			if status == "succeeded" {
				return domain.ErrPaymentReceived
			}
		}
		if err := o.Transition(domain.StatusCancelled); err != nil {
			return err
		}
		if err := s.Inventory.Release(ctx, o.ID); err != nil {
			return err
		}
	case domain.StatusPaid:
		if o.PaymentID != nil {
			if o.PaymentMethod == domain.MethodCard {
				if err := s.Payments.Refund(ctx, *o.PaymentID); err != nil {
					return err
				}
				o.RefundCents = o.TotalCents
			} else if _, err := s.Payments.Fail(ctx, *o.PaymentID); err != nil { // cash on delivery: nothing was collected
				return err
			}
		}
		if err := o.Transition(domain.StatusCancelled); err != nil {
			return err
		}
		if err := s.Inventory.Return(ctx, o.ID); err != nil {
			return err
		}
	default:
		return o.Transition(domain.StatusCancelled) // reports the invalid transition
	}
	if err := s.Promotions.Release(ctx, o.ID); err != nil {
		return err
	}
	now := time.Now()
	o.CancelledAt = &now
	if err := s.Repo.Save(ctx, *o); err != nil {
		return err
	}
	if err := s.Repo.AddEvent(ctx, o.ID, EvCancelled, detail, actor); err != nil {
		return err
	}
	s.notifyCancelled(ctx, *o)
	return nil
}

// ---- refund ----

// Refund returns the money for an order that was confirmed (any stage up to delivered).
// restock puts the goods back on the shelf; leave it off for damaged or unreturned items.
func (s *Service) Refund(ctx context.Context, actor, orderID uuid.UUID, restock bool, reason string) error {
	return s.Tx.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.Repo.GetForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		detail := "Refunded"
		if r := strings.TrimSpace(reason); r != "" {
			detail += ": " + clip(r, 300)
		}
		return s.refundLocked(ctx, &o, restock, detail, &actor)
	})
}

func (s *Service) refundLocked(ctx context.Context, o *domain.Order, restock bool, detail string, actor *uuid.UUID) error {
	if !o.Refundable() {
		return o.Transition(domain.StatusRefunded) // reports the invalid transition
	}
	if o.PaymentID != nil {
		switch {
		case o.PaymentMethod == domain.MethodCard, o.Status == domain.StatusDelivered:
			// A card payment, or cash already collected on delivery: the money goes back to the customer.
			if err := s.Payments.Refund(ctx, *o.PaymentID); err != nil {
				return err
			}
			o.RefundCents = o.TotalCents
		default:
			// Cash on delivery before delivery: nothing was collected, so there is nothing to return.
			if _, err := s.Payments.Fail(ctx, *o.PaymentID); err != nil {
				return err
			}
		}
	}
	if err := o.Transition(domain.StatusRefunded); err != nil {
		return err
	}
	if restock {
		if err := s.Inventory.Return(ctx, o.ID); err != nil {
			return err
		}
	}
	now := time.Now()
	o.RefundedAt = &now
	if err := s.Repo.Save(ctx, *o); err != nil {
		return err
	}
	if err := s.Repo.AddEvent(ctx, o.ID, EvRefunded, detail, actor); err != nil {
		return err
	}
	s.notifyRefunded(ctx, *o)
	return nil
}

// ---- fulfilment ----

// Ship records dispatch with optional tracking details. An order that has not been
// picked up yet (still "paid") is moved through fulfilling first.
func (s *Service) Ship(ctx context.Context, actor, orderID uuid.UUID, t domain.Tracking) error {
	t = domain.Tracking{Carrier: strings.TrimSpace(t.Carrier), Number: strings.TrimSpace(t.Number), URL: strings.TrimSpace(t.URL)}
	if err := t.Validate(); err != nil {
		return err
	}
	return s.Tx.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.Repo.GetForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		if o.Status == domain.StatusPaid {
			if err := o.Transition(domain.StatusFulfilling); err != nil {
				return err
			}
			if err := s.Repo.AddEvent(ctx, o.ID, EvFulfilling, "We are preparing your order", &actor); err != nil {
				return err
			}
		}
		if err := o.Transition(domain.StatusShipped); err != nil {
			return err
		}
		now := time.Now()
		o.Tracking, o.ShippedAt = t, &now
		if err := s.Repo.Save(ctx, o); err != nil {
			return err
		}
		detail := "Shipped"
		if !t.Empty() {
			detail += trackingSuffix(t)
		}
		if err := s.Repo.AddEvent(ctx, o.ID, EvShipped, detail, &actor); err != nil {
			return err
		}
		s.notifyShipped(ctx, o)
		return nil
	})
}

// Deliver marks an order delivered. For cash on delivery this is when the cash is collected.
func (s *Service) Deliver(ctx context.Context, actor, orderID uuid.UUID) error {
	return s.Tx.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.Repo.GetForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		if err := o.Transition(domain.StatusDelivered); err != nil {
			return err
		}
		if o.PaymentMethod == domain.MethodCOD && o.PaymentID != nil {
			if err := s.Payments.CollectCOD(ctx, *o.PaymentID); err != nil {
				return err
			}
		}
		now := time.Now()
		o.DeliveredAt = &now
		if err := s.Repo.Save(ctx, o); err != nil {
			return err
		}
		if err := s.Repo.AddEvent(ctx, o.ID, EvDelivered, "Delivered", &actor); err != nil {
			return err
		}
		s.notifyDelivered(ctx, o)
		return nil
	})
}

// SetNote stores staff's internal note on an order (never shown to the customer).
func (s *Service) SetNote(ctx context.Context, actor, orderID uuid.UUID, note string) error {
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 2000 {
		return domain.ValidationError("the note is too long (2000 characters max)")
	}
	return s.Tx.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.Repo.GetForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		o.AdminNote = note
		return s.Repo.Save(ctx, o)
	})
}

// ---- returns ----

// RequestReturn lets a customer ask to send goods back, once, within the return window.
func (s *Service) RequestReturn(ctx context.Context, userID, orderID uuid.UUID, reason string) error {
	reason = strings.TrimSpace(reason)
	switch {
	case reason == "":
		return domain.ValidationError("please tell us why you are returning the order")
	case len([]rune(reason)) > 500:
		return domain.ValidationError("the reason is too long (500 characters max)")
	}
	return s.Tx.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.Repo.GetForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		if o.UserID != userID {
			return domain.ErrNotFound
		}
		now := time.Now()
		if !o.CanRequestReturn(now, s.Settings.ReturnWindowDays) {
			return domain.ErrReturnWindow
		}
		o.Return = domain.Return{Status: domain.ReturnRequested, Reason: reason, RequestedAt: &now}
		if err := s.Repo.Save(ctx, o); err != nil {
			return err
		}
		if err := s.Repo.AddEvent(ctx, o.ID, EvReturnRequested, "Return requested", nil); err != nil {
			return err
		}
		s.notifyReturnRequested(ctx, o)
		return nil
	})
}

// ResolveReturn approves (refunding the order) or rejects a customer's return request.
func (s *Service) ResolveReturn(ctx context.Context, actor, orderID uuid.UUID, approve bool, note string, restock bool) error {
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 500 {
		return domain.ValidationError("the reply is too long (500 characters max)")
	}
	return s.Tx.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.Repo.GetForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		if o.Return.Status != domain.ReturnRequested {
			return domain.ErrInvalidTransition
		}
		o.Return.Note = note
		if approve {
			if err := s.refundLocked(ctx, &o, restock, "Refunded: return approved", &actor); err != nil {
				return err
			}
			o.Return.Status = domain.ReturnApproved
			if err := s.Repo.Save(ctx, o); err != nil {
				return err
			}
			return s.Repo.AddEvent(ctx, o.ID, EvReturnApproved, "Return approved", &actor)
		}
		o.Return.Status = domain.ReturnRejected
		if err := s.Repo.Save(ctx, o); err != nil {
			return err
		}
		if err := s.Repo.AddEvent(ctx, o.ID, EvReturnRejected, "Return declined", &actor); err != nil {
			return err
		}
		s.notifyReturnRejected(ctx, o)
		return nil
	})
}

// ---- event handlers (payments, stock) ----

// OnPaymentSucceeded confirms the order and makes its stock permanent.
func (s *Service) OnPaymentSucceeded(ctx context.Context, orderID uuid.UUID) error {
	return s.ignoreMissing("payment.succeeded", orderID, s.onPaymentSucceeded(ctx, orderID))
}

func (s *Service) onPaymentSucceeded(ctx context.Context, orderID uuid.UUID) error {
	return s.Tx.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.Repo.GetForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		switch o.Status {
		case domain.StatusAwaitingPayment:
			if err := s.Inventory.Commit(ctx, orderID); errors.Is(err, domain.ErrStockLost) {
				// The hold expired just before the money arrived. Do not sell stock we
				// no longer hold: cancel and flag for a manual refund.
				s.Log.Error("payment succeeded but stock hold was lost; refund required", "order_id", orderID)
				if terr := o.Transition(domain.StatusCancelled); terr != nil {
					return terr
				}
				now := time.Now()
				o.CancelledAt = &now
				if err := s.Repo.Save(ctx, o); err != nil {
					return err
				}
				return s.Repo.AddEvent(ctx, orderID, EvCancelled, "Cancelled: the items sold out while your payment was processing. A refund is on its way.", nil)
			} else if err != nil {
				return err
			}
			if err := o.Transition(domain.StatusPaid); err != nil {
				return err
			}
			if err := s.Repo.Save(ctx, o); err != nil {
				return err
			}
			if err := s.Repo.AddEvent(ctx, orderID, EvPaid, "Payment received", nil); err != nil {
				return err
			}
			s.notifyConfirmed(ctx, o)
		case domain.StatusCancelled:
			// Money arrived for an order we already cancelled: needs a manual refund.
			s.Log.Error("payment succeeded for cancelled order; refund required", "order_id", orderID)
		}
		return nil // already confirmed or beyond (cash on delivery, redelivered event)
	})
}

func (s *Service) OnPaymentFailed(ctx context.Context, orderID uuid.UUID) error {
	return s.ignoreMissing("payment.failed", orderID, ignoreSettled(s.cancelIfUnpaid(ctx, orderID, "Payment failed")))
}

func (s *Service) OnReservationExpired(ctx context.Context, orderID uuid.UUID) error {
	return s.ignoreMissing("inventory.reservation_expired", orderID, ignoreSettled(s.cancelIfUnpaid(ctx, orderID, "Cancelled: not paid in time")))
}

// cancelIfUnpaid cancels an order that is still awaiting payment, and does nothing for any other state.
func (s *Service) cancelIfUnpaid(ctx context.Context, orderID uuid.UUID, detail string) error {
	return s.Tx.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.Repo.GetForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		if o.Status != domain.StatusAwaitingPayment {
			return nil
		}
		return s.cancelLocked(ctx, &o, detail, nil)
	})
}

// ignoreSettled makes cancellation events idempotent: an order that is already
// paid or whose payment just arrived has nothing left to cancel.
func ignoreSettled(err error) error {
	if errors.Is(err, domain.ErrInvalidTransition) || errors.Is(err, domain.ErrPaymentReceived) {
		return nil
	}
	return err
}

// ignoreMissing turns "order not found" into success: retrying an event for an
// order that does not exist can never help, so it must not clog the queue.
func (s *Service) ignoreMissing(event string, orderID uuid.UUID, err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		s.Log.Warn("event for unknown order dropped", "event", event, "order_id", orderID)
		return nil
	}
	return err
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func trackingSuffix(t domain.Tracking) string {
	var parts []string
	if t.Carrier != "" {
		parts = append(parts, "via "+t.Carrier)
	}
	if t.Number != "" {
		parts = append(parts, "tracking "+t.Number)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
