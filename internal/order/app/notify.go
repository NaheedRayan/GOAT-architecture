package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/NaheedRayan/goat-architecture/internal/order/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/mail"
	"github.com/NaheedRayan/goat-architecture/internal/platform/money"
)

// Emails are queued inside the business transaction (via the outbox), so a
// customer is told about exactly the changes that were committed. A failure to
// queue never fails the business operation itself.

func shortOrderID(o domain.Order) string {
	s := o.ID.String()
	return "#" + s[len(s)-8:]
}

func (s *Service) fmtMoney(cents int64, o domain.Order) string {
	return money.Format(cents, o.Currency)
}

func (s *Service) recipient(ctx context.Context, o domain.Order) (to, firstName string, ok bool) {
	to = o.ContactEmail
	name := o.Shipping.FullName
	if to == "" {
		c, err := s.Customers.Contact(ctx, o.UserID)
		if err != nil || c.Email == "" {
			return "", "", false
		}
		to = c.Email
		if name == "" {
			name = c.Name
		}
	}
	if f := strings.Fields(name); len(f) > 0 {
		firstName = f[0]
	} else {
		firstName = "there"
	}
	return to, firstName, true
}

func (s *Service) send(ctx context.Context, o domain.Order, subject string, build func(first string) mail.Content) {
	to, first, ok := s.recipient(ctx, o)
	if !ok {
		return
	}
	if err := s.Mail.Send(ctx, to, subject, build(first)); err != nil {
		s.Log.Warn("order email not queued", "order_id", o.ID, "subject", subject, "err", err)
	}
}

func (s *Service) orderButton(o domain.Order) *mail.Button {
	return &mail.Button{Label: "View your order", URL: s.Settings.Link("/orders/" + o.ID.String())}
}

func (s *Service) summaryLines(o domain.Order) []string {
	var lines []string
	for _, it := range o.Items {
		lines = append(lines, fmt.Sprintf("%s × %d — %s", it.DisplayName(), it.Quantity, s.fmtMoney(it.SubtotalCents(), o)))
	}
	lines = append(lines, "Subtotal: "+s.fmtMoney(o.SubtotalCents, o))
	if o.DiscountCents > 0 {
		label := "Discount"
		if o.CouponCode != "" {
			label += " (" + o.CouponCode + ")"
		}
		lines = append(lines, label+": −"+s.fmtMoney(o.DiscountCents, o))
	}
	if o.ShippingMethod != "" || o.ShippingCents > 0 {
		lines = append(lines, "Delivery: "+s.fmtMoney(o.ShippingCents, o))
	}
	if o.TaxCents > 0 {
		label := s.Settings.TaxLabel
		if o.TaxInclusive {
			label += " (included)"
		}
		lines = append(lines, label+": "+s.fmtMoney(o.TaxCents, o))
	}
	lines = append(lines, "Total: "+s.fmtMoney(o.TotalCents, o))
	return lines
}

func (s *Service) notifyConfirmed(ctx context.Context, o domain.Order) {
	s.send(ctx, o, "Order "+shortOrderID(o)+" confirmed", func(first string) mail.Content {
		pay := "Your payment was received."
		if o.PaymentMethod == domain.MethodCOD {
			pay = "You chose cash on delivery: please pay the courier when your order arrives."
		}
		addr := o.Shipping
		return mail.Content{
			Heading: "Thanks for your order, " + first,
			Paragraphs: []string{
				"We have received order " + shortOrderID(o) + ". " + pay,
				"Delivering to: " + strings.Join(nonEmpty(addr.FullName, addr.Line1, addr.Line2, addr.City+" "+addr.PostalCode, addr.Country), ", "),
			},
			Lines:    s.summaryLines(o),
			Button:   s.orderButton(o),
			Footnote: "You can cancel the order from your account until we start preparing it.",
		}
	})
}

