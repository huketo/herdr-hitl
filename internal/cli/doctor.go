package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/ipc"
	"github.com/huketo/herdr-hitl/internal/paths"
)

// checkState grades a single doctor check.
type checkState string

const (
	stateOK   checkState = "ok"
	stateWarn checkState = "warn"
	stateFail checkState = "fail"
)

// check is one line of the doctor report.
type check struct {
	Name   string     `json:"name"`
	State  checkState `json:"state"`
	Detail string     `json:"detail,omitempty"`
}

// report is the structured form of `doctor -o json`.
type report struct {
	OK      bool    `json:"ok"`
	Version string  `json:"version"`
	Go      string  `json:"go"`
	OS      string  `json:"os"`
	Arch    string  `json:"arch"`
	Checks  []check `json:"checks"`
}

func newDoctorCommand(g *globals) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check configuration, transports, daemon, and Herdr integration",
		Long: "Report what herdr-hitl can see. Tokens are never printed, only a\n" +
			"masked fingerprint. Exits 1 when something required is missing.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != formatText && format != formatJSON {
				return usagef("--format %q: expected %q or %q", format, formatText, formatJSON)
			}
			ctx, stop := signalContext(cmd.Context())
			defer stop()

			rep := runChecks(ctx, g)
			if format == formatJSON {
				if err := writeJSON(cmd.OutOrStdout(), rep); err != nil {
					return err
				}
			} else if err := writeReport(cmd.OutOrStdout(), rep); err != nil {
				return err
			}
			if !rep.OK {
				return silentCode(ExitError, errors.New("doctor: configuration is incomplete"))
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&format, "format", "o", formatText, "output format: text | json")
	return cmd
}

// runChecks collects the whole report. It never returns an error: a broken
// environment is the thing being reported, not a reason to stop.
func runChecks(ctx context.Context, g *globals) *report {
	rep := &report{
		Version: g.info.Version,
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
	add := func(name string, state checkState, format string, args ...any) {
		rep.Checks = append(rep.Checks, check{Name: name, State: state, Detail: fmt.Sprintf(format, args...)})
	}

	configFile, err := paths.ConfigFile()
	switch {
	case err != nil:
		add("config dir", stateFail, "%v", err)
	default:
		if _, statErr := os.Stat(configFile); statErr == nil {
			add("config file", stateOK, "%s", configFile)
		} else {
			add("config file", stateWarn, "%s (absent; using defaults and environment)", configFile)
		}
		checkStrandedHerdrConfig(add, configFile)
	}

	if stateDir, err := paths.StateDir(); err != nil {
		add("state dir", stateFail, "%v", err)
	} else {
		add("state dir", stateOK, "%s", stateDir)
	}

	endpoint, err := g.endpoint()
	if err != nil {
		add("socket", stateFail, "%v", err)
	} else {
		add("socket", stateOK, "%s", endpoint)
	}

	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		add("config", stateFail, "%v", cfgErr)
	} else {
		add("config", stateOK, "loaded from %s", sourceOr(cfg))
		addChannelCheck(add, cfg)
		addTransportChecks(add, cfg)
	}

	if endpoint != "" {
		if resp, err := ipc.Call(ctx, endpoint, &ipc.Request{Op: ipc.OpStatus}); err != nil {
			add("daemon", stateWarn, "not running (it starts on the first `ask`)")
		} else if st := resp.Status; st != nil {
			add("daemon", stateOK, "pid %d, version %s, %d pending, up %s, transports %s",
				st.PID, st.Version, st.Pending, st.Uptime, listOr(st.Transports, "none"))
			// A live transport knows things the config cannot: which bot it
			// authenticated as, and whether the destination can accept a
			// typed answer at all.
			for _, detail := range st.Descriptions {
				add("connection", stateOK, "%s", detail)
			}
		} else {
			add("daemon", stateWarn, "running but reported no status")
		}
	}

	if path, ok := herdrBinary(); ok {
		add("herdr cli", stateOK, "%s", path)
	} else {
		add("herdr cli", stateWarn, "not found on PATH (notifications and pane tokens are disabled)")
	}

	add("build", stateOK, "%s %s/%s (%s)", rep.Go, rep.OS, rep.Arch, versionOr(g.info.Version))

	rep.OK = true
	for _, c := range rep.Checks {
		if c.State == stateFail {
			rep.OK = false
		}
	}
	return rep
}

