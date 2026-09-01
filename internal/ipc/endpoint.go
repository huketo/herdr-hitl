package ipc

import "errors"

// ErrAlreadyRunning means another daemon already owns the endpoint. The
// caller should exit quietly rather than fight over the messenger connection:
// Telegram allows exactly one getUpdates poller per bot token.
var ErrAlreadyRunning = errors.New("ipc: daemon already running")