func (s *Service) notifyShipped(ctx context.Context, o domain.Order) {
	s.send(ctx, o, "Order "+shortOrderID(o)+" is on its way", func(first string) mail.Content {
		paras := []string{"Good news, " + first + ": your order " + shortOrderID(o) + " has shipped."}
		if o.Tracking.Carrier != "" {
			paras = append(paras, "Carrier: "+o.Tracking.Carrier)
		}
		if o.Tracking.Number != "" {
			paras = append(paras, "Tracking number: "+o.Tracking.Number)
		}
		c := mail.Content{Heading: "Your order has shipped", Paragraphs: paras, Button: s.orderButton(o)}
		if o.Tracking.URL != "" {
			c.Button = &mail.Button{Label: "Track your parcel", URL: o.Tracking.URL}
		}
		return c
	})
}

func (s *Service) notifyDelivered(ctx context.Context, o domain.Order) {
	s.send(ctx, o, "Order "+shortOrderID(o)+" was delivered", func(first string) mail.Content {
		return mail.Content{
			Heading:    "Delivered",
			Paragraphs: []string{fmt.Sprintf("Your order %s was delivered. If anything is wrong you can request a return within %d days from your order page.", shortOrderID(o), s.Settings.ReturnWindowDays)},
			Button:     s.orderButton(o),
		}
	})
}

func (s *Service) notifyCancelled(ctx context.Context, o domain.Order) {
	s.send(ctx, o, "Order "+shortOrderID(o)+" was cancelled", func(first string) mail.Content {
		paras := []string{"Your order " + shortOrderID(o) + " was cancelled."}
		if o.RefundCents > 0 {
			paras = append(paras, "We are refunding "+s.fmtMoney(o.RefundCents, o)+" to your original payment method. It can take a few days to appear.")
		}
		return mail.Content{Heading: "Order cancelled", Paragraphs: paras, Button: s.orderButton(o)}
	})
}

func (s *Service) notifyRefunded(ctx context.Context, o domain.Order) {
	s.send(ctx, o, "Order "+shortOrderID(o)+" refunded", func(first string) mail.Content {
		paras := []string{"Your order " + shortOrderID(o) + " was refunded."}
		switch {
		case o.RefundCents > 0 && o.PaymentMethod == domain.MethodCOD:
			paras = append(paras, "We will return "+s.fmtMoney(o.RefundCents, o)+" to you as agreed with our team.")
		case o.RefundCents > 0:
			paras = append(paras, s.fmtMoney(o.RefundCents, o)+" is on its way back to your original payment method. It can take a few days to appear.")
		}
		if o.Return.Note != "" {
			paras = append(paras, "Note from the store: "+o.Return.Note)
		}
		return mail.Content{Heading: "Refund issued", Paragraphs: paras, Button: s.orderButton(o)}
	})
}

func (s *Service) notifyReturnRequested(ctx context.Context, o domain.Order) {
	s.send(ctx, o, "We received your return request for order "+shortOrderID(o), func(first string) mail.Content {
		return mail.Content{
			Heading:    "Return request received",
			Paragraphs: []string{"Thanks, " + first + ". We will review your request and email you with the next steps."},
			Button:     s.orderButton(o),
		}
	})
	if s.Settings.AlertEmail == "" {
		return
	}
	err := s.Mail.Send(ctx, s.Settings.AlertEmail, "Return requested for order "+shortOrderID(o), mail.Content{
		Heading: "Return requested",
		Paragraphs: []string{
			fmt.Sprintf("%s asked to return order %s (%s).", o.Shipping.FullName, shortOrderID(o), s.fmtMoney(o.TotalCents, o)),
			"Reason: " + o.Return.Reason,
		},
		Button: &mail.Button{Label: "Review in admin", URL: s.Settings.Link("/admin/orders/" + o.ID.String())},
	})
	if err != nil {
		s.Log.Warn("return alert not queued", "order_id", o.ID, "err", err)
	}
}

func (s *Service) notifyReturnRejected(ctx context.Context, o domain.Order) {
	s.send(ctx, o, "About your return request for order "+shortOrderID(o), func(first string) mail.Content {
		paras := []string{"We reviewed your return request for order " + shortOrderID(o) + " and are unable to accept it."}
		if o.Return.Note != "" {
			paras = append(paras, "Note from the store: "+o.Return.Note)
		}
		return mail.Content{Heading: "Return request declined", Paragraphs: paras, Button: s.orderButton(o)}
	})
}

func nonEmpty(parts ...string) []string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
