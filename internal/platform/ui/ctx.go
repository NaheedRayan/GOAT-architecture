// Package ui holds the shared page shell and small view helpers.
package ui

import "context"

type cartCountKey struct{}

// WithCartCount lets the cart module expose the item count to the page header
// without the header importing the cart module.
func WithCartCount(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, cartCountKey{}, n)
}

func CartCount(ctx context.Context) int {
	n, _ := ctx.Value(cartCountKey{}).(int)
	return n
}
