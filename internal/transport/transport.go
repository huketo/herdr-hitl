// Package transport defines what a messenger backend must provide. The
// concrete backends live in the telegram and discord subpackages.
package transport

import (
	"context"

	"github.com/huketo/herdr-hitl/internal/hitl"
)

// Transport is a messenger backend with a lifecycle.
//
// A Transport owns exactly one connection to its messenger for the whole
// daemon process. That is not an optimisation: Telegram deletes updates from a
// single per-token queue, so two pollers steal each other's answers, and
// Discord rate-limits IDENTIFY to 1000 per 24 hours with a token reset as the
// penalty for exceeding it.
type Transport interface {
	hitl.Poster

	// Start opens the connection and begins receiving answers. It returns
	// once the transport is live, or an error if it cannot connect. The
	// receive loop runs until ctx is done.
	Start(ctx context.Context) error

	// Close tears the connection down. It is safe to call without Start.
	Close() error

	// Describe returns a one-line summary for `herdr-hitl doctor`, e.g.
	// "telegram: @herdr_hitl_bot -> chat 111222333". It must never include
	// the bot token.
	Describe() string
}
