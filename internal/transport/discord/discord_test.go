package discord

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/hitl"
)

// fakeResolver stands in for the broker. It never talks to a network and
// records what a transport pushed at it.
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
	req, ok := f.requests[id]
	return req, ok
}

const testToken = "MTIzNDU2Nzg5.SECRET-TOKEN-VALUE"

func TestNewValidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      config.Discord
		resolver hitl.Resolver
		wantErr  bool
	}{
		{name: "channel", cfg: config.Discord{BotToken: testToken, ChannelID: "123"}, resolver: newFakeResolver()},
		{name: "dm", cfg: config.Discord{BotToken: testToken, UserID: "456"}, resolver: newFakeResolver()},
		{name: "no token", cfg: config.Discord{ChannelID: "123"}, resolver: newFakeResolver(), wantErr: true},
		{name: "no destination", cfg: config.Discord{BotToken: testToken}, resolver: newFakeResolver(), wantErr: true},
		{name: "no resolver", cfg: config.Discord{BotToken: testToken, ChannelID: "123"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := New(tt.cfg, tt.resolver, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("New = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Name() != config.TransportDiscord {
				t.Errorf("Name = %q, want %q", got.Name(), config.TransportDiscord)
			}
		})
	}
}

func TestNewSetsExplicitIntents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		messageContent bool
		want           discordgo.Intent
	}{
		{
			name: "default",
			want: discordgo.IntentDirectMessages | discordgo.IntentGuildMessages,
		},
		{
			name:           "privileged message content",
			messageContent: true,
			want: discordgo.IntentDirectMessages | discordgo.IntentGuildMessages |
				discordgo.IntentMessageContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tr, err := New(config.Discord{
				BotToken:             testToken,
				ChannelID:            "123",
				MessageContentIntent: tt.messageContent,
			}, newFakeResolver(), nil)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			// discordgo's constructor defaults to IntentsAllWithoutPrivileged,
			// which asks for intents this bot was never granted.
			if got := tr.session.Identify.Intents; got != tt.want {
				t.Fatalf("intents = %d, want %d", got, tt.want)
			}
			if tr.session.ShouldReconnectOnError {
				t.Errorf("reconnect is enabled before READY; a rejected identify would loop")
			}
		})
	}
}

func TestDescribeHidesTheToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  config.Discord
		want string
	}{
		{
			name: "channel",
			cfg:  config.Discord{BotToken: testToken, ChannelID: "123456789012345678"},
			want: "discord: (not connected) -> channel 123456789012345678",
		},
		{
			name: "dm",
			cfg:  config.Discord{BotToken: testToken, UserID: "987654321098765432"},
			want: "discord: (not connected) -> dm user 987654321098765432",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tr, err := New(tt.cfg, newFakeResolver(), nil)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := tr.Describe(); got != tt.want {
				t.Fatalf("Describe = %q, want %q", got, tt.want)
			}
			if strings.Contains(tr.Describe(), testToken) {
				t.Fatal("Describe leaked the bot token")
			}

			tr.mu.Lock()
			tr.self = &discordgo.User{Username: "herdr-hitl", Discriminator: "4821", GlobalName: "herdr-hitl"}
			tr.mu.Unlock()
			if got := tr.Describe(); !strings.Contains(got, "herdr-hitl#4821") {
				t.Fatalf("Describe = %q, want the bot tag herdr-hitl#4821", got)
			}
		})
	}
}

