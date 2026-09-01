package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/huketo/herdr-hitl/internal/hitl"
)

// idExtraRunes are the punctuation characters allowed in an explicit choice id.
// Choice ids travel inside Telegram callback_data (64 bytes total), so the set
// stays narrow and ASCII.
const idExtraRunes = "-_.:"

// parseChoices turns the repeated --choice values into domain choices and
// applies the --primary/--danger styles.
//
// A value is either "id=Label" (split at the first "=") or a bare label whose
// id is slugified from the label.
func parseChoices(specs, primary, danger []string) ([]hitl.Choice, error) {
	choices := make([]hitl.Choice, 0, len(specs))
	origin := make(map[string]string, len(specs))

	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			return nil, usagef("--choice: empty value")
		}

		var id, label string
		if key, rest, ok := strings.Cut(spec, "="); ok {
			id, label = strings.TrimSpace(key), strings.TrimSpace(rest)
			if id == "" {
				return nil, usagef("--choice %q: empty id before %q", spec, "=")
			}
			if label == "" {
				return nil, usagef("--choice %q: empty label", spec)
			}
			if err := validateChoiceID(id, spec); err != nil {
				return nil, err
			}
		} else {
			label = spec
			id = slug(label)
			if id == "" {
				return nil, usagef("--choice %q: cannot derive an id from the label, use \"id=Label\"", spec)
			}
		}
		if prev, dup := origin[id]; dup {
			return nil, usagef("--choice %q and --choice %q both use the id %q", prev, spec, id)
		}
		origin[id] = spec
		choices = append(choices, hitl.Choice{ID: id, Label: label})
	}

	styled := make(map[string]string, len(primary)+len(danger))
	apply := func(ids []string, flag, style string) error {
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			idx := indexOfChoice(choices, id)
			if idx < 0 {
				return usagef("--%s %q: no such choice id (have %s)", flag, id, quoteIDs(choices))
			}
			if other, ok := styled[id]; ok && other != flag {
				return usagef("choice %q is both --%s and --%s", id, other, flag)
			}
			styled[id] = flag
			choices[idx].Style = style
		}
		return nil
	}
	if err := apply(primary, "primary", hitl.StylePrimary); err != nil {
		return nil, err
	}
	if err := apply(danger, "danger", hitl.StyleDanger); err != nil {
		return nil, err
	}
	return choices, nil
}

// validateChoiceID rejects ids that would not survive a callback payload.
func validateChoiceID(id, spec string) error {
	if len([]rune(id)) > hitl.MaxChoiceIDLen {
		return usagef("--choice %q: id exceeds %d characters", spec, hitl.MaxChoiceIDLen)
	}
	for _, r := range id {
		if r > unicode.MaxASCII || (!isAlnum(r) && !strings.ContainsRune(idExtraRunes, r)) {
			return usagef("--choice %q: id may only contain ASCII letters, digits, and %q", spec, idExtraRunes)
		}
	}
	return nil
}

func indexOfChoice(choices []hitl.Choice, id string) int {
	for i := range choices {
		if choices[i].ID == id {
			return i
		}
	}
	return -1
}

