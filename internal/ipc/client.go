package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// dialTimeout bounds a single connection attempt to the daemon.
const dialTimeout = 5 * time.Second

// ErrDaemonUnavailable means nothing is listening on the endpoint.
var ErrDaemonUnavailable = errors.New("ipc: daemon is not running")

// Client is a one-shot connection to the daemon. Ask blocks for as long as the
// human takes, so the connection is not pooled or reused.
type Client struct {
	conn net.Conn
}

// Dial connects to the daemon endpoint.
func Dial(ctx context.Context, endpoint string) (*Client, error) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	conn, err := dial(dialCtx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrDaemonUnavailable, endpoint, err)
	}
	return &Client{conn: conn}, nil
}

// Close releases the connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Do writes one request and reads one response. The call blocks until the
// daemon answers or ctx is done; cancelling ctx closes the connection, which
// is exactly the signal the daemon uses to withdraw a pending question.
func (c *Client) Do(ctx context.Context, req *Request) (*Response, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	payload = append(payload, '\n')

	stop := context.AfterFunc(ctx, func() { _ = c.conn.Close() })
	defer stop()

	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return nil, fmt.Errorf("set write deadline: %w", err)
	}
	if _, err := c.conn.Write(payload); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}
	if err := c.conn.SetWriteDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("clear write deadline: %w", err)
	}

	line, err := readFrame(bufio.NewReaderSize(c.conn, 64<<10))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("read response: %w", err)
	}

	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if !resp.OK {
		if resp.Error == nil {
			return nil, errors.New("daemon reported failure without a reason")
		}
		return &resp, resp.Error
	}
	return &resp, nil
}

// Call opens a connection, runs one request, and closes it.
func Call(ctx context.Context, endpoint string, req *Request) (*Response, error) {
	c, err := Dial(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return c.Do(ctx, req)
}

// Probe reports whether a daemon is accepting connections on endpoint.
func Probe(ctx context.Context, endpoint string) bool {
	c, err := Dial(ctx, endpoint)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
