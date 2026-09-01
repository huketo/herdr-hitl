package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/huketo/herdr-hitl/internal/hitl"
)

// readHeaderTimeout bounds how long a connection may stay silent before
// sending its request. It stops a stuck client from pinning a goroutine.
const readHeaderTimeout = 30 * time.Second

// writeTimeout bounds the response write.
const writeTimeout = 30 * time.Second

// Handler implements the daemon side of the protocol.
type Handler interface {
	Ask(ctx context.Context, p *AskParams) (*hitl.Answer, error)
	Notify(ctx context.Context, p *AskParams) error
	Pending(ctx context.Context) ([]*hitl.Request, error)
	AnswerRequest(ctx context.Context, p *AnswerParams) error
	CancelRequest(ctx context.Context, p *CancelParams) error
	Status(ctx context.Context) (*Status, error)
	Shutdown(ctx context.Context) error
}

// Server serves the daemon protocol over any net.Listener.
type Server struct {
	handler Handler
	log     *slog.Logger
}

// NewServer wires a handler.
func NewServer(h Handler, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{handler: h, log: log}
}

// Serve accepts connections until ctx is done or the listener fails. It waits
// for in-flight connections to finish before returning so a shutdown never
// strands a blocked `ask`.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()

	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handle(ctx, conn)
		}()
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	if err := conn.SetReadDeadline(time.Now().Add(readHeaderTimeout)); err != nil {
		s.log.Debug("set read deadline", "error", err)
	}
	reader := bufio.NewReaderSize(conn, 64<<10)
	line, err := readFrame(reader)
	if err != nil {
		s.log.Debug("read request", "error", err)
		return
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		s.log.Debug("clear read deadline", "error", err)
	}

	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.reply(conn, &Response{OK: false, Error: &Error{
			Code: CodeInvalid, Message: "malformed request: " + err.Error(),
		}})
		return
	}

	// The client holding the connection open is the liveness signal. Once it
	// hangs up, a blocking ask must unwind and clear the chat message.
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		cancel()
	}()

	s.reply(conn, s.dispatch(callCtx, &req))
}

func (s *Server) dispatch(ctx context.Context, req *Request) *Response {
	switch req.Op {
	case OpAsk:
		if req.Ask == nil {
			return errResponse(&Error{Code: CodeInvalid, Message: "ask: missing parameters"})
		}
		ans, err := s.handler.Ask(ctx, req.Ask)
		if err != nil {
			return errResponse(NewError(err))
		}
		// A timeout or cancellation is a successful protocol exchange that
		// carries a non-answered outcome. The CLI maps it to an exit code.
		return &Response{OK: true, Answer: ans}

	case OpNotify:
		if req.Notify == nil {
			return errResponse(&Error{Code: CodeInvalid, Message: "notify: missing parameters"})
		}
		if err := s.handler.Notify(ctx, req.Notify); err != nil {
			return errResponse(NewError(err))
		}
		return &Response{OK: true}

	case OpPending:
		pending, err := s.handler.Pending(ctx)
		if err != nil {
			return errResponse(NewError(err))
		}
		return &Response{OK: true, Pending: pending}

	case OpAnswer:
		if req.Answer == nil {
			return errResponse(&Error{Code: CodeInvalid, Message: "answer: missing parameters"})
		}
		if err := s.handler.AnswerRequest(ctx, req.Answer); err != nil {
			return errResponse(NewError(err))
		}
		return &Response{OK: true}

	case OpCancel:
		if req.Cancel == nil {
			return errResponse(&Error{Code: CodeInvalid, Message: "cancel: missing parameters"})
		}
		if err := s.handler.CancelRequest(ctx, req.Cancel); err != nil {
			return errResponse(NewError(err))
		}
		return &Response{OK: true}

	case OpStatus:
		st, err := s.handler.Status(ctx)
		if err != nil {
			return errResponse(NewError(err))
		}
		return &Response{OK: true, Status: st}

	case OpShutdown:
		if err := s.handler.Shutdown(ctx); err != nil {
			return errResponse(NewError(err))
		}
		return &Response{OK: true}

	default:
		return errResponse(&Error{Code: CodeInvalid, Message: "unknown op " + string(req.Op)})
	}
}

func (s *Server) reply(conn net.Conn, resp *Response) {
	if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		s.log.Debug("set write deadline", "error", err)
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		s.log.Error("marshal response", "error", err)
		return
	}
	payload = append(payload, '\n')
	if _, err := conn.Write(payload); err != nil {
		s.log.Debug("write response", "error", err)
	}
}

func errResponse(e *Error) *Response { return &Response{OK: false, Error: e} }

// readFrame reads one newline-delimited JSON frame, rejecting oversized input
// instead of buffering it.
func readFrame(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, more, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk...)
		if len(buf) > MaxFrameBytes {
			return nil, fmt.Errorf("frame exceeds %d bytes", MaxFrameBytes)
		}
		if !more {
			return buf, nil
		}
	}
}
