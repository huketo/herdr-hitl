package daemon

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/hitl"
	"github.com/huketo/herdr-hitl/internal/transport"
	"github.com/huketo/herdr-hitl/internal/transport/discord"
	"github.com/huketo/herdr-hitl/internal/transport/telegram"
)

// DefaultTransports builds the real messenger transports named by cfg. It is
// the production TransportFactory; tests substitute an in-process fake.
//
// A backend whose construction fails is logged and skipped so one bad token
// does not silence the other messenger. Nothing configured at all, or nothing
// constructible, is an error: a daemon with no way to reach a human would
// accept asks it can never answer.
func DefaultTransports(cfg *config.Config, resolver hitl.Resolver, log *slog.Logger) ([]transport.Transport, error) {
	enabled := cfg.EnabledTransports()
	if len(enabled) == 0 {
		return nil, fmt.Errorf("%w: no messenger is configured; run `herdr-hitl config init` "+
			"and fill in the token (config lives in `herdr plugin config-dir huketo.hitl`)",
			hitl.ErrNoTransport)
	}

	built := make([]transport.Transport, 0, len(enabled))
	var failures []error
	for _, name := range enabled {
		var (
			t   transport.Transport
			err error
		)
		switch name {
		case config.TransportTelegram:
			t, err = telegram.New(cfg.Telegram, resolver, log)
		case config.TransportDiscord:
			t, err = discord.New(cfg.Discord, resolver, log)
		default:
			err = fmt.Errorf("unknown transport %q", name)
		}
		if err != nil {
			log.Error("transport unavailable", "transport", name, "error", err)
			failures = append(failures, fmt.Errorf("%s: %w", name, err))
			continue
		}
		built = append(built, t)
	}
	if len(built) == 0 {
		return nil, fmt.Errorf("%w: every configured messenger failed to initialise: %w",
			hitl.ErrNoTransport, errors.Join(failures...))
	}
	return built, nil
}
