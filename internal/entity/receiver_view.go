package entity

import "github.com/kellenff/yactt/internal/domain"

// ReceiverView is the bounded on-wire form of a method's receiver
// entity. Four fields — no layers, no source, no recursion into the
// receiver's receiver — so inline-emitted receiver info stays cheap
// even for methods whose receivers are in other files.
//
// Consumers that need the full receiver (layers, source, body) can
// dereference via node_get on ReceiverView.ID; this view is a
// discovery aid, not a substitute.
//
// Tool-layer response structs embed this with `*entity.ReceiverView
// `json:"receiver,omitempty"`` so every method-emitting tool gets the
// field for free and nil receivers stay out of the response.
type ReceiverView struct {
	ID      string          `json:"id"`
	Kind    domain.NodeKind `json:"kind"`
	Name    string          `json:"name"`
	Summary string          `json:"summary,omitempty"`
}
