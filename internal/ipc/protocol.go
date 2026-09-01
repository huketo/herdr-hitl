// Package ipc carries requests between the short-lived `herdr-hitl` CLI and
// the resident daemon that owns the messenger connections.
//
// The wire format is one JSON request line, then one JSON response line, then
// the connection closes. Keeping the connection open for the whole ask is
// deliberate: an EOF from the client is how the daemon learns the agent went
// away (Ctrl-C, killed pane) and withdraws the question from the chat.
package ipc

import (
	"errors"
	"time"

	"github.com/huketo/herdr-hitl/internal/hitl"
)

// MaxFrameBytes caps a single request line. Attachments travel as paths, not
// bytes, so a legitimate frame is small.
const MaxFrameBytes = 4 << 20

// Op names a daemon operation.
type Op string

// Supported operations.
const (
	// OpAsk posts a question and blocks until it resolves.
	OpAsk Op = "ask"
	// OpNotify posts a message that expects no answer.
	OpNotify Op = "notify"
	// OpPending lists outstanding questions.
	OpPending Op = "pending"
	// OpAnswer resolves a pending question from the terminal.
	OpAnswer Op = "answer"
	// OpCancel withdraws a pending question.
	OpCancel Op = "cancel"
	// OpStatus reports daemon health.
	OpStatus Op = "status"
	// OpShutdown stops the daemon.
	OpShutdown Op = "shutdown"
)

// Request is the single JSON object a client writes.
type Request struct {
	Op     Op            `json:"op"`
	Ask    *AskParams    `json:"ask,omitempty"`
	Notify *AskParams    `json:"notify,omitempty"`
	Answer *AnswerParams `json:"answer,omitempty"`
	Cancel *CancelParams `json:"cancel,omitempty"`
}

// AskParams describes a question. Attachments are absolute paths on the
// daemon's machine; the daemon reads and uploads them.
type AskParams struct {
	Title         string        `json:"title,omitempty"`
	Body          string        `json:"body"`
	Choices       []hitl.Choice `json:"choices,omitempty"`
	AllowFreeText bool          `json:"allow_free_text"`
	Attachments   []string      `json:"attachments,omitempty"`
	Timeout       Duration      `json:"timeout,omitempty"`
	Transports    []string      `json:"transports,omitempty"`
	Origin        hitl.Origin   `json:"origin,omitempty"`
}

// AnswerParams resolves a pending question locally.
type AnswerParams struct {
	RequestID string `json:"request_id"`
	ChoiceID  string `json:"choice_id,omitempty"`
	Text      string `json:"text,omitempty"`
	Responder string `json:"responder,omitempty"`
}

// CancelParams withdraws a pending question.
type CancelParams struct {
	RequestID string `json:"request_id"`
	Reason    string `json:"reason,omitempty"`
}

// Response is the single JSON object the daemon writes back.
type Response struct {
	OK      bool            `json:"ok"`
	Error   *Error          `json:"error,omitempty"`
	Answer  *hitl.Answer    `json:"answer,omitempty"`
	Pending []*hitl.Request `json:"pending,omitempty"`
	Status  *Status         `json:"status,omitempty"`
}

// Status reports what the daemon is doing.
type Status struct {
	PID        int      `json:"pid"`
	Version    string   `json:"version"`
	Socket     string   `json:"socket"`
	Transports []string `json:"transports"`
	Pending    int      `json:"pending"`
	StartedAt  string   `json:"started_at"`
	Uptime     string   `json:"uptime"`
}

// ErrorCode classifies a failure so the CLI can pick an exit status without
// string matching.
type ErrorCode string

// Error codes.
const (
	CodeInvalid         ErrorCode = "invalid"
	CodeTimeout         ErrorCode = "timeout"
	CodeCanceled        ErrorCode = "canceled"
	CodeUnknownRequest  ErrorCode = "unknown_request"
	CodeAlreadyAnswered ErrorCode = "already_answered"
	CodeNoTransport     ErrorCode = "no_transport"
	CodeInternal        ErrorCode = "internal"
)

// Error is a structured daemon failure.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return string(e.Code) + ": " + e.Message
}

// Unwrap maps daemon error codes back onto the domain sentinels so callers can
// use errors.Is across the process boundary.
func (e *Error) Unwrap() error {
	switch e.Code {
	case CodeTimeout:
		return hitl.ErrTimeout
	case CodeCanceled:
		return hitl.ErrCanceled
	case CodeUnknownRequest:
		return hitl.ErrUnknownRequest
	case CodeAlreadyAnswered:
		return hitl.ErrAlreadyAnswered
	case CodeNoTransport:
		return hitl.ErrNoTransport
	default:
		return nil
	}
}

// NewError classifies err for the wire.
func NewError(err error) *Error {
	if err == nil {
		return nil
	}
	code := CodeInternal
	switch {
	case errors.Is(err, hitl.ErrTimeout):
		code = CodeTimeout
	case errors.Is(err, hitl.ErrCanceled):
		code = CodeCanceled
	case errors.Is(err, hitl.ErrUnknownRequest):
		code = CodeUnknownRequest
	case errors.Is(err, hitl.ErrAlreadyAnswered):
		code = CodeAlreadyAnswered
	case errors.Is(err, hitl.ErrNoTransport):
		code = CodeNoTransport
	}
	return &Error{Code: code, Message: err.Error()}
}

// Duration is a time.Duration that marshals as a Go duration string ("30m"),
// so that config files, CLI flags, and the wire all speak one dialect.
type Duration time.Duration

// String renders the duration.
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON writes the duration as a quoted string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}

// UnmarshalJSON accepts both `"30m"` and a raw nanosecond count.
func (d *Duration) UnmarshalJSON(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	if b[0] == '"' {
		if len(b) < 2 {
			return errors.New("ipc: malformed duration")
		}
		parsed, err := time.ParseDuration(string(b[1 : len(b)-1]))
		if err != nil {
			return err
		}
		*d = Duration(parsed)
		return nil
	}
	var ns int64
	for _, c := range b {
		if c < '0' || c > '9' {
			return errors.New("ipc: malformed duration " + string(b))
		}
		ns = ns*10 + int64(c-'0')
	}
	*d = Duration(ns)
	return nil
}