func TestCloseBeforeStartIsIdempotent(t *testing.T) {
	t.Parallel()

	tr, err := New(config.Discord{BotToken: testToken, ChannelID: "123"}, newFakeResolver(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := range 3 {
		if err := tr.Close(); err != nil {
			t.Fatalf("Close call %d: %v", i+1, err)
		}
	}
}

func TestSettleWithoutAPostIsANoOp(t *testing.T) {
	t.Parallel()

	// Nothing was posted, so there is no message to edit and no session call
	// to make: Settle must not reach for the network.
	tr, err := New(config.Discord{BotToken: testToken, ChannelID: "123"}, newFakeResolver(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := &hitl.Request{ID: "abcdef012345", Title: "gone"}
	if err := tr.Settle(t.Context(), req, &hitl.Answer{RequestID: req.ID, Status: hitl.StatusTimeout}); err != nil {
		t.Fatalf("Settle: %v", err)
	}
}

func TestPermitted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		allowed []string
		user    *discordgo.User
		want    bool
	}{
		{name: "open channel", user: &discordgo.User{ID: "1"}, want: true},
		{name: "allowed", allowed: []string{"1", "2"}, user: &discordgo.User{ID: "2"}, want: true},
		{name: "rejected", allowed: []string{"1"}, user: &discordgo.User{ID: "3"}},
		{name: "unknown user with a list", allowed: []string{"1"}},
		{name: "unknown user without a list", want: true},
		{name: "blank entries ignored", allowed: []string{" ", ""}, user: &discordgo.User{ID: "9"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tr, err := New(config.Discord{
				BotToken:       testToken,
				ChannelID:      "123",
				AllowedUserIDs: tt.allowed,
			}, newFakeResolver(), nil)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := tr.permitted(tt.user); got != tt.want {
				t.Fatalf("permitted = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResponder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		user *discordgo.User
		want string
	}{
		{name: "global name wins", user: &discordgo.User{ID: "1", Username: "huke", GlobalName: "Huke"}, want: "Huke"},
		{name: "legacy discriminator", user: &discordgo.User{ID: "1", Username: "huke", Discriminator: "4821"}, want: "huke#4821"},
		{name: "migrated discriminator", user: &discordgo.User{ID: "1", Username: "huke", Discriminator: "0"}, want: "huke"},
		{name: "nil user", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := responder(tt.user)
			if got.Transport != config.TransportDiscord {
				t.Errorf("transport = %q, want %q", got.Transport, config.TransportDiscord)
			}
			if got.Username != tt.want {
				t.Fatalf("username = %q, want %q", got.Username, tt.want)
			}
		})
	}
}

func TestInteractionUser(t *testing.T) {
	t.Parallel()

	user := &discordgo.User{ID: "dm"}
	member := &discordgo.User{ID: "guild"}

	tests := []struct {
		name string
		in   *discordgo.Interaction
		want string
	}{
		{name: "nil"},
		{name: "dm", in: &discordgo.Interaction{User: user}, want: "dm"},
		{name: "guild", in: &discordgo.Interaction{Member: &discordgo.Member{User: member}}, want: "guild"},
		{name: "neither", in: &discordgo.Interaction{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := interactionUser(tt.in)
			switch {
			case tt.want == "":
				if got != nil {
					t.Fatalf("interactionUser = %+v, want nil", got)
				}
			case got == nil || got.ID != tt.want:
				t.Fatalf("interactionUser = %+v, want id %q", got, tt.want)
			}
		})
	}
}

func TestExplainSendErrorNamesTheFix(t *testing.T) {
	t.Parallel()

	// A bot may only DM a user it shares a guild with, and opening the DM
	// channel succeeds regardless — the failure only shows up on the first
	// question, as a bare 403. The error carries the invite URL because the
	// fix is always the same and the bot knows its own application id.
	tr, err := New(config.Discord{BotToken: testToken, UserID: "248009230597095424"}, newFakeResolver(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tr.self = &discordgo.User{ID: "1544189650929713283", Username: "Herdr"}

	// RESTError.Error dereferences Response, so a usable fixture needs one.
	restErr := &discordgo.RESTError{
		Response:     &http.Response{Status: "403 Forbidden", StatusCode: http.StatusForbidden},
		ResponseBody: []byte(`{"message":"Cannot send messages to this user due to having no mutual guilds","code":50278}`),
		Message: &discordgo.APIErrorMessage{
			Code:    50278,
			Message: "Cannot send messages to this user due to having no mutual guilds",
		},
	}

	got := tr.explainSendError(restErr).Error()
	for _, want := range []string{
		"no mutual guilds",
		"shares a server with",
		"https://discord.com/oauth2/authorize?client_id=1544189650929713283",
		"permissions=125952",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("error %q does not mention %q", got, want)
		}
	}
}

func TestExplainSendErrorLeavesOtherFailuresAlone(t *testing.T) {
	t.Parallel()

	tr, err := New(config.Discord{BotToken: testToken, ChannelID: "1"}, newFakeResolver(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tr.self = &discordgo.User{ID: "42"}

	for _, err := range []error{
		errors.New("connection refused"),
		&discordgo.RESTError{
			Response:     &http.Response{Status: "403 Forbidden", StatusCode: http.StatusForbidden},
			ResponseBody: []byte(`{"message":"Missing Permissions","code":50013}`),
			Message:      &discordgo.APIErrorMessage{Code: 50013, Message: "Missing Permissions"},
		},
	} {
		if got := tr.explainSendError(err); got != err { //nolint:errorlint // identity is the point
			t.Errorf("explainSendError(%v) rewrote an unrelated failure: %v", err, got)
		}
	}
}
