package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huketo/herdr-hitl/internal/channel"
	"github.com/huketo/herdr-hitl/internal/hitl"
	"github.com/huketo/herdr-hitl/internal/paths"
)

// autoChannel puts the harness in the presence-routed policy, which is the
// only policy where the away marker decides anything.
func autoChannel(t *testing.T) {
	t.Helper()
	t.Setenv("HITL_CHANNEL", string(channel.PolicyAuto))
}

func TestAskRefusesWhileTheHumanIsAtTheTerminal(t *testing.T) {
	h := newHarness(t)
	autoChannel(t)
	h.handler.answer = &hitl.Answer{RequestID: "abc123", Status: hitl.StatusAnswered, Text: "yes"}

	code, stdout, stderr := h.run(t, "ask", "-t", "Deploy?", "-c", "yes")
	if code != ExitTerminal {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitTerminal, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing that could be mistaken for an answer", stdout)
	}
	if !strings.Contains(stderr, "channel is terminal") {
		t.Fatalf("stderr = %q, want the resolved channel", stderr)
	}
	h.handler.mu.Lock()
	defer h.handler.mu.Unlock()
	if h.handler.asked != nil {
		t.Fatal("the question reached the daemon; the gate must refuse before delivering")
	}
}

func TestNotifyRefusesWhileTheHumanIsAtTheTerminal(t *testing.T) {
	h := newHarness(t)
	autoChannel(t)

	code, _, stderr := h.run(t, "notify", "-t", "Migration finished")
	if code != ExitTerminal {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitTerminal, stderr)
	}
	h.handler.mu.Lock()
	defer h.handler.mu.Unlock()
	if h.handler.notified != nil {
		t.Fatal("the notification was delivered anyway")
	}
}

func TestAskDeliversWhileAway(t *testing.T) {
	h := newHarness(t)
	autoChannel(t)
	h.handler.answer = &hitl.Answer{RequestID: "abc123", Status: hitl.StatusAnswered, Text: "yes"}

	if code, _, stderr := h.run(t, "away"); code != ExitOK {
		t.Fatalf("away exit code = %d (stderr: %s)", code, stderr)
	}
	code, stdout, stderr := h.run(t, "ask", "-t", "Deploy?", "-c", "yes")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitOK, stderr)
	}
	if stdout != "yes\n" {
		t.Fatalf("stdout = %q, want the answer text", stdout)
	}

	if code, _, stderr := h.run(t, "here"); code != ExitOK {
		t.Fatalf("here exit code = %d (stderr: %s)", code, stderr)
	}
	if code, _, _ := h.run(t, "ask", "-t", "Deploy?", "-c", "yes"); code != ExitTerminal {
		t.Fatalf("exit code after `here` = %d, want %d", code, ExitTerminal)
	}
}

func TestAskChannelFlagOverridesThePolicy(t *testing.T) {
	h := newHarness(t)
	autoChannel(t)
	h.handler.answer = &hitl.Answer{RequestID: "abc123", Status: hitl.StatusAnswered, Text: "yes"}

	code, stdout, stderr := h.run(t, "ask", "-t", "Deploy?", "-c", "yes", "--channel", "messenger")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitOK, stderr)
	}
	if stdout != "yes\n" {
		t.Fatalf("stdout = %q, want the answer text", stdout)
	}
}

// TestAskChannelFlagRefusesWithoutAMarker guards the other direction: an
// unattended launcher that mistypes the value must not silently deliver.
func TestAskChannelFlagRefusesUnknownValues(t *testing.T) {
	h := newHarness(t)

	code, _, stderr := h.run(t, "ask", "-t", "Deploy?", "-c", "yes", "--channel", "phone")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "unknown channel") {
		t.Fatalf("stderr = %q, want the rejected value explained", stderr)
	}
}

// TestAskDeliversByDefault is the backwards-compatibility guard: an
// installation that never heard of channels keeps paging the human.
func TestAskDeliversByDefault(t *testing.T) {
	h := newHarness(t)
	h.handler.answer = &hitl.Answer{RequestID: "abc123", Status: hitl.StatusAnswered, Text: "yes"}

	if code, _, stderr := h.run(t, "away"); code != ExitOK {
		t.Fatalf("away exit code = %d (stderr: %s)", code, stderr)
	}
	if code, _, _ := h.run(t, "here"); code != ExitOK {
		t.Fatal("here failed")
	}
	code, stdout, stderr := h.run(t, "ask", "-t", "Deploy?", "-c", "yes")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitOK, stderr)
	}
	if stdout != "yes\n" {
		t.Fatalf("stdout = %q, want the answer text", stdout)
	}
}

