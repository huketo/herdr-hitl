// Package config loads herdr-hitl settings from config.toml, a .env file, and
// the process environment, in that order of increasing precedence.
//
// The layout mirrors the official Herdr plugin examples: user-editable files
// live in HERDR_PLUGIN_CONFIG_DIR, never in the plugin checkout, because a
// GitHub-installed plugin root is replaced wholesale on reinstall.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/huketo/herdr-hitl/internal/paths"
)

// DefaultTimeout is how long a question waits for a human when neither the
// caller nor the config says otherwise.
const DefaultTimeout = 30 * time.Minute

// Transport names.
const (
	TransportTelegram = "telegram"
	TransportDiscord  = "discord"
)

// Duration is a time.Duration that reads as a string in TOML and env vars.
type Duration time.Duration

// UnmarshalText parses "30m", "2h", "0".
func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(strings.TrimSpace(string(text)))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalText renders the duration.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// String renders the duration.
func (d Duration) String() string { return time.Duration(d).String() }

// Duration converts to the standard type.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// Config is the whole settings surface.
type Config struct {
	// Transports lists the transports used when `ask` does not name one.
	// Empty means every enabled transport.
	Transports []string `toml:"transports"`
	// Timeout is the default answer deadline. Zero means wait forever.
	Timeout Duration `toml:"timeout"`

	Telegram Telegram `toml:"telegram"`
	Discord  Discord  `toml:"discord"`
	Daemon   Daemon   `toml:"daemon"`
	Herdr    Herdr    `toml:"herdr"`

	// source records where the config was read from, for `doctor` output.
	source string
}

// Telegram configures the Telegram Bot API transport.
type Telegram struct {
	// Enabled turns the transport on. Defaults to true once a token is set.
	Enabled *bool `toml:"enabled"`
	// BotToken is the token from @BotFather.
	BotToken string `toml:"bot_token"`
	// ChatID is the destination chat: a numeric user, group, or channel id.
	ChatID string `toml:"chat_id"`
	// AllowedUserIDs restricts who may answer. Empty means anyone with access
	// to the chat, which for a private chat is only the owner.
	AllowedUserIDs []string `toml:"allowed_user_ids"`
	// APIBase overrides the Bot API endpoint for self-hosted bot API servers.
	APIBase string `toml:"api_base"`
}

// Discord configures the Discord gateway transport.
type Discord struct {
	// Enabled turns the transport on. Defaults to true once a token is set.
	Enabled *bool `toml:"enabled"`
	// BotToken is the bot token from the Discord developer portal.
	BotToken string `toml:"bot_token"`
	// ChannelID is the destination channel. Leave empty to DM UserID.
	ChannelID string `toml:"channel_id"`
	// UserID opens (or reuses) a DM channel with that user. A DM avoids the
	// privileged Message Content intent for free-text replies.
	UserID string `toml:"user_id"`
	// AllowedUserIDs restricts who may answer.
	AllowedUserIDs []string `toml:"allowed_user_ids"`
	// MessageContentIntent must be enabled in the developer portal before it
	// can be requested; without it, free-text replies only work in DMs.
	MessageContentIntent bool `toml:"message_content_intent"`
}

// Daemon configures the resident process.
type Daemon struct {
	// IdleShutdown stops the daemon after this long with nothing pending.
	// Zero keeps it resident, which is the right default: reconnecting to
	// Discord costs an IDENTIFY, and those are rate limited to 1000/24h.
	IdleShutdown Duration `toml:"idle_shutdown"`
	// LogLevel is one of debug, info, warn, error.
	LogLevel string `toml:"log_level"`
}

// Herdr configures the optional callbacks into the Herdr CLI.
type Herdr struct {
	// Notifications raises a Herdr toast when a question is posted and when
	// it is answered.
	Notifications *bool `toml:"notifications"`
	// PaneTokens publishes a `$hitl` sidebar token on the asking pane while
	// the question is outstanding.
	PaneTokens *bool `toml:"pane_tokens"`
}

// IsEnabled reports whether the Telegram transport should be started.
func (t Telegram) IsEnabled() bool { return boolOr(t.Enabled, t.BotToken != "") }

// IsEnabled reports whether the Discord transport should be started.
func (d Discord) IsEnabled() bool { return boolOr(d.Enabled, d.BotToken != "") }

// NotificationsEnabled reports whether Herdr toasts are wanted.
func (h Herdr) NotificationsEnabled() bool { return boolOr(h.Notifications, true) }

// PaneTokensEnabled reports whether sidebar tokens are wanted.
func (h Herdr) PaneTokensEnabled() bool { return boolOr(h.PaneTokens, true) }

