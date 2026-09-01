package hitl

// Resolver is the slice of the broker that transports depend on. Taking the
// interface rather than *Broker keeps transports testable without a broker and
// makes the dependency direction obvious: transports push answers in, they
// never drive the broker's lifecycle.
type Resolver interface {
	// Resolve records an answer. It returns ErrUnknownRequest when the id is
	// not pending and ErrAlreadyAnswered when another human got there first.
	Resolve(ans *Answer) error
	// Lookup returns a pending request.
	Lookup(id string) (*Request, bool)
}

// Compile-time proof that the broker satisfies the interface transports use.
var _ Resolver = (*Broker)(nil)
