package telegram

import (
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/hitl"
)

// fakeResolver stands in for the broker. It records what transports push in
// without needing a running daemon.
type fakeResolver struct {
	mu       sync.Mutex
	requests map[string]*hitl.Request
	answers  []*hitl.Answer
	err      error
}

func newFakeResolver(reqs ...*hitl.Request) *fakeResolver {
	f := &fakeResolver{requests: make(map[string]*hitl.Request, len(reqs))}
	for _, r := range reqs {
		f.requests[r.ID] = r
	}
	return f
}

func (f *fakeResolver) Resolve(ans *hitl.Answer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.answers = append(f.answers, ans)
	return nil
}

func (f *fakeResolver) Lookup(id string) (*hitl.Request, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.requests[id]
	return r, ok
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestTransport builds a transport with a syntactically valid but fake
// token. bot.New performs no network I/O once getMe is skipped.
func newTestTransport(t *testing.T, cfg config.Telegram, resolver hitl.Resolver) *Transport {
	t.Helper()
	if cfg.BotToken == "" {
		cfg.BotToken = "111222:AAFakeTokenForTests"
	}
	if cfg.ChatID == "" {
		cfg.ChatID = "-1001234567890"
	}
	tr, err := New(cfg, resolver, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

func TestNewValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      config.Telegram
		resolver hitl.Resolver
	}{
		{name: "no resolver", cfg: config.Telegram{BotToken: "t", ChatID: "1"}},
		{name: "no token", cfg: config.Telegram{ChatID: "1"}, resolver: newFakeResolver()},
		{name: "blank token", cfg: config.Telegram{BotToken: "  ", ChatID: "1"}, resolver: newFakeResolver()},
		{name: "no chat", cfg: config.Telegram{BotToken: "t"}, resolver: newFakeResolver()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(tc.cfg, tc.resolver, discardLogger()); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestParseChatID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want any
	}{
		{name: "supergroup", in: "-1001234567890", want: int64(-1001234567890)},
		{name: "user", in: "111222333", want: int64(111222333)},
		{name: "channel handle", in: "@herdr_hitl", want: "@herdr_hitl"},
		{name: "padded", in: " 42 ", want: int64(42)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseChatID(tc.in); got != tc.want {
				t.Fatalf("parseChatID(%q) = %v (%T), want %v (%T)", tc.in, got, got, tc.want, tc.want)
			}
		})
	}
}

func TestDescribeHidesToken(t *testing.T) {
	t.Parallel()

	const token = "111222:AASecretTokenValue"
	tr := newTestTransport(t, config.Telegram{BotToken: token, ChatID: "111222333"}, newFakeResolver())

	before := tr.Describe()
	if strings.Contains(before, token) || strings.Contains(before, "AASecret") {
		t.Fatalf("Describe leaked the token: %q", before)
	}
	if !strings.Contains(before, "111222333") {
		t.Fatalf("Describe should name the chat: %q", before)
	}

	tr.mu.Lock()
	tr.username = "herdr_hitl_bot"
	tr.mu.Unlock()

	if got, want := tr.Describe(), "telegram: @herdr_hitl_bot -> chat 111222333"; got != want {
		t.Fatalf("Describe = %q, want %q", got, want)
	}
}

func TestCloseIsIdempotentBeforeStart(t *testing.T) {
	t.Parallel()

	tr := newTestTransport(t, config.Telegram{}, newFakeResolver())
	for range 3 {
		if err := tr.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
}

func TestName(t *testing.T) {
	t.Parallel()

	tr := newTestTransport(t, config.Telegram{}, newFakeResolver())
	if got := tr.Name(); got != config.TransportTelegram {
		t.Fatalf("Name = %q, want %q", got, config.TransportTelegram)
	}
}

// track registers a posted question the way Post would.
func track(t *testing.T, tr *Transport, requestID string, chatID int64, messageID int, allowFree bool) {
	t.Helper()
	tr.remember(&posted{
		requestID: requestID,
		chatID:    chatID,
		messageID: messageID,
		allowFree: allowFree,
	})
}

func TestMatchFreeText(t *testing.T) {
	t.Parallel()

	const chat = int64(-100777)

	t.Run("reply binds to the question it answers", func(t *testing.T) {
		t.Parallel()
		tr := newTestTransport(t, config.Telegram{}, newFakeResolver())
		track(t, tr, "reqA", chat, 10, true)
		track(t, tr, "reqB", chat, 11, true)

		id, res := tr.matchFreeText(chat, 11)
		if res != matchOne || id != "reqB" {
			t.Fatalf("got (%q, %v), want (reqB, matchOne)", id, res)
		}
	})

	t.Run("single pending binds without a reply", func(t *testing.T) {
		t.Parallel()
		tr := newTestTransport(t, config.Telegram{}, newFakeResolver())
		track(t, tr, "reqA", chat, 10, true)

		id, res := tr.matchFreeText(chat, 0)
		if res != matchOne || id != "reqA" {
			t.Fatalf("got (%q, %v), want (reqA, matchOne)", id, res)
		}
	})

	t.Run("several pending refuse to guess", func(t *testing.T) {
		t.Parallel()
		tr := newTestTransport(t, config.Telegram{}, newFakeResolver())
		track(t, tr, "reqA", chat, 10, true)
		track(t, tr, "reqB", chat, 11, true)

		id, res := tr.matchFreeText(chat, 0)
		if res != matchAmbiguous || id != "" {
			t.Fatalf("got (%q, %v), want ('', matchAmbiguous)", id, res)
		}
	})

	t.Run("nothing pending matches nothing", func(t *testing.T) {
		t.Parallel()
		tr := newTestTransport(t, config.Telegram{}, newFakeResolver())

		if id, res := tr.matchFreeText(chat, 0); res != matchNone || id != "" {
			t.Fatalf("got (%q, %v), want ('', matchNone)", id, res)
		}
	})

	t.Run("another chat does not bind", func(t *testing.T) {
		t.Parallel()
		tr := newTestTransport(t, config.Telegram{}, newFakeResolver())
		track(t, tr, "reqA", chat, 10, true)

		if _, res := tr.matchFreeText(chat+1, 0); res != matchNone {
			t.Fatalf("got %v, want matchNone for a foreign chat", res)
		}
	})

	t.Run("buttons only question ignores a reply", func(t *testing.T) {
		t.Parallel()
		tr := newTestTransport(t, config.Telegram{}, newFakeResolver())
		track(t, tr, "reqA", chat, 10, false)

		if _, res := tr.matchFreeText(chat, 10); res != matchNone {
			t.Fatalf("got %v, want matchNone for a buttons-only question", res)
		}
	})

	t.Run("reply to an unrelated message falls back to the single pending", func(t *testing.T) {
		t.Parallel()
		tr := newTestTransport(t, config.Telegram{}, newFakeResolver())
		track(t, tr, "reqA", chat, 10, true)

		id, res := tr.matchFreeText(chat, 999)
		if res != matchOne || id != "reqA" {
			t.Fatalf("got (%q, %v), want (reqA, matchOne)", id, res)
		}
	})

	t.Run("settled question stops matching", func(t *testing.T) {
		t.Parallel()
		tr := newTestTransport(t, config.Telegram{}, newFakeResolver())
		track(t, tr, "reqA", chat, 10, true)
		if tr.forget("reqA") == nil {
			t.Fatal("forget should return the tracked question")
		}
		if tr.forget("reqA") != nil {
			t.Fatal("forget must be idempotent")
		}
		if _, res := tr.matchFreeText(chat, 10); res != matchNone {
			t.Fatalf("got %v, want matchNone after settling", res)
		}
	})
}

func TestPruneDropsExpiredQuestions(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tr := newTestTransport(t, config.Telegram{}, newFakeResolver())
	now := base
	tr.now = func() time.Time { return now }

	// A notice with a deadline: the broker never settles it, so only pruning
	// can reclaim it.
	tr.remember(&posted{requestID: "notice", chatID: 1, messageID: 5, allowFree: true, expiresAt: base.Add(time.Minute)})
	// An ask that waits forever must survive.
	tr.remember(&posted{requestID: "forever", chatID: 2, messageID: 6, allowFree: true})

	now = base.Add(2 * time.Minute)
	if _, res := tr.matchFreeText(1, 5); res != matchNone {
		t.Fatalf("expired notice still matched: %v", res)
	}
	if id, res := tr.matchFreeText(2, 6); res != matchOne || id != "forever" {
		t.Fatalf("deadline-free question was pruned: (%q, %v)", id, res)
	}
}

func TestPermitted(t *testing.T) {
	t.Parallel()

	user := &models.User{ID: 111222333, Username: "Huke"}
	other := &models.User{ID: 999, Username: "stranger"}

	tests := []struct {
		name    string
		allowed []string
		user    *models.User
		want    bool
	}{
		{name: "empty list allows anyone", user: user, want: true},
		{name: "numeric id matches", allowed: []string{"111222333"}, user: user, want: true},
		{name: "handle matches case insensitively", allowed: []string{"@huke"}, user: user, want: true},
		{name: "other user rejected", allowed: []string{"111222333"}, user: other, want: false},
		{name: "nil user rejected when restricted", allowed: []string{"1"}, user: nil, want: false},
		{name: "blank entries ignored", allowed: []string{"  ", "111222333"}, user: user, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := newTestTransport(t, config.Telegram{AllowedUserIDs: tc.allowed}, newFakeResolver())
			if got := tr.permitted(tc.user); got != tc.want {
				t.Fatalf("permitted = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResponderOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		user *models.User
		want hitl.Responder
	}{
		{
			name: "handle preferred",
			user: &models.User{ID: 7, Username: "huke", FirstName: "Huke"},
			want: hitl.Responder{Transport: config.TransportTelegram, UserID: "7", Username: "@huke"},
		},
		{
			name: "falls back to a display name",
			user: &models.User{ID: 8, FirstName: "Ada", LastName: "Lovelace"},
			want: hitl.Responder{Transport: config.TransportTelegram, UserID: "8", Username: "Ada Lovelace"},
		},
		{
			name: "nil user still names the transport",
			user: nil,
			want: hitl.Responder{Transport: config.TransportTelegram},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := responderOf(tc.user); got != tc.want {
				t.Fatalf("responderOf = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestExpiryOf(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	if got := expiryOf(&hitl.Request{CreatedAt: created}); !got.IsZero() {
		t.Fatalf("a request without a timeout must never expire, got %v", got)
	}
	got := expiryOf(&hitl.Request{CreatedAt: created, Timeout: 30 * time.Minute})
	if !got.After(created.Add(30 * time.Minute)) {
		t.Fatalf("expiry %v should sit past the deadline so Settle can still run", got)
	}
}

func TestIsNotModified(t *testing.T) {
	t.Parallel()

	if !isNotModified(errTelegram("bad request, Bad Request: message is not modified: ...")) {
		t.Fatal("expected the benign edit error to be recognised")
	}
	if isNotModified(errTelegram("bad request, Bad Request: chat not found")) {
		t.Fatal("unrelated errors must not be swallowed")
	}
	if isNotModified(nil) {
		t.Fatal("nil is not an error")
	}
}

type errTelegram string

func (e errTelegram) Error() string { return string(e) }

func TestBodyDocumentName(t *testing.T) {
	t.Parallel()

	if got, want := bodyDocumentName("abc123"), "question-abc123.md"; got != want {
		t.Fatalf("bodyDocumentName = %q, want %q", got, want)
	}
}