func boolOr(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

// Source reports the config file the settings came from, or "" for defaults.
func (c *Config) Source() string { return c.source }

// Default returns the zero-config baseline.
func Default() *Config {
	return &Config{
		Timeout: Duration(DefaultTimeout),
		Daemon:  Daemon{LogLevel: "info"},
	}
}

// Load reads config.toml, overlays .env, then overlays the process
// environment. A missing config file is not an error: environment-only setups
// are the common case inside Herdr.
func Load() (*Config, error) {
	dir, err := paths.ConfigDir()
	if err != nil {
		return nil, err
	}
	return LoadFrom(dir)
}

// LoadFrom reads configuration out of an explicit directory.
func LoadFrom(dir string) (*Config, error) {
	cfg := Default()

	file := dir + string(os.PathSeparator) + "config.toml"
	data, err := os.ReadFile(file) //nolint:gosec // path is user-owned config
	switch {
	case err == nil:
		if err := toml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", file, err)
		}
		cfg.source = file
	case errors.Is(err, os.ErrNotExist):
		// defaults + environment only
	default:
		return nil, fmt.Errorf("read %s: %w", file, err)
	}

	env, err := loadDotEnv(dir + string(os.PathSeparator) + ".env")
	if err != nil {
		return nil, err
	}
	cfg.applyEnv(func(key string) string {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			return v
		}
		return env[key]
	})

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyEnv overlays environment values. Both the namespaced HITL_* names and
// the bare vendor names used by the Herdr plugin examples are accepted.
func (c *Config) applyEnv(get func(string) string) {
	if v := get("HITL_TRANSPORTS"); v != "" {
		c.Transports = splitList(v)
	}
	if v := get("HITL_TIMEOUT"); v != "" {
		var d Duration
		if err := d.UnmarshalText([]byte(v)); err == nil {
			c.Timeout = d
		}
	}
	if v := get("HITL_LOG_LEVEL"); v != "" {
		c.Daemon.LogLevel = v
	}
	if v := get("HITL_IDLE_SHUTDOWN"); v != "" {
		var d Duration
		if err := d.UnmarshalText([]byte(v)); err == nil {
			c.Daemon.IdleShutdown = d
		}
	}

	if v := firstEnv(get, "HITL_TELEGRAM_BOT_TOKEN", "TELEGRAM_BOT_TOKEN"); v != "" {
		c.Telegram.BotToken = v
	}
	if v := firstEnv(get, "HITL_TELEGRAM_CHAT_ID", "TELEGRAM_CHAT_ID"); v != "" {
		c.Telegram.ChatID = v
	}
	if v := firstEnv(get, "HITL_TELEGRAM_ALLOWED_USER_IDS", "TELEGRAM_ALLOWED_USER_IDS"); v != "" {
		c.Telegram.AllowedUserIDs = splitList(v)
	}
	if v := get("HITL_TELEGRAM_API_BASE"); v != "" {
		c.Telegram.APIBase = v
	}
	if v := firstEnv(get, "HITL_TELEGRAM_ENABLED", "TELEGRAM_ENABLED"); v != "" {
		c.Telegram.Enabled = parseBool(v)
	}

	if v := firstEnv(get, "HITL_DISCORD_BOT_TOKEN", "DISCORD_BOT_TOKEN"); v != "" {
		c.Discord.BotToken = v
	}
	if v := firstEnv(get, "HITL_DISCORD_CHANNEL_ID", "DISCORD_CHANNEL_ID"); v != "" {
		c.Discord.ChannelID = v
	}
	if v := firstEnv(get, "HITL_DISCORD_USER_ID", "DISCORD_USER_ID"); v != "" {
		c.Discord.UserID = v
	}
	if v := firstEnv(get, "HITL_DISCORD_ALLOWED_USER_IDS", "DISCORD_ALLOWED_USER_IDS"); v != "" {
		c.Discord.AllowedUserIDs = splitList(v)
	}
	if v := firstEnv(get, "HITL_DISCORD_ENABLED", "DISCORD_ENABLED"); v != "" {
		c.Discord.Enabled = parseBool(v)
	}
	if v := get("HITL_DISCORD_MESSAGE_CONTENT_INTENT"); v != "" {
		if b := parseBool(v); b != nil {
			c.Discord.MessageContentIntent = *b
		}
	}

	if v := get("HITL_HERDR_NOTIFICATIONS"); v != "" {
		c.Herdr.Notifications = parseBool(v)
	}
	if v := get("HITL_HERDR_PANE_TOKENS"); v != "" {
		c.Herdr.PaneTokens = parseBool(v)
	}
}

// Validate rejects settings that would fail later, at upload time, with a much
// worse error message.
func (c *Config) Validate() error {
	if c.Timeout < 0 {
		return errors.New("config: timeout must not be negative")
	}
	for _, name := range c.Transports {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case TransportTelegram, TransportDiscord, "", "all":
		default:
			return fmt.Errorf("config: unknown transport %q", name)
		}
	}
	if c.Telegram.IsEnabled() {
		if c.Telegram.BotToken == "" {
			return errors.New("config: telegram is enabled but bot_token is empty")
		}
		if c.Telegram.ChatID == "" {
			return errors.New("config: telegram is enabled but chat_id is empty")
		}
	}
	if c.Discord.IsEnabled() {
		if c.Discord.BotToken == "" {
			return errors.New("config: discord is enabled but bot_token is empty")
		}
		if c.Discord.ChannelID == "" && c.Discord.UserID == "" {
			return errors.New("config: discord is enabled but neither channel_id nor user_id is set")
		}
	}
	switch strings.ToLower(c.Daemon.LogLevel) {
	case "", "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: unknown log_level %q", c.Daemon.LogLevel)
	}
	return nil
}

// EnabledTransports lists the transports that are configured well enough to
// start.
func (c *Config) EnabledTransports() []string {
	var out []string
	if c.Telegram.IsEnabled() {
		out = append(out, TransportTelegram)
	}
	if c.Discord.IsEnabled() {
		out = append(out, TransportDiscord)
	}
	return out
}

// DefaultTransports resolves the transports an `ask` without --transport uses.
func (c *Config) DefaultTransports() []string {
	enabled := c.EnabledTransports()
	if len(c.Transports) == 0 {
		return enabled
	}
	allowed := make(map[string]struct{}, len(enabled))
	for _, name := range enabled {
		allowed[name] = struct{}{}
	}
	var out []string
	for _, name := range c.Transports {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "all" {
			return enabled
		}
		if _, ok := allowed[name]; ok {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return enabled
	}
	return out
}

func firstEnv(get func(string) string, keys ...string) string {
	for _, k := range keys {
		if v := get(k); v != "" {
			return v
		}
	}
	return ""
}

func splitList(v string) []string {
	fields := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func parseBool(v string) *bool {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return nil
	}
	return &b
}
