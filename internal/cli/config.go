package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/paths"
)

// starterConfig is written by `config init`. It documents every key the
// operator is likely to need and leaves the secrets empty, because a token in
// a file the user did not choose is a token they will forget to rotate.
const starterConfig = `# herdr-hitl configuration.
#
# Secrets may live here (this file is created 0600) or in the environment.
# Environment variables win over this file:
#
#   HITL_TELEGRAM_BOT_TOKEN / TELEGRAM_BOT_TOKEN
#   HITL_TELEGRAM_CHAT_ID   / TELEGRAM_CHAT_ID
#   HITL_DISCORD_BOT_TOKEN  / DISCORD_BOT_TOKEN
#   HITL_DISCORD_CHANNEL_ID / DISCORD_CHANNEL_ID
#   HITL_DISCORD_USER_ID
#   HITL_TRANSPORTS, HITL_TIMEOUT, HITL_LOG_LEVEL, HITL_IDLE_SHUTDOWN
#
# A .env file next to this one is read with the same names.

# Transports used by an ` + "`ask`" + ` without --transport. Empty means every
# transport that is configured.
transports = []

# Default answer deadline. "0" waits forever.
timeout = "30m"

[telegram]
# enabled = true
# Token from @BotFather.
bot_token = ""
# Destination chat: message the bot, then read the chat id from
# https://api.telegram.org/bot<TOKEN>/getUpdates
chat_id = ""
# Restrict who may answer. Empty means anyone who can see the chat.
allowed_user_ids = []
# api_base = "https://api.telegram.org"

[discord]
# enabled = true
# Bot token from the Discord developer portal.
bot_token = ""
# Post into a channel...
channel_id = ""
# ...or leave channel_id empty and DM this user instead. A DM reads free-text
# replies without the privileged Message Content intent.
user_id = ""
allowed_user_ids = []
# Only set this after enabling the intent in the developer portal.
message_content_intent = false

[daemon]
# Stop the daemon after this long with nothing pending. "0" keeps it resident,
# which is the right default: reconnecting to Discord spends an IDENTIFY, and
# those are rate limited to 1000 per 24h.
idle_shutdown = "0"
log_level = "info"

[herdr]
# Raise a Herdr toast when a question is posted and answered.
notifications = true
# Publish a $hitl sidebar token on the asking pane while a question is open.
pane_tokens = true
`

// newConfigCommand takes the globals for symmetry with the other command
// constructors; config inspection needs none of them.
func newConfigCommand(_ *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and create the configuration file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	path := &cobra.Command{
		Use:          "path",
		Short:        "Print the configuration file path",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, err := paths.ConfigFile()
			if err != nil {
				return failf("resolve config path: %w", err)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), file)
			return nil
		},
	}

	show := &cobra.Command{
		Use:          "show",
		Short:        "Print the effective configuration as TOML, with secrets masked",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return writeMaskedConfig(cmd.OutOrStdout(), cfg)
		},
	}

	var force bool
	initCmd := &cobra.Command{
		Use:          "init",
		Short:        "Write a commented starter configuration file",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConfigInit(cmd, force)
		},
	}
	initCmd.Flags().BoolVar(&force, "force", false, "overwrite an existing configuration file")

	cmd.AddCommand(path, show, initCmd)
	return cmd
}

func runConfigInit(cmd *cobra.Command, force bool) error {
	dir, err := paths.ConfigDir()
	if err != nil {
		return failf("resolve config dir: %w", err)
	}
	if err := paths.EnsureDir(dir); err != nil {
		return failf("%w", err)
	}
	file := filepath.Join(dir, "config.toml")

	if _, err := os.Stat(file); err == nil && !force {
		return failf("%s already exists (pass --force to overwrite)", file)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return failf("stat %s: %w", file, err)
	}
	if err := os.WriteFile(file, []byte(starterConfig), 0o600); err != nil {
		return failf("write %s: %w", file, err)
	}

	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "wrote %s\n\n", file)
	_, _ = fmt.Fprintln(out, "Next:")
	fmt.Fprintln(out, "  1. Set telegram.bot_token and telegram.chat_id (or the discord equivalents).")
	fmt.Fprintln(out, "  2. Run `herdr-hitl doctor` to confirm the transport is usable.")
	fmt.Fprintln(out, "  3. Run `herdr-hitl ask -t 'Ping?' -c yes -c no` to try it end to end.")
	return nil
}

// writeMaskedConfig renders the effective settings as TOML. It is written by
// hand rather than marshalled so that a token can never escape through a new
// struct field nobody remembered to mask.
func writeMaskedConfig(w io.Writer, cfg *config.Config) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# effective configuration (%s)\n", sourceOr(cfg))
	fmt.Fprintf(&b, "# secrets are masked; use `herdr-hitl config path` to edit the real file\n\n")

	fmt.Fprintf(&b, "transports = %s\n", tomlStrings(cfg.Transports))
	fmt.Fprintf(&b, "timeout = %q\n", cfg.Timeout.String())

	fmt.Fprintf(&b, "\n[telegram]\n")
	fmt.Fprintf(&b, "enabled = %t\n", cfg.Telegram.IsEnabled())
	fmt.Fprintf(&b, "bot_token = %q\n", maskToken(cfg.Telegram.BotToken))
	fmt.Fprintf(&b, "chat_id = %q\n", cfg.Telegram.ChatID)
	fmt.Fprintf(&b, "allowed_user_ids = %s\n", tomlStrings(cfg.Telegram.AllowedUserIDs))
	if cfg.Telegram.APIBase != "" {
		fmt.Fprintf(&b, "api_base = %q\n", cfg.Telegram.APIBase)
	}

	fmt.Fprintf(&b, "\n[discord]\n")
	fmt.Fprintf(&b, "enabled = %t\n", cfg.Discord.IsEnabled())
	fmt.Fprintf(&b, "bot_token = %q\n", maskToken(cfg.Discord.BotToken))
	fmt.Fprintf(&b, "channel_id = %q\n", cfg.Discord.ChannelID)
	fmt.Fprintf(&b, "user_id = %q\n", cfg.Discord.UserID)
	fmt.Fprintf(&b, "allowed_user_ids = %s\n", tomlStrings(cfg.Discord.AllowedUserIDs))
	fmt.Fprintf(&b, "message_content_intent = %t\n", cfg.Discord.MessageContentIntent)

	fmt.Fprintf(&b, "\n[daemon]\n")
	fmt.Fprintf(&b, "idle_shutdown = %q\n", cfg.Daemon.IdleShutdown.String())
	fmt.Fprintf(&b, "log_level = %q\n", logLevelOr(cfg.Daemon.LogLevel))

	fmt.Fprintf(&b, "\n[herdr]\n")
	fmt.Fprintf(&b, "notifications = %t\n", cfg.Herdr.NotificationsEnabled())
	fmt.Fprintf(&b, "pane_tokens = %t\n", cfg.Herdr.PaneTokensEnabled())

	_, err := io.WriteString(w, b.String())
	return err
}

// maskToken keeps `config show` valid TOML: an unset token is an empty
// string, not the word doctor prints for a human.
func maskToken(secret string) string {
	if secret == "" {
		return ""
	}
	return mask(secret)
}

func tomlStrings(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	quoted := make([]string, 0, len(items))
	for _, s := range items {
		quoted = append(quoted, fmt.Sprintf("%q", s))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func logLevelOr(level string) string {
	if level == "" {
		return "info"
	}
	return level
}
