// Package payments handles payment-processing for the sample service.
//
// Charge is the single public entry point — the LSP subgraph's Tier-1
// resolution demos the cross-package caller relationship `auth.Login →
// payments.Charge(token)` on this single line.
package payments

import "errors"

// ErrDeclined is returned when the payment processor rejects the charge.
var ErrDeclined = errors.New("payment declined")

// Charge processes a payment for the given token.
//
// MVP implementation: returns nil on success, ErrDeclined on empty tokens.
// Real-world implementations would call a payment gateway here.
func Charge(token string) error {
	if token == "" {
		return ErrDeclined
	}
	return nil
}
