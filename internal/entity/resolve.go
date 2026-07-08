package entity

// ReceiverLookup is the resolver callback signature: given a receiver
// type name and the enclosing package, return the resolved Entity or
// (Entity{}, false) on miss. The lookup is responsible for the
// best-match policy — most callers pass a closure wrapping
// store.Repo.Lookup.
//
// ponytail: a callback keeps this package free of any store/domain
// coupling. The tool layer wires the closure; tests wire a stub.
type ReceiverLookup func(receiverName, pkg string) (Entity, bool)

// ResolveReceivers walks entities in place and, for every entity
// with a non-empty receiver name whose receiver is still nil and
// whose receiver name resolves via lookup, fills in the receiver
// pointer. Pure: no I/O, no side effects beyond mutating the input
// slice. Lookup is called at most once per entity; an unresolvable
// receiver stays nil and the caller can fall back to ReceiverName
// for display.
//
// ponytail: best-effort by design. If a real consumer starts hitting
// unresolved receivers in practice, narrow the lookup (e.g. consult
// the LSP type hierarchy) before changing this contract.
func ResolveReceivers(entities []Entity, lookup ReceiverLookup) {
	if lookup == nil {
		return
	}
	for i := range entities {
		e := &entities[i]
		if e.receiverName == "" || e.receiver != nil {
			continue
		}
		if r, ok := lookup(e.receiverName, e.pkg); ok {
			e.WithReceiver(&r)
		}
	}
}