func quoteIDs(choices []hitl.Choice) string {
	ids := make([]string, 0, len(choices))
	for _, c := range choices {
		ids = append(ids, `"`+c.ID+`"`)
	}
	if len(ids) == 0 {
		return "no choices"
	}
	return strings.Join(ids, ", ")
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// slug derives a stable choice id from a label: lowercase, every run of
// non-alphanumerics collapsed to a single "-", trimmed, capped at
// hitl.MaxChoiceIDLen.
func slug(label string) string {
	var b strings.Builder
	b.Grow(len(label))
	dash := false
	for _, r := range strings.ToLower(label) {
		if isAlnum(r) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if r := []rune(out); len(r) > hitl.MaxChoiceIDLen {
		out = strings.Trim(string(r[:hitl.MaxChoiceIDLen]), "-")
	}
	return out
}

// resolveBody applies the documented precedence: --message-file, then
// --message ("-" reads stdin), then piped stdin. A title-only question is
// allowed; a question with nothing to say at all is a usage error.
//
// interactive says whether stdin is a terminal, in which case reading it
// would hang the agent waiting for a human who is looking at a chat app.
func resolveBody(message, messageFile, title string, stdin io.Reader, interactive bool) (string, error) {
	switch {
	case messageFile != "":
		data, err := os.ReadFile(messageFile) //nolint:gosec // path comes from the operator
		if err != nil {
			return "", usagef("--message-file: %w", err)
		}
		return string(data), nil

	case message == "-":
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", failf("read stdin: %w", err)
		}
		return string(data), nil

	case message != "":
		return message, nil

	case !interactive:
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", failf("read stdin: %w", err)
		}
		return string(data), nil

	case strings.TrimSpace(title) != "":
		return "", nil

	default:
		return "", usagef("no question body: pass --message, --message-file, or pipe it on stdin")
	}
}

// isTerminal reports whether r is an interactive terminal. Anything that is
// not an *os.File (a test buffer, a pipe wrapper) counts as redirected input.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// resolveAttachments makes every path absolute — the daemon reads them, and it
// may have a different working directory — and validates them here so a typo
// fails in the CLI instead of halfway through an upload.
func resolveAttachments(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.TrimSpace(arg) == "" {
			return nil, usagef("--attach: empty path")
		}
		abs, err := filepath.Abs(arg)
		if err != nil {
			return nil, usagef("--attach %q: %w", arg, err)
		}
		if _, err := hitl.NewAttachment(abs); err != nil {
			return nil, usagef("--attach %q: %w", arg, err)
		}
		out = append(out, abs)
	}
	return out, nil
}

// herdrContext is the subset of HERDR_PLUGIN_CONTEXT_JSON worth reading.
type herdrContext struct {
	WorkspaceLabel   string `json:"workspace_label"`
	FocusedPaneAgent string `json:"focused_pane_agent"`
}

// detectOrigin describes the caller so a human juggling several agents can
// tell two questions apart in one chat.
func detectOrigin(agent string) hitl.Origin {
	o := hitl.Origin{
		Cwd:         getwd(),
		User:        firstEnv("USER", "USERNAME", "LOGNAME"),
		PaneID:      os.Getenv("HERDR_PANE_ID"),
		TabID:       os.Getenv("HERDR_TAB_ID"),
		WorkspaceID: os.Getenv("HERDR_WORKSPACE_ID"),
	}
	if host, err := os.Hostname(); err == nil {
		o.Host = host
	}

	ctx := loadHerdrContext(os.Getenv("HERDR_PLUGIN_CONTEXT_JSON"))
	o.WorkspaceLabel = ctx.WorkspaceLabel

	switch {
	case strings.TrimSpace(agent) != "":
		o.Agent = strings.TrimSpace(agent)
	case os.Getenv("HITL_AGENT") != "":
		o.Agent = os.Getenv("HITL_AGENT")
	case ctx.FocusedPaneAgent != "":
		o.Agent = ctx.FocusedPaneAgent
	default:
		o.Agent = "agent"
	}
	return o
}

// loadHerdrContext accepts either the JSON document itself or a path to it;
// Herdr has shipped both shapes and neither is worth failing over.
func loadHerdrContext(v string) herdrContext {
	var ctx herdrContext
	v = strings.TrimSpace(v)
	if v == "" {
		return ctx
	}
	data := []byte(v)
	if !strings.HasPrefix(v, "{") {
		raw, err := os.ReadFile(v) //nolint:gosec // path is supplied by the host process
		if err != nil {
			return ctx
		}
		data = raw
	}
	_ = json.Unmarshal(data, &ctx)
	return ctx
}

func getwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
