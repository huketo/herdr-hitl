// Package channel decides where a Question goes: out to a messenger, or
// nowhere at all because the human is sitting at the terminal the agent runs
// in.
//
// The decision is taken in the CLI rather than the daemon on purpose. Presence
// is a property of the process that asks — its environment, its launcher, its
// operator — and the daemon is shared by every asking process on the machine.
//
// Nothing here inspects the machine to guess whether a human is nearby. Focus,
// idle timers, and attached-client probes all answer a different question than
// "will this person see a terminal prompt", and they answer it wrongly at the
// moment it matters. Presence is declared instead: an unattended launcher
// declares itself with HITL_CHANNEL, and a human who walks away declares it
// with the Away marker.
package channel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Channel is a class of destination for a Question.
type Channel string

const (
	// Messenger delivers the Question through a Transport, as usual.
	Messenger Channel = "messenger"
	// Terminal means the human is at the agent's own interface, so the agent
	// must ask there and nothing is delivered to a messenger.
	Terminal Channel = "terminal"
	// Auto defers to the Away marker. It is an input value only: Resolve
	// never returns it.
	Auto Channel = "auto"
	// AFK means the human declared unavailable mode. Questions and
	// notifications are refused before reaching the daemon or network.
	AFK Channel = "afk"
)

// Reason names what settled the decision. It is reported to the human and in
// `channel -o json`, so it is part of the CLI's observable surface.
type Reason string

// Reasons, in the order Resolve consults them.
const (
	// ReasonFlag: --channel named the channel outright.
	ReasonFlag Reason = "flag"
	// ReasonConfig: HITL_CHANNEL or the config file named it. The config
	// loader overlays the environment before anything here runs, so the two
	// are indistinguishable by the time a decision is taken.
	ReasonConfig Reason = "config"
	// ReasonAway: the policy is auto and the Away marker is set.
	ReasonAway Reason = "away marker"
	// ReasonPresent: the policy is auto and no Away marker is set.
	ReasonPresent Reason = "no away marker"
	// ReasonStaleAway: the policy is auto and the Away marker expired.
	ReasonStaleAway Reason = "away marker expired"
	// ReasonDefault: nothing said anything, so questions are delivered.
	ReasonDefault Reason = "default"
	// ReasonAFK: the human declared unavailable mode (AFK).
	ReasonAFK Reason = "afk marker"
)

// Decision is the resolved routing outcome and the reason for it.
type Decision struct {
	Channel Channel `json:"channel"`
	// Policy is the effective setting the decision came from: messenger,
	// terminal, or auto.
	Policy Policy `json:"policy"`
	Reason Reason `json:"reason"`
	// AwayUntil is when the Away marker lapses. Zero when the marker is
	// absent or carries no expiry.
	AwayUntil time.Time `json:"away_until,omitzero"`
}

// Delivers reports whether a Question may reach a messenger.
func (d Decision) Delivers() bool { return d.Channel == Messenger }

// Policy is a channel setting before the Away marker is consulted.
type Policy string

// Policies. The zero value is not a policy: Resolve maps "unset" to
// PolicyMessenger so that an installation which never heard of this feature
// keeps delivering questions.
const (
	PolicyMessenger Policy = Policy(Messenger)
	PolicyTerminal  Policy = Policy(Terminal)
	PolicyAuto      Policy = Policy(Auto)
)

// ErrUnknown reports a value that is not a channel.
var ErrUnknown = errors.New("unknown channel")

// ParsePolicy reads a --channel value or a config value. An empty string is
// not a policy and is rejected; callers decide what "unset" means.
func ParsePolicy(v string) (Policy, error) {
	switch Policy(strings.ToLower(strings.TrimSpace(v))) {
	case PolicyMessenger:
		return PolicyMessenger, nil
	case PolicyTerminal:
		return PolicyTerminal, nil
	case PolicyAuto:
		return PolicyAuto, nil
	default:
		return "", fmt.Errorf("%w %q: expected %q, %q, or %q",
			ErrUnknown, v, Messenger, Terminal, Auto)
	}
}

// Marker is the state the human toggles by walking away and coming back.
type Marker struct {
	// Set reports that the marker file exists.
	Set bool
	// AFK reports whether the marker represents an AFK declaration.
	AFK bool
	// Until is when the marker lapses. Zero means it never does.
	Until time.Time
	// Malformed records that the file existed but did not parse.
	Malformed bool
}

// ActiveAFK reports whether the marker is an active AFK declaration at now.
//
// A malformed AFK expiry must not downgrade to message delivery; it remains active.
func (m Marker) ActiveAFK(now time.Time) bool {
	if !m.Set || !m.AFK {
		return false
	}
	if m.Malformed || m.Until.IsZero() {
		return true
	}
	return now.Before(m.Until)
}

