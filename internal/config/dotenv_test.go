package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDotEnvLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		line      string
		wantKey   string
		wantValue string
		wantOK    bool
	}{
		{name: "plain", line: "KEY=value", wantKey: "KEY", wantValue: "value", wantOK: true},
		{name: "spaces around the equals", line: "  KEY = value  ", wantKey: "KEY", wantValue: "value", wantOK: true},
		{name: "export prefix", line: "export KEY=value", wantKey: "KEY", wantValue: "value", wantOK: true},
		{name: "double quoted", line: `KEY="a b"`, wantKey: "KEY", wantValue: "a b", wantOK: true},
		{name: "single quoted", line: `KEY='a b'`, wantKey: "KEY", wantValue: "a b", wantOK: true},
		{
			// A bot token contains colons and may contain dashes; splitting at
			// the first '=' keeps the rest intact.
			name:      "value containing an equals sign",
			line:      "TOKEN=123456:AAH-abc=def",
			wantKey:   "TOKEN",
			wantValue: "123456:AAH-abc=def",
			wantOK:    true,
		},
		{name: "trailing comment on an unquoted value", line: "KEY=value # why", wantKey: "KEY", wantValue: "value", wantOK: true},
		{name: "hash kept inside quotes", line: `KEY="value # why"`, wantKey: "KEY", wantValue: "value # why", wantOK: true},
		{name: "empty value", line: "KEY=", wantKey: "KEY", wantValue: "", wantOK: true},
		{name: "comment", line: "# KEY=value", wantOK: false},
		{name: "blank", line: "   ", wantOK: false},
		{name: "no equals", line: "JUST_A_WORD", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			key, value, ok := parseDotEnvLine(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if key != tc.wantKey || value != tc.wantValue {
				t.Errorf("got %q=%q, want %q=%q", key, value, tc.wantKey, tc.wantValue)
			}
		})
	}
}

func TestLoadDotEnv(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	body := "# comment\n\nA=1\nexport B=\"two\"\nC=3 # trailing\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := loadDotEnv(path)
	if err != nil {
		t.Fatalf("loadDotEnv: %v", err)
	}
	want := map[string]string{"A": "1", "B": "two", "C": "3"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}

	// A secret file must not leak into the environment of every child process
	// the plugin spawns, so the loader returns values instead of exporting.
	if _, exported := os.LookupEnv("A"); exported {
		t.Error("loadDotEnv exported A into the process environment")
	}
}

func TestLoadDotEnvMissingFileIsNotAnError(t *testing.T) {
	t.Parallel()

	got, err := loadDotEnv(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("loadDotEnv: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want an empty map", got)
	}
}
