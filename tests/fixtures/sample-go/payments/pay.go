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

// Wallet holds a customer's stored payment instrument. Charge method
// exists primarily to exercise entity-package receiver resolution in
// the fidelity suite — there should be at least one meth:-prefixed
// node in the indexed graph when this file is parsed.
type Wallet struct {
	Token string
}

// Charge processes a payment against the wallet's stored token.
//
// ponytail: this exists so the fidelity suite has a known method
// symbol with a reachable receiver to pin entity.Entity.Receiver()
// against. Don't delete it without adding a different one in the
// same package — TestFidelity_KindMapping_RoundTrips depends on it.
func (w *Wallet) Charge() error {
	if w.Token == "" {
		return ErrDeclined
	}
	return nil
}
