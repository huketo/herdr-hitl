package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/huketo/herdr-hitl/internal/channel"
	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/paths"
)

// resolveChannel decides whether this invocation may reach a messenger.
//
// The only value that can still be invalid here is the flag: a bad config or
// environment value is rejected by config.Validate while the file is loaded,
// which is why the error is reported as a usage error.
func resolveChannel(explicit string, cfg *config.Config) (channel.Decision, error) {
	path, err := paths.AwayFile()
	if err != nil {
		return channel.Decision{}, failf("resolve away marker: %w", err)
	}
	marker, err := channel.ReadMarker(path)
	if err != nil {
		return channel.Decision{}, failf("%w", err)
	}
	decision, err := channel.Resolve(explicit, cfg.Channel, marker, time.Now())
	if err != nil {
		return channel.Decision{}, usagef("--channel: %w", err)
	}
	return decision, nil
}

// enforceChannel stops a delivery the operator did not want.
//
// A prompt rule an agent is supposed to follow is not enough on its own: the
// agent that forgets it is exactly the agent that wakes a phone at 2am. This
// is the same policy applied where it cannot be forgotten, with its own exit
// code so a caller can tell "ask somewhere else" apart from a real failure.
func enforceChannel(cmd *cobra.Command, verb, remedy string, d channel.Decision) error {
	if d.Delivers() {
		return nil
	}
	w := cmd.ErrOrStderr()
	_, _ = fmt.Fprintf(w, "herdr-hitl: not %s: %s\n", verb, describe(d))
	_, _ = fmt.Fprintf(w, "the human is at your own interface, so %s.\n", remedy)
	_, _ = fmt.Fprintln(w, "`herdr-hitl away` sends questions to the messenger; --channel messenger overrides once.")
	return silentCode(ExitTerminal, fmt.Errorf("channel is %s", d.Channel))
}

// describe renders a decision as one line: the channel, and what settled it.
func describe(d channel.Decision) string {
	reason := string(d.Reason)
	if d.Reason == channel.ReasonAway && !d.AwayUntil.IsZero() {
		reason = fmt.Sprintf("%s until %s", reason, d.AwayUntil.Format(time.RFC3339))
	}
	if d.Reason == channel.ReasonConfig || d.Reason == channel.ReasonFlag {
		reason = fmt.Sprintf("%s channel = %q", reason, d.Policy)
	}
	return fmt.Sprintf("channel is %s (%s)", d.Channel, reason)
}

func newChannelCommand(_ *globals) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "channel",
		Short: "Print where a question would go: messenger or terminal",
		Long: "Resolve the routing policy and the Away marker into one channel.\n\n" +
			"With -o text the channel is the only thing written to stdout, so\n" +
			"`[ \"$(herdr-hitl channel)\" = messenger ]` works. `messenger` means\n" +
			"`ask` delivers; `terminal` means it refuses with exit 5 because the\n" +
			"human is expected to be at the agent's own interface.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != formatText && format != formatJSON {
				return usagef("--format %q: expected %q or %q", format, formatText, formatJSON)
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			decision, err := resolveChannel("", cfg)
			if err != nil {
				return err
			}
			if format == formatJSON {
				return writeJSON(cmd.OutOrStdout(), decision)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), decision.Channel)
			return err
		},
	}
	cmd.Flags().StringVarP(&format, "format", "o", formatText, "output format: text | json")
	return cmd
}

func newAwayCommand(_ *globals) *cobra.Command {
	var window time.Duration
	cmd := &cobra.Command{
		Use:   "away",
		Short: "Say you left the terminal, so questions go to the messenger",
		Long: "Set the Away marker. With `channel = \"auto\"` this is what makes\n" +
			"`ask` deliver to Telegram or Discord instead of refusing.\n\n" +
			"Without --for the marker holds until `herdr-hitl here` clears it.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if window < 0 {
				return usagef("--for must not be negative")
			}
			path, err := paths.AwayFile()
			if err != nil {
				return failf("resolve away marker: %w", err)
			}
			var until time.Time
			if window > 0 {
				until = time.Now().Add(window).Truncate(time.Second)
			}
			if err := channel.WriteMarker(path, until); err != nil {
				return failf("%w", err)
			}
			if until.IsZero() {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "away marker set, with no expiry")
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "away marker set until %s\n", until.Format(time.RFC3339))
			}
			return reportChannel(cmd.OutOrStdout())
		},
	}
	cmd.Flags().DurationVar(&window, "for", 0, "clear the marker automatically after this long")
	return cmd
}

func newHereCommand(_ *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "here",
		Short: "Say you are back at the terminal, so questions stay there",
		Long: "Clear the Away marker. With `channel = \"auto\"` this is what makes\n" +
			"`ask` refuse with exit 5 so the agent asks in its own interface.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := paths.AwayFile()
			if err != nil {
				return failf("resolve away marker: %w", err)
			}
			existed, err := channel.ClearMarker(path)
			if err != nil {
				return failf("%w", err)
			}
			if existed {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "away marker cleared")
			} else {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no away marker was set")
			}
			return reportChannel(cmd.OutOrStdout())
		},
	}
}

// reportChannel prints the resolved channel after the marker changed.
//
// The marker alone does not decide anything: with `channel = "messenger"` it
// is inert, and a human who toggles it and sees no change would otherwise have
// to guess why. Printing the outcome makes an inert toggle visible at once.
func reportChannel(w io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	decision, err := resolveChannel("", cfg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, describe(decision))
	return err
}