// Away reports whether the marker is in force at now.
//
// A malformed marker counts as away. The two failure modes are not
// symmetrical: routing to the terminal when nobody is there strands the run
// until the human comes back, while routing to the messenger when they are
// present costs one unwanted notification. An AFK marker is not an Away marker.
func (m Marker) Away(now time.Time) bool {
	if m.AFK {
		return false
	}
	switch {
	case !m.Set:
		return false
	case m.Malformed:
		return true
	case m.Until.IsZero():
		return true
	default:
		return now.Before(m.Until)
	}
}

// Resolve picks the channel. explicit is the --channel value ("" when the flag
// was absent), configured is the config/environment value ("" when unset).
// Active AFK overrides explicit and configured channels, resolving to afk.
func Resolve(explicit, configured string, m Marker, now time.Time) (Decision, error) {
	policy := PolicyMessenger
	reason := ReasonDefault
	switch {
	case strings.TrimSpace(explicit) != "":
		p, err := ParsePolicy(explicit)
		if err != nil {
			return Decision{}, err
		}
		policy, reason = p, ReasonFlag
	case strings.TrimSpace(configured) != "":
		p, err := ParsePolicy(configured)
		if err != nil {
			return Decision{}, err
		}
		policy, reason = p, ReasonConfig
	}

	if m.ActiveAFK(now) {
		return Decision{
			Channel:   AFK,
			Policy:    policy,
			Reason:    ReasonAFK,
			AwayUntil: m.Until,
		}, nil
	}

	if policy != PolicyAuto {
		return Decision{Channel: Channel(policy), Policy: policy, Reason: reason}, nil
	}

	d := Decision{Policy: PolicyAuto, AwayUntil: m.Until}
	switch {
	case m.Away(now):
		d.Channel, d.Reason = Messenger, ReasonAway
	case m.Set:
		d.Channel, d.Reason = Terminal, ReasonStaleAway
	default:
		d.Channel, d.Reason = Terminal, ReasonPresent
	}
	return d, nil
}

// forever is what an Away marker with no expiry holds. The file is written for
// a human to read with `cat`, so the sentinel is a word rather than an empty
// file that looks like a bug.
const forever = "forever"

// ReadMarker loads the Away or AFK marker. An absent file is the zero Marker and not
// an error: "the human is here" is the normal state.
func ReadMarker(path string) (Marker, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is derived from the state dir
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Marker{}, nil
	case err != nil:
		return Marker{}, fmt.Errorf("read %s: %w", path, err)
	}

	text := strings.TrimSpace(string(data))
	if isAFKText(text) {
		return parseAFKMarker(text), nil
	}
	if text == "" || strings.EqualFold(text, forever) {
		return Marker{Set: true}, nil
	}
	if until, ok := parseExpiry(text); ok {
		return Marker{Set: true, Until: until}, nil
	}
	// Unparseable content is still a human saying they left; see Marker.Away.
	return Marker{Set: true, Malformed: true}, nil
}

func isAFKText(text string) bool {
	lower := strings.ToLower(text)
	return lower == "afk" ||
		strings.HasPrefix(lower, "afk ") ||
		strings.HasPrefix(lower, "afk\t") ||
		strings.HasPrefix(lower, "afk\n") ||
		strings.HasPrefix(lower, "afk\r") ||
		strings.HasPrefix(lower, "afk:")
}

func parseAFKMarker(text string) Marker {
	rem := strings.TrimSpace(text[3:])
	rem = strings.TrimPrefix(rem, ":")
	rem = strings.TrimSpace(rem)
	if rem == "" || strings.EqualFold(rem, forever) {
		return Marker{Set: true, AFK: true}
	}
	if until, ok := parseExpiry(rem); ok {
		return Marker{Set: true, AFK: true, Until: until}
	}
	return Marker{Set: true, AFK: true, Malformed: true}
}

// parseExpiry reads the timestamp form of the marker file.
func parseExpiry(text string) (time.Time, bool) {
	until, err := time.Parse(time.RFC3339, text)
	return until, err == nil
}

// WriteMarker sets the Away marker. A zero until means it never lapses.
func WriteMarker(path string, until time.Time) error {
	body := forever
	if !until.IsZero() {
		body = until.Format(time.RFC3339)
	}
	return atomicWriteMarker(path, []byte(body+"\n"))
}

// WriteAFKMarker sets the AFK marker. A zero until means it never lapses.
func WriteAFKMarker(path string, until time.Time) error {
	body := "afk"
	if !until.IsZero() {
		body = "afk " + until.Format(time.RFC3339Nano)
	}
	return atomicWriteMarker(path, []byte(body+"\n"))
}

func atomicWriteMarker(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".away-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp marker in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	tmpPath = ""
	return nil
}

// ClearMarker removes the Away marker, reporting whether one was there.
func ClearMarker(path string) (bool, error) {
	err := os.Remove(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("remove %s: %w", path, err)
	}
}
