package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huketo/herdr-hitl/internal/hitl"
)

func TestParseChoices(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		specs   []string
		primary []string
		danger  []string
		want    []hitl.Choice
		errHas  string
	}{
		{
			name:  "explicit id and label",
			specs: []string{"yes=Ship it", "no=Hold"},
			want: []hitl.Choice{
				{ID: "yes", Label: "Ship it"},
				{ID: "no", Label: "Hold"},
			},
		},
		{
			name:  "label splits at the first equals only",
			specs: []string{"eq=a = b"},
			want:  []hitl.Choice{{ID: "eq", Label: "a = b"}},
		},
		{
			name:  "bare label is slugified",
			specs: []string{"Ship it!", "Roll  back / revert"},
			want: []hitl.Choice{
				{ID: "ship-it", Label: "Ship it!"},
				{ID: "roll-back-revert", Label: "Roll  back / revert"},
			},
		},
		{
			name:  "slug is capped at the id limit",
			specs: []string{strings.Repeat("a", hitl.MaxChoiceIDLen+10)},
			want: []hitl.Choice{{
				ID:    strings.Repeat("a", hitl.MaxChoiceIDLen),
				Label: strings.Repeat("a", hitl.MaxChoiceIDLen+10),
			}},
		},
		{
			name:    "styles are applied by id",
			specs:   []string{"yes=Ship it", "no=Hold", "drop=Drop the table"},
			primary: []string{"yes"},
			danger:  []string{"drop"},
			want: []hitl.Choice{
				{ID: "yes", Label: "Ship it", Style: hitl.StylePrimary},
				{ID: "no", Label: "Hold"},
				{ID: "drop", Label: "Drop the table", Style: hitl.StyleDanger},
			},
		},
		{
			name:   "duplicate ids name both values",
			specs:  []string{"Ship it", "ship-it=Ship it again"},
			errHas: `--choice "Ship it" and --choice "ship-it=Ship it again" both use the id "ship-it"`,
		},
		{
			name:   "duplicate explicit ids",
			specs:  []string{"yes=Ship", "yes=Hold"},
			errHas: `both use the id "yes"`,
		},
		{
			name:    "unknown primary id",
			specs:   []string{"yes=Ship it"},
			primary: []string{"nope"},
			errHas:  `--primary "nope": no such choice id (have "yes")`,
		},
		{
			name:   "unknown danger id",
			specs:  []string{"yes=Ship it"},
			danger: []string{"drop"},
			errHas: `--danger "drop": no such choice id`,
		},
		{
			name:    "a choice cannot be both styles",
			specs:   []string{"yes=Ship it"},
			primary: []string{"yes"},
			danger:  []string{"yes"},
			errHas:  `choice "yes" is both --primary and --danger`,
		},
		{
			name:   "empty value",
			specs:  []string{"  "},
			errHas: "--choice: empty value",
		},
		{
			name:   "empty label",
			specs:  []string{"yes="},
			errHas: "empty label",
		},
		{
			name:   "empty id",
			specs:  []string{"=Ship it"},
			errHas: "empty id",
		},
		{
			name:   "unslugifiable label",
			specs:  []string{"!!!"},
			errHas: "cannot derive an id",
		},
		{
			name:   "explicit id rejects exotic characters",
			specs:  []string{"a b=Label"},
			errHas: "id may only contain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseChoices(tt.specs, tt.primary, tt.danger)
			if tt.errHas != "" {
				if err == nil {
					t.Fatalf("parseChoices() = %+v, want error containing %q", got, tt.errHas)
				}
				if !strings.Contains(err.Error(), tt.errHas) {
					t.Fatalf("error = %q, want it to contain %q", err, tt.errHas)
				}
				if code := exitCode(err); code != ExitUsage {
					t.Fatalf("exit code = %d, want %d", code, ExitUsage)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseChoices() error = %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d choices, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("choice %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParsedChoicesSurviveDomainValidation(t *testing.T) {
	t.Parallel()

	choices, err := parseChoices([]string{"Ship it", "yes=Hold on"}, []string{"ship-it"}, nil)
	if err != nil {
		t.Fatalf("parseChoices() error = %v", err)
	}
	req := &hitl.Request{Title: "t", Choices: choices}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestResolveBody(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "body.md")
	if err := os.WriteFile(file, []byte("from file"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		message     string
		messageFile string
		title       string
		stdin       string
		want        string
		errHas      string
	}{
		{
			name:        "message-file wins over message and stdin",
			message:     "from flag",
			messageFile: file,
			stdin:       "from stdin",
			want:        "from file",
		},
		{
			name:    "dash reads stdin",
			message: "-",
			stdin:   "piped body\n",
			want:    "piped body\n",
		},
		{
			name:    "message wins over stdin",
			message: "from flag",
			stdin:   "from stdin",
			want:    "from flag",
		},
		{
			name:  "redirected stdin is the fallback",
			stdin: "from stdin",
			want:  "from stdin",
		},
		{
			name:   "missing file is a usage error",
			want:   "",
			errHas: "--message-file",
			//nolint:gocritic // the path is deliberately absent
			messageFile: filepath.Join(dir, "absent.md"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveBody(tt.message, tt.messageFile, tt.title, strings.NewReader(tt.stdin), false)
			if tt.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errHas) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.errHas)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveBody() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("resolveBody() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestResolveBodyWithoutInput covers the interactive case: stdin is a
// terminal, so it must not be read at all.
func TestResolveBodyWithoutInput(t *testing.T) {
	t.Parallel()

	body, err := resolveBody("", "", "Deploy?", terminalReader{}, true)
	if err != nil {
		t.Fatalf("title-only question should be allowed, got %v", err)
	}
	if body != "" {
		t.Fatalf("body = %q, want empty", body)
	}

	_, err = resolveBody("", "", "", terminalReader{}, true)
	if err == nil {
		t.Fatal("expected a usage error when there is nothing to ask")
	}
	if code := exitCode(err); code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
}

// terminalReader stands in for an interactive stdin: the resolver must never
// consume it, so every read is a test failure.
type terminalReader struct{}

func (terminalReader) Read([]byte) (int, error) {
	return 0, errors.New("terminal reader must not be read")
}

func TestSlug(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"Ship it":              "ship-it",
		"  spaced  out  ":      "spaced-out",
		"MiXeD/Case_Thing":     "mixed-case-thing",
		"---":                  "",
		"already-slugged":      "already-slugged",
		"trailing punctuation": "trailing-punctuation",
	}
	for in, want := range tests {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveAttachments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(file, []byte("# notes"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := resolveAttachments([]string{file})
	if err != nil {
		t.Fatalf("resolveAttachments() error = %v", err)
	}
	if len(got) != 1 || !filepath.IsAbs(got[0]) {
		t.Fatalf("resolveAttachments() = %v, want one absolute path", got)
	}

	_, err = resolveAttachments([]string{filepath.Join(dir, "absent.png")})
	if err == nil {
		t.Fatal("expected an error for a missing attachment")
	}
	if code := exitCode(err); code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
}
