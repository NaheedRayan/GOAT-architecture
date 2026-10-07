package domain

import (
	"testing"
	"time"
)

func TestStateMachine(t *testing.T) {
	ok := [][2]string{
		{StatusAwaitingPayment, StatusPaid}, {StatusAwaitingPayment, StatusCancelled},
		{StatusPaid, StatusFulfilling}, {StatusPaid, StatusCancelled}, {StatusPaid, StatusRefunded},
		{StatusFulfilling, StatusShipped}, {StatusFulfilling, StatusRefunded},
		{StatusShipped, StatusDelivered}, {StatusShipped, StatusRefunded}, {StatusDelivered, StatusRefunded},
	}
	for _, c := range ok {
		o := Order{Status: c[0]}
		if err := o.Transition(c[1]); err != nil || o.Status != c[1] {
			t.Errorf("%s → %s should be allowed: %v", c[0], c[1], err)
		}
	}
	bad := [][2]string{
		{StatusAwaitingPayment, StatusRefunded}, {StatusAwaitingPayment, StatusShipped}, {StatusAwaitingPayment, StatusDelivered},
		{StatusFulfilling, StatusCancelled}, {StatusFulfilling, StatusPaid}, {StatusShipped, StatusPaid}, {StatusShipped, StatusCancelled},
		{StatusDelivered, StatusShipped}, {StatusDelivered, StatusCancelled},
		{StatusCancelled, StatusPaid}, {StatusCancelled, StatusRefunded}, {StatusRefunded, StatusPaid}, {StatusRefunded, StatusRefunded},
	}
	for _, c := range bad {
		o := Order{Status: c[0]}
		if err := o.Transition(c[1]); err == nil {
			t.Errorf("%s → %s should be rejected", c[0], c[1])
		}
	}
}

func TestShippingValidate(t *testing.T) {
	a := ShippingAddress{FullName: "A", Phone: "1", Line1: "x", City: "c", PostalCode: "1", Country: "BD"}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	a.City = " "
	if err := a.Validate(); err == nil {
		t.Fatal("blank city should fail")
	}
}

func TestPricing(t *testing.T) {
	cases := []struct {
		name               string
		sub, disc, ship    int64
		bps                int
		inclusive          bool
		wantTax, wantTotal int64
	}{
		{"no tax", 10000, 0, 500, 0, false, 0, 10500},
		{"20% on top of goods and shipping", 10000, 0, 500, 2000, false, 2100, 12600},
		{"discount reduces the taxable amount", 10000, 2000, 500, 2000, false, 1700, 10200},
		{"rounds half up", 1005, 0, 0, 1000, false, 101, 1106},
		{"inclusive: total unchanged, tax extracted", 12000, 0, 0, 2000, true, 2000, 12000},
		{"inclusive with shipping", 10000, 0, 500, 2000, true, 1750, 10500},
		{"discount larger than subtotal never goes negative", 1000, 5000, 0, 2000, false, 0, 0},
		{"free everything", 0, 0, 0, 2000, false, 0, 0},
	}
	for _, c := range cases {
		p := Price(c.sub, c.disc, c.ship, c.bps, c.inclusive)
		if p.TaxCents != c.wantTax || p.TotalCents != c.wantTotal {
			t.Errorf("%s: tax=%d total=%d, want tax=%d total=%d", c.name, p.TaxCents, p.TotalCents, c.wantTax, c.wantTotal)
		}
	}
}

func TestCancelAndReturnRules(t *testing.T) {
	for st, want := range map[string]bool{
		StatusAwaitingPayment: true, StatusPaid: true, StatusFulfilling: false, StatusShipped: false,
		StatusDelivered: false, StatusCancelled: false, StatusRefunded: false,
	} {
		if got := (Order{Status: st}).CanCustomerCancel(); got != want {
			t.Errorf("CanCustomerCancel(%s) = %v, want %v", st, got, want)
		}
	}
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	ago := func(days int) *time.Time { t := now.AddDate(0, 0, -days); return &t }
	cases := []struct {
		name string
		o    Order
		want bool
	}{
		{"shipped, in window", Order{Status: StatusShipped, ShippedAt: ago(3)}, true},
		{"delivered, in window", Order{Status: StatusDelivered, ShippedAt: ago(10), DeliveredAt: ago(2)}, true},
		{"window counts from delivery, not shipping", Order{Status: StatusDelivered, ShippedAt: ago(30), DeliveredAt: ago(1)}, true},
		{"delivered, window over", Order{Status: StatusDelivered, DeliveredAt: ago(15)}, false},
		{"not shipped yet", Order{Status: StatusPaid}, false},
		{"already requested", Order{Status: StatusDelivered, DeliveredAt: ago(1), Return: Return{Status: ReturnRequested}}, false},
		{"already refunded", Order{Status: StatusRefunded, DeliveredAt: ago(1)}, false},
	}
	for _, c := range cases {
		if got := c.o.CanRequestReturn(now, 14); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTrackingValidate(t *testing.T) {
	good := []Tracking{{}, {Carrier: "DHL", Number: "123"}, {URL: "https://track.example/1"}}
	for _, g := range good {
		if err := g.Validate(); err != nil {
			t.Errorf("%+v rejected: %v", g, err)
		}
	}
	for _, b := range []Tracking{{URL: "javascript:alert(1)"}, {URL: "ftp://x/y"}, {URL: "//evil"}, {Carrier: string(make([]byte, 70))}} {
		if err := b.Validate(); err == nil {
			t.Errorf("%+v accepted", b)
		}
	}
}