func TestChannelCommandText(t *testing.T) {
	h := newHarness(t)
	autoChannel(t)

	code, stdout, stderr := h.run(t, "channel")
	if code != ExitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, stderr)
	}
	if stdout != "terminal\n" {
		t.Fatalf("stdout = %q, want one scriptable word", stdout)
	}

	if code, _, _ := h.run(t, "away", "--for", "2h"); code != ExitOK {
		t.Fatal("away failed")
	}
	if _, stdout, _ = h.run(t, "channel"); stdout != "messenger\n" {
		t.Fatalf("stdout = %q, want messenger while away", stdout)
	}
}

func TestChannelCommandJSON(t *testing.T) {
	h := newHarness(t)
	autoChannel(t)
	if code, _, _ := h.run(t, "away", "--for", "90m"); code != ExitOK {
		t.Fatal("away failed")
	}

	code, stdout, stderr := h.run(t, "channel", "-o", "json")
	if code != ExitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, stderr)
	}
	var got channel.Decision
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not a decision document: %v (%q)", err, stdout)
	}
	if got.Channel != channel.Messenger || got.Policy != channel.PolicyAuto {
		t.Fatalf("decision = %+v", got)
	}
	if got.Reason != channel.ReasonAway {
		t.Fatalf("reason = %q, want %q", got.Reason, channel.ReasonAway)
	}
	if got.AwayUntil.IsZero() {
		t.Fatal("away_until is missing, so a human cannot tell when the marker lapses")
	}
}

// TestAwayReportsAnInertMarker is the whole point of printing the resolved
// channel after a toggle: under the default policy the marker changes nothing,
// and a silent no-op would send the human hunting through config files.
func TestAwayReportsAnInertMarker(t *testing.T) {
	h := newHarness(t)

	code, stdout, stderr := h.run(t, "away")
	if code != ExitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stdout, "away marker set") {
		t.Fatalf("stdout = %q, want the marker action", stdout)
	}
	if !strings.Contains(stdout, "channel is messenger") {
		t.Fatalf("stdout = %q, want the resolved channel", stdout)
	}
}

func TestHereWithoutAMarker(t *testing.T) {
	h := newHarness(t)
	autoChannel(t)

	code, stdout, stderr := h.run(t, "here")
	if code != ExitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stdout, "no away marker was set") {
		t.Fatalf("stdout = %q, want the marker state reported", stdout)
	}
	if !strings.Contains(stdout, "channel is terminal") {
		t.Fatalf("stdout = %q, want the resolved channel", stdout)
	}
}

func TestAwayRejectsANegativeWindow(t *testing.T) {
	h := newHarness(t)

	if code, _, _ := h.run(t, "away", "--for", "-5m"); code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
}

// TestDoctorWarnsAboutTheTerminalChannel keeps the report able to explain the
// symptom "the transport is fine but no question ever arrives".
func TestDoctorWarnsAboutTheTerminalChannel(t *testing.T) {
	h := newHarness(t)
	autoChannel(t)

	_, stdout, _ := h.run(t, "doctor")
	if !strings.Contains(stdout, "channel is terminal") {
		t.Fatalf("doctor output = %q, want the channel check", stdout)
	}
	if !strings.Contains(stdout, "exits 5") {
		t.Fatalf("doctor output = %q, want the consequence spelled out", stdout)
	}
}

func TestConfigShowReportsTheChannel(t *testing.T) {
	h := newHarness(t)

	_, stdout, _ := h.run(t, "config", "show")
	if !strings.Contains(stdout, `channel = "messenger"`) {
		t.Fatalf("config show = %q, want the effective channel", stdout)
	}
}

func TestAskRefusesWhileAFK(t *testing.T) {
	h := newHarness(t)
	h.handler.answer = &hitl.Answer{RequestID: "abc123", Status: hitl.StatusAnswered, Text: "yes"}

	if code, _, stderr := h.run(t, "afk"); code != ExitOK {
		t.Fatalf("afk exit code = %d (stderr: %s)", code, stderr)
	}

	code, stdout, stderr := h.run(t, "ask", "-t", "Deploy?", "-c", "yes")
	if code != ExitAFK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitAFK, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing that could be mistaken for an answer", stdout)
	}
	if !strings.Contains(stderr, "channel is afk") {
		t.Fatalf("stderr = %q, want the resolved channel", stderr)
	}
	h.handler.mu.Lock()
	defer h.handler.mu.Unlock()
	if h.handler.asked != nil {
		t.Fatal("the question reached the daemon; AFK must refuse before delivering")
	}
}

