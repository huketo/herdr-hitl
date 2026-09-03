package channel

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParsePolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    Policy
		wantErr bool
	}{
		{in: "messenger", want: PolicyMessenger},
		{in: "terminal", want: PolicyTerminal},
		{in: "auto", want: PolicyAuto},
		{in: "  AUTO ", want: PolicyAuto},
		{in: "", wantErr: true},
		{in: "phone", wantErr: true},
		{in: "telegram", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			got, err := ParsePolicy(tt.in)
			if tt.wantErr {
				if !errors.Is(err, ErrUnknown) {
					t.Fatalf("ParsePolicy(%q) error = %v, want ErrUnknown", tt.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePolicy(%q) = %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParsePolicy(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)

	tests := []struct {
		name       string
		explicit   string
		configured string
		marker     Marker
		want       Channel
		wantReason Reason
	}{
		{
			name:       "nothing configured keeps delivering",
			want:       Messenger,
			wantReason: ReasonDefault,
		},
		{
			name:       "an away marker cannot override the default policy",
			marker:     Marker{Set: true},
			want:       Messenger,
			wantReason: ReasonDefault,
		},
		{
			name:       "auto with no marker keeps the question in the terminal",
			configured: "auto",
			want:       Terminal,
			wantReason: ReasonPresent,
		},
		{
			name:       "auto with an open-ended marker delivers",
			configured: "auto",
			marker:     Marker{Set: true},
			want:       Messenger,
			wantReason: ReasonAway,
		},
		{
			name:       "auto with a live marker delivers",
			configured: "auto",
			marker:     Marker{Set: true, Until: future},
			want:       Messenger,
			wantReason: ReasonAway,
		},
		{
			name:       "auto with a lapsed marker does not deliver",
			configured: "auto",
			marker:     Marker{Set: true, Until: past},
			want:       Terminal,
			wantReason: ReasonStaleAway,
		},
		{
			name:       "a malformed marker is treated as away",
			configured: "auto",
			marker:     Marker{Set: true, Malformed: true},
			want:       Messenger,
			wantReason: ReasonAway,
		},
		{
			name:       "terminal policy refuses even while away",
			configured: "terminal",
			marker:     Marker{Set: true},
			want:       Terminal,
			wantReason: ReasonConfig,
		},
		{
			name:       "the flag beats an auto policy with no marker",
			explicit:   "messenger",
			configured: "auto",
			want:       Messenger,
			wantReason: ReasonFlag,
		},
		{
			name:       "the flag beats a messenger policy",
			explicit:   "terminal",
			configured: "messenger",
			want:       Terminal,
			wantReason: ReasonFlag,
		},
		{
			name:       "the flag may ask for auto itself",
			explicit:   "auto",
			configured: "messenger",
			marker:     Marker{Set: true},
			want:       Messenger,
			wantReason: ReasonAway,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Resolve(tt.explicit, tt.configured, tt.marker, now)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Channel != tt.want {
				t.Fatalf("channel = %q, want %q (%+v)", got.Channel, tt.want, got)
			}
			if got.Reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.wantReason)
			}
			if got.Delivers() != (tt.want == Messenger) {
				t.Fatalf("Delivers() = %t for channel %q", got.Delivers(), got.Channel)
			}
		})
	}
}

func TestResolveRejectsUnknownValues(t *testing.T) {
	t.Parallel()

	if _, err := Resolve("phone", "", Marker{}, time.Now()); !errors.Is(err, ErrUnknown) {
		t.Fatalf("flag error = %v, want ErrUnknown", err)
	}
	if _, err := Resolve("", "phone", Marker{}, time.Now()); !errors.Is(err, ErrUnknown) {
		t.Fatalf("config error = %v, want ErrUnknown", err)
	}
}

// TestResolveReportsTheExpiry keeps the expiry available to the CLI: a human
// who is told "away marker" without a deadline cannot tell a marker they set
// this morning from one they set last week.
func TestResolveReportsTheExpiry(t *testing.T) {
	t.Parallel()

	until := time.Date(2026, 9, 3, 18, 0, 0, 0, time.UTC)
	got, err := Resolve("", "auto", Marker{Set: true, Until: until}, until.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !got.AwayUntil.Equal(until) {
		t.Fatalf("away_until = %s, want %s", got.AwayUntil, until)
	}
	if got.Policy != PolicyAuto {
		t.Fatalf("policy = %q, want %q", got.Policy, PolicyAuto)
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state", "away")

	m, err := ReadMarker(path)
	if err != nil {
		t.Fatalf("ReadMarker on an absent file: %v", err)
	}
	if m.Set || m.Away(time.Now()) {
		t.Fatalf("absent marker = %+v, want the zero marker", m)
	}

	if err := WriteMarker(path, time.Time{}); err != nil {
		t.Fatalf("WriteMarker: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := string(body); got != "forever\n" {
		t.Fatalf("file = %q, want a word a human can read", got)
	}
	if m, err = ReadMarker(path); err != nil || !m.Away(time.Now()) {
		t.Fatalf("marker = %+v, err = %v, want away", m, err)
	}

	until := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := WriteMarker(path, until); err != nil {
		t.Fatalf("WriteMarker with an expiry: %v", err)
	}
	m, err = ReadMarker(path)
	if err != nil {
		t.Fatalf("ReadMarker: %v", err)
	}
	if !m.Until.Equal(until) {
		t.Fatalf("until = %s, want %s", m.Until, until)
	}
	if !m.Away(until.Add(-time.Minute)) || m.Away(until.Add(time.Minute)) {
		t.Fatalf("marker %+v does not lapse at %s", m, until)
	}

	existed, err := ClearMarker(path)
	if err != nil || !existed {
		t.Fatalf("ClearMarker = %t, %v, want true, nil", existed, err)
	}
	if existed, err = ClearMarker(path); err != nil || existed {
		t.Fatalf("second ClearMarker = %t, %v, want false, nil", existed, err)
	}
}

func TestMalformedMarkerIsAway(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "away")
	if err := os.WriteFile(path, []byte("tomorrow morning\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	m, err := ReadMarker(path)
	if err != nil {
		t.Fatalf("ReadMarker: %v", err)
	}
	if !m.Malformed {
		t.Fatalf("marker = %+v, want Malformed", m)
	}
	if !m.Away(time.Now()) {
		t.Fatal("a marker nobody can parse must fail towards delivery, not silence")
	}
}