// addChannelCheck reports where a question would go right now.
//
// It is a warning rather than an ok when nothing would be delivered: a
// perfectly configured transport that never receives a question is the one
// symptom this report exists to explain.
func addChannelCheck(add func(string, checkState, string, ...any), cfg *config.Config) {
	decision, err := resolveChannel("", cfg)
	if err != nil {
		add("channel", stateFail, "%v", err)
		return
	}
	if decision.Delivers() {
		add("channel", stateOK, "%s", describe(decision))
		return
	}
	add("channel", stateWarn, "%s; `ask` exits 5 without sending. `herdr-hitl away` switches it", describe(decision))
}

// addTransportChecks reports whether each transport is usable, without ever
// printing a token.
func addTransportChecks(add func(string, checkState, string, ...any), cfg *config.Config) {
	enabled := cfg.EnabledTransports()
	if len(enabled) == 0 {
		add("transports", stateFail, "none enabled; set a bot token (see `herdr-hitl config init`)")
		return
	}
	add("transports", stateOK, "%s", strings.Join(enabled, ", "))

	if cfg.Telegram.IsEnabled() {
		add("telegram", telegramState(cfg.Telegram), "token %s, chat_id %s, allowed_user_ids %s",
			mask(cfg.Telegram.BotToken), presence(cfg.Telegram.ChatID), listOr(cfg.Telegram.AllowedUserIDs, "any"))
	}
	if cfg.Discord.IsEnabled() {
		target := "channel_id " + presence(cfg.Discord.ChannelID)
		if cfg.Discord.ChannelID == "" {
			target = "user_id " + presence(cfg.Discord.UserID) + " (DM)"
		}
		add("discord", discordState(cfg.Discord), "token %s, %s, allowed_user_ids %s",
			mask(cfg.Discord.BotToken), target, listOr(cfg.Discord.AllowedUserIDs, "any"))
	}
}

func telegramState(t config.Telegram) checkState {
	if t.BotToken == "" || t.ChatID == "" {
		return stateFail
	}
	return stateOK
}

func discordState(d config.Discord) checkState {
	if d.BotToken == "" || (d.ChannelID == "" && d.UserID == "") {
		return stateFail
	}
	return stateOK
}

// mask reduces a secret to a fingerprint that is enough to tell two tokens
// apart and useless to anyone reading a pasted terminal.
func mask(secret string) string {
	if secret == "" {
		return "missing"
	}
	r := []rune(secret)
	if len(r) < 12 {
		return "set"
	}
	return string(r[:6]) + "…" + string(r[len(r)-3:])
}

func presence(v string) string {
	if v == "" {
		return "missing"
	}
	return v
}

func sourceOr(cfg *config.Config) string {
	if src := cfg.Source(); src != "" {
		return src
	}
	return "defaults and environment"
}

func versionOr(v string) string {
	if v == "" {
		return "dev"
	}
	return v
}

// herdrBinary locates the Herdr CLI the same way the daemon does.
func herdrBinary() (string, bool) {
	if p := os.Getenv("HERDR_BIN_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	p, err := exec.LookPath("herdr")
	if err != nil {
		return "", false
	}
	return p, true
}

func writeReport(w io.Writer, rep *report) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range rep.Checks {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", stateMark(c.State), c.Name, c.Detail)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if !rep.OK {
		_, err := fmt.Fprintln(w, "\nsomething required is missing; see the FAIL lines above")
		return err
	}
	return nil
}

func stateMark(s checkState) string {
	switch s {
	case stateOK:
		return "OK  "
	case stateWarn:
		return "WARN"
	default:
		return "FAIL"
	}
}

// checkStrandedHerdrConfig reports a config file sitting in the directory
// Herdr injects for plugin processes.
//
// Earlier versions read that directory when it was set, which split an
// installation in two: plugin actions and the startup hook used it, while the
// pane an agent asks from used the XDG location. Configuration is now resolved
// identically everywhere, so a file left there is inert — and inert config
// holding a bot token is exactly the kind of thing that should not fail
// silently.
func checkStrandedHerdrConfig(add func(string, checkState, string, ...any), active string) {
	dir, ok := paths.HerdrConfigDir()
	if !ok || dir == filepath.Dir(active) {
		return
	}
	for _, name := range []string{"config.toml", ".env"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		add("stranded config", stateWarn,
			"%s is not read; configuration lives at %s. Move it there or delete it.", path, active)
	}
}