func TestNotifyRefusesWhileAFK(t *testing.T) {
	h := newHarness(t)

	if code, _, stderr := h.run(t, "afk"); code != ExitOK {
		t.Fatalf("afk exit code = %d (stderr: %s)", code, stderr)
	}

	code, stdout, stderr := h.run(t, "notify", "-t", "Migration finished")
	if code != ExitAFK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitAFK, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing", stdout)
	}
	h.handler.mu.Lock()
	defer h.handler.mu.Unlock()
	if h.handler.notified != nil {
		t.Fatal("the notification was delivered anyway")
	}
}

func TestAFKOverridesMessengerFlag(t *testing.T) {
	h := newHarness(t)
	h.handler.answer = &hitl.Answer{RequestID: "abc123", Status: hitl.StatusAnswered, Text: "yes"}

	if code, _, _ := h.run(t, "afk"); code != ExitOK {
		t.Fatal("afk failed")
	}

	code, stdout, stderr := h.run(t, "ask", "-t", "Deploy?", "-c", "yes", "--channel", "messenger")
	if code != ExitAFK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitAFK, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing", stdout)
	}

	code, _, _ = h.run(t, "notify", "-t", "Hi", "--channel", "messenger")
	if code != ExitAFK {
		t.Fatalf("notify exit code = %d, want %d", code, ExitAFK)
	}
}

func TestAFKRefusesAskWithDefault(t *testing.T) {
	h := newHarness(t)

	if code, _, _ := h.run(t, "afk"); code != ExitOK {
		t.Fatal("afk failed")
	}

	code, stdout, _ := h.run(t, "ask", "-t", "Deploy?", "-c", "yes", "--default", "yes")
	if code != ExitAFK {
		t.Fatalf("exit code = %d, want %d", code, ExitAFK)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing (no synthetic approval)", stdout)
	}
}

func TestAFKRecoveryToHereAndAway(t *testing.T) {
	h := newHarness(t)
	h.handler.answer = &hitl.Answer{RequestID: "abc123", Status: hitl.StatusAnswered, Text: "yes"}

	// 1. set AFK -> ask exits 6
	if code, _, _ := h.run(t, "afk"); code != ExitOK {
		t.Fatal("afk failed")
	}
	if code, _, _ := h.run(t, "ask", "-t", "Deploy?", "-c", "yes"); code != ExitAFK {
		t.Fatalf("exit code while AFK = %d, want %d", code, ExitAFK)
	}

	// 2. here clears AFK -> under default policy, ask delivers
	code, _, stderr := h.run(t, "here")
	if code != ExitOK {
		t.Fatalf("here exit code = %d (stderr: %s)", code, stderr)
	}
	code, stdout, _ := h.run(t, "ask", "-t", "Deploy?", "-c", "yes")
	if code != ExitOK {
		t.Fatalf("ask after here failed: code = %d", code)
	}
	if stdout != "yes\n" {
		t.Fatalf("stdout = %q, want answer text", stdout)
	}

	// 3. set AFK again -> ask exits 6
	if code, _, _ = h.run(t, "afk"); code != ExitOK {
		t.Fatal("second afk failed")
	}
	if code, _, _ = h.run(t, "ask", "-t", "Deploy?", "-c", "yes"); code != ExitAFK {
		t.Fatalf("exit code while AFK = %d, want %d", code, ExitAFK)
	}

	// 4. away replaces AFK -> ask delivers
	code, _, stderr = h.run(t, "away")
	if code != ExitOK {
		t.Fatalf("away exit code = %d (stderr: %s)", code, stderr)
	}
	if code, stdout, _ = h.run(t, "ask", "-t", "Deploy?", "-c", "yes"); code != ExitOK {
		t.Fatalf("ask after away failed: code = %d", code)
	}
	if stdout != "yes\n" {
		t.Fatalf("stdout = %q, want answer text", stdout)
	}
}

