// Command herdr-hitl blocks a coding agent on a human decision delivered over
// Telegram or Discord.
package main

import (
	"os"

	"github.com/huketo/herdr-hitl/internal/cli"
)

// Build metadata, stamped in by the release build:
//
//	go build -ldflags "-X main.version=... -X main.commit=... -X main.date=..."
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(cli.Main(cli.BuildInfo{Version: version, Commit: commit, Date: date}))
}
