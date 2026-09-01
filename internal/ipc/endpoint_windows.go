//go:build windows

package ipc

import (
	"context"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
)

// pipeSDDL grants access to the creating user and to SYSTEM only:
// D:(A;;GA;;;OW) owner, (A;;GA;;;SY) local system.
const pipeSDDL = "D:(A;;GA;;;OW)(A;;GA;;;SY)"

// Listen binds the daemon endpoint as a named pipe. Unlike a Unix socket a
// pipe has no on-disk remnant, so a crashed daemon leaves nothing to clean up;
// a second listener simply fails.
func Listen(endpoint string) (net.Listener, error) {
	if Probe(context.Background(), endpoint) {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, endpoint)
	}
	l, err := winio.ListenPipe(endpoint, &winio.PipeConfig{
		SecurityDescriptor: pipeSDDL,
		MessageMode:        false,
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", endpoint, err)
	}
	return l, nil
}

func dial(ctx context.Context, endpoint string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, endpoint)
}
