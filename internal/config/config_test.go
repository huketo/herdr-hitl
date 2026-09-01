package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/huketo/herdr-hitl/internal/config"
)

// writeConfigDir materialises a config directory with the given files.
func writeConfigDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestLoadFromMissingDirectoryYieldsDefaults(t *testing.T) {
	cfg, err := config.LoadFrom(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Timeout.Duration() != config.DefaultTimeout {
		t.Errorf("Timeout = %s, want %s", cfg.Timeout, config.DefaultTimeout)
	}
	if cfg.Source() != "" {
		t.Errorf("Source() = %q, want empty for a defaults-only load", cfg.Source())
	}
	if got := cfg.EnabledTransports(); len(got) != 0 {
		t.Errorf("EnabledTransports() = %v, want none", got)
	}
}

func TestLoadFromReadsToml(t *testing.T) {
	dir := writeConfigDir(t, map[string]string{
		"config.toml": `
transports = ["telegram"]
timeout = "10m"

[telegram]
bot_token = "123:abc"
chat_id = "111222333"
allowed_user_ids = ["111222333"]

[daemon]
idle_shutdown = "2h"
log_level = "debug"

[herdr]
notifications = false
`,
	})

	cfg, err := config.LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Timeout.Duration() != 10*time.Minute {
		t.Errorf("Timeout = %s, want 10m", cfg.Timeout)
	}
	if cfg.Telegram.ChatID != "111222333" {
		t.Errorf("ChatID = %q", cfg.Telegram.ChatID)
	}
	if !cfg.Telegram.IsEnabled() {
		t.Error("telegram should be enabled once a token is present")
	}
	if cfg.Discord.IsEnabled() {
		t.Error("discord should stay disabled with no token")
	}
	if cfg.Daemon.IdleShutdown.Duration() != 2*time.Hour {
		t.Errorf("IdleShutdown = %s, want 2h", cfg.Daemon.IdleShutdown)
	}
	if cfg.Herdr.NotificationsEnabled() {
		t.Error("notifications = false in TOML must win over the default")
	}
	if !cfg.Herdr.PaneTokensEnabled() {
		t.Error("pane_tokens defaults to true when unset")
	}
	if cfg.Source() != filepath.Join(dir, "config.toml") {
		t.Errorf("Source() = %q", cfg.Source())
	}
}

func TestPrecedenceEnvBeatsDotEnvBeatsToml(t *testing.T) {
	dir := writeConfigDir(t, map[string]string{
		"config.toml": "[telegram]\nbot_token = \"from-toml\"\nchat_id = \"from-toml\"\n",
		".env":        "HITL_TELEGRAM_BOT_TOKEN=from-dotenv\nHITL_TELEGRAM_CHAT_ID=from-dotenv\n",
	})

	cfg, err := config.LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Telegram.BotToken != "from-dotenv" {
		t.Errorf("BotToken = %q, want the .env value to beat TOML", cfg.Telegram.BotToken)
	}

	t.Setenv("HITL_TELEGRAM_BOT_TOKEN", "from-env")
	cfg, err = config.LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Telegram.BotToken != "from-env" {
		t.Errorf("BotToken = %q, want the process environment to win", cfg.Telegram.BotToken)
	}
}

func TestVendorEnvNamesAreAccepted(t *testing.T) {
	// The official Herdr plugin examples use bare TELEGRAM_* / DISCORD_*
	// names; accepting them means an existing .env keeps working.
	dir := writeConfigDir(t, nil)
	t.Setenv("TELEGRAM_BOT_TOKEN", "vendor-token")
	t.Setenv("TELEGRAM_CHAT_ID", "42")
	t.Setenv("DISCORD_BOT_TOKEN", "discord-token")
	t.Setenv("DISCORD_CHANNEL_ID", "99")

	cfg, err := config.LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Telegram.BotToken != "vendor-token" || cfg.Telegram.ChatID != "42" {
		t.Errorf("telegram = %+v", cfg.Telegram)
	}
	if cfg.Discord.BotToken != "discord-token" || cfg.Discord.ChannelID != "99" {
		t.Errorf("discord = %+v", cfg.Discord)
	}
	if got := cfg.EnabledTransports(); !slices.Equal(got, []string{"telegram", "discord"}) {
		t.Errorf("EnabledTransports() = %v", got)
	}
}

func TestNamespacedEnvBeatsVendorEnv(t *testing.T) {
	dir := writeConfigDir(t, nil)
	t.Setenv("TELEGRAM_BOT_TOKEN", "vendor")
	t.Setenv("HITL_TELEGRAM_BOT_TOKEN", "namespaced")
	t.Setenv("TELEGRAM_CHAT_ID", "42")

	cfg, err := config.LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Telegram.BotToken != "namespaced" {
		t.Errorf("BotToken = %q, want the HITL_-prefixed value", cfg.Telegram.BotToken)
	}
}

func TestValidateRejectsHalfConfiguredTransports(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		{
			name:    "telegram without chat id",
			files:   map[string]string{"config.toml": "[telegram]\nbot_token = \"t\"\n"},
			wantErr: "chat_id is empty",
		},
		{
			name:    "telegram explicitly enabled without a token",
			files:   map[string]string{"config.toml": "[telegram]\nenabled = true\n"},
			wantErr: "bot_token is empty",
		},
		{
			name:    "discord without a destination",
			files:   map[string]string{"config.toml": "[discord]\nbot_token = \"t\"\n"},
			wantErr: "neither channel_id nor user_id",
		},
		{
			name:    "unknown transport name",
			files:   map[string]string{"config.toml": "transports = [\"slack\"]\n"},
			wantErr: `unknown transport "slack"`,
		},
		{
			name:    "unknown log level",
			files:   map[string]string{"config.toml": "[daemon]\nlog_level = \"chatty\"\n"},
			wantErr: "unknown log_level",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.LoadFrom(writeConfigDir(t, tc.files))
			if err == nil {
				t.Fatalf("LoadFrom = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestDisablingATransportWithAToken(t *testing.T) {
	dir := writeConfigDir(t, map[string]string{
		"config.toml": "[telegram]\nenabled = false\nbot_token = \"t\"\nchat_id = \"1\"\n",
	})
	cfg, err := config.LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Telegram.IsEnabled() {
		t.Error("enabled = false must win over the presence of a token")
	}
}

func TestDefaultTransportsFallsBackToWhatIsEnabled(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{
			name: "no preference lists everything enabled",
			files: map[string]string{"config.toml": `
[telegram]
bot_token = "t"
chat_id = "1"
[discord]
bot_token = "d"
channel_id = "2"
`},
			want: []string{"telegram", "discord"},
		},
		{
			name: "preference narrows the fan-out",
			files: map[string]string{"config.toml": `
transports = ["discord"]
[telegram]
bot_token = "t"
chat_id = "1"
[discord]
bot_token = "d"
channel_id = "2"
`},
			want: []string{"discord"},
		},
		{
			// Naming a transport that is not configured must not silence the
			// ask; falling back to what works beats delivering nothing.
			name: "preference naming a disabled transport falls back",
			files: map[string]string{"config.toml": `
transports = ["discord"]
[telegram]
bot_token = "t"
chat_id = "1"
`},
			want: []string{"telegram"},
		},
		{
			name: "all expands",
			files: map[string]string{"config.toml": `
transports = ["all"]
[telegram]
bot_token = "t"
chat_id = "1"
`},
			want: []string{"telegram"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.LoadFrom(writeConfigDir(t, tc.files))
			if err != nil {
				t.Fatalf("LoadFrom: %v", err)
			}
			if got := cfg.DefaultTransports(); !slices.Equal(got, tc.want) {
				t.Errorf("DefaultTransports() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDurationRoundTrip(t *testing.T) {
	t.Parallel()

	var d config.Duration
	if err := d.UnmarshalText([]byte(" 90s ")); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	if d.Duration() != 90*time.Second {
		t.Errorf("Duration() = %s, want 1m30s", d)
	}
	text, err := d.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	if string(text) != "1m30s" {
		t.Errorf("MarshalText() = %q", text)
	}
	if err := d.UnmarshalText([]byte("soon")); err == nil {
		t.Error("UnmarshalText(\"soon\") = nil, want error")
	}
}
