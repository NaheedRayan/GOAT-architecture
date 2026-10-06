package domain

import "testing"

func TestStateMachine(t *testing.T) {
	ok := [][2]string{
		{StatusAwaitingPayment, StatusPaid}, {StatusAwaitingPayment, StatusCancelled},
		{StatusPaid, StatusFulfilling}, {StatusFulfilling, StatusShipped},
	}
	for _, c := range ok {
		o := Order{Status: c[0]}
		if err := o.Transition(c[1]); err != nil || o.Status != c[1] {
			t.Errorf("%s → %s should be allowed: %v", c[0], c[1], err)
		}
	}
	bad := [][2]string{
		{StatusPaid, StatusCancelled}, {StatusShipped, StatusPaid}, {StatusCancelled, StatusPaid},
		{StatusAwaitingPayment, StatusShipped}, {StatusFulfilling, StatusPaid},
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