func TestAFKChannelTextAndJSON(t *testing.T) {
	h := newHarness(t)

	if code, _, stderr := h.run(t, "afk", "--for", "2h"); code != ExitOK {
		t.Fatalf("afk failed: %d (%s)", code, stderr)
	}

	// Text
	code, stdout, stderr := h.run(t, "channel")
	if code != ExitOK {
		t.Fatalf("channel exit code = %d (%s)", code, stderr)
	}
	if stdout != "afk\n" {
		t.Fatalf("stdout = %q, want afk", stdout)
	}

	// JSON
	code, stdout, stderr = h.run(t, "channel", "-o", "json")
	if code != ExitOK {
		t.Fatalf("channel -o json exit code = %d (%s)", code, stderr)
	}
	var got channel.Decision
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not a decision document: %v (%q)", err, stdout)
	}
	if got.Channel != channel.AFK {
		t.Fatalf("channel = %q, want afk", got.Channel)
	}
	if got.Reason != channel.ReasonAFK {
		t.Fatalf("reason = %q, want %q", got.Reason, channel.ReasonAFK)
	}
	if got.AwayUntil.IsZero() {
		t.Fatal("away_until is zero, want expiry")
	}
}

func TestAFKExpiryReturnsToExistingRouting(t *testing.T) {
	h := newHarness(t)
	h.handler.answer = &hitl.Answer{RequestID: "abc123", Status: hitl.StatusAnswered, Text: "yes"}

	path, err := paths.AwayFile()
	if err != nil {
		t.Fatalf("AwayFile: %v", err)
	}
	// Write an expired AFK marker directly
	if err := channel.WriteAFKMarker(path, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("WriteAFKMarker: %v", err)
	}

	// Default policy is messenger -> expired AFK returns to messenger
	if _, stdout, _ := h.run(t, "channel"); stdout != "messenger\n" {
		t.Fatalf("stdout = %q, want messenger for expired AFK under default", stdout)
	}
	code, stdout, _ := h.run(t, "ask", "-t", "Deploy?", "-c", "yes")
	if code != ExitOK || stdout != "yes\n" {
		t.Fatalf("ask under expired AFK default: code=%d, stdout=%q", code, stdout)
	}

	// Auto policy -> expired AFK returns to terminal
	autoChannel(t)
	if _, stdout, _ := h.run(t, "channel"); stdout != "terminal\n" {
		t.Fatalf("stdout = %q, want terminal for expired AFK under auto", stdout)
	}
	code, _, _ = h.run(t, "ask", "-t", "Deploy?", "-c", "yes")
	if code != ExitTerminal {
		t.Fatalf("ask under expired AFK auto: code=%d, want %d", code, ExitTerminal)
	}
}

func TestAFKMalformedDoesNotDowngrade(t *testing.T) {
	h := newHarness(t)
	h.handler.answer = &hitl.Answer{RequestID: "abc123", Status: hitl.StatusAnswered, Text: "yes"}

	path, err := paths.AwayFile()
	if err != nil {
		t.Fatalf("AwayFile: %v", err)
	}
	if err := os.WriteFile(path, []byte("afk invalid-time\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, stdout, _ := h.run(t, "channel"); stdout != "afk\n" {
		t.Fatalf("stdout = %q, want afk for malformed AFK", stdout)
	}

	code, stdout, _ := h.run(t, "ask", "-t", "Deploy?", "-c", "yes")
	if code != ExitAFK {
		t.Fatalf("exit code = %d, want %d", code, ExitAFK)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing (malformed AFK must not deliver)", stdout)
	}
}

func TestAFKInvalidForNoStateMutation(t *testing.T) {
	h := newHarness(t)

	// Set a valid away marker first
	if code, _, _ := h.run(t, "away", "--for", "2h"); code != ExitOK {
		t.Fatal("away failed")
	}
	path, err := paths.AwayFile()
	if err != nil {
		t.Fatalf("AwayFile: %v", err)
	}
	orig, err := channel.ReadMarker(path)
	if err != nil || !orig.Set || orig.AFK || orig.Until.IsZero() {
		t.Fatalf("orig marker = %+v, want valid away marker", orig)
	}

	// Negative duration
	if code, _, _ := h.run(t, "afk", "--for", "-5m"); code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	afterNeg, err := channel.ReadMarker(path)
	if err != nil || !afterNeg.Set || afterNeg.AFK || !afterNeg.Until.Equal(orig.Until) {
		t.Fatalf("marker mutated after negative --for: %+v", afterNeg)
	}

	// Unparseable duration
	if code, _, _ := h.run(t, "afk", "--for", "invalid"); code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	afterInvalid, err := channel.ReadMarker(path)
	if err != nil || !afterInvalid.Set || afterInvalid.AFK || !afterInvalid.Until.Equal(orig.Until) {
		t.Fatalf("marker mutated after invalid --for: %+v", afterInvalid)
	}
}
