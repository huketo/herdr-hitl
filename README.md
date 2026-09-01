# herdr-hitl

A [Herdr](https://herdr.dev) plugin that lets a coding agent block on a human decision delivered over Telegram or Discord. An agent halfway through a long run hits something it must not decide alone — force-push over a colleague's branch, pick between two migration strategies, supply a credential it cannot read, resolve a requirement the issue never stated. Today it either guesses, or it stops and waits for someone to notice a terminal that nobody is watching. `herdr-hitl` gives it a third option: one blocking command, `herdr-hitl ask`, that pushes the question to your phone with buttons or a text box, waits for you to answer, and prints the answer on stdout. Exit code says whether you answered, let it time out, or declined.

## Quickstart

Install as a Herdr plugin (builds the binary, registers a startup hook that keeps the daemon alive):

```sh
herdr plugin install huketo/herdr-hitl
herdr plugin action invoke huketo.hitl.config-init
herdr plugin action invoke huketo.hitl.install-cli   # puts herdr-hitl on PATH
```

Or install the CLI standalone — the plugin manifest is optional, everything works without Herdr:

```sh
go install github.com/huketo/herdr-hitl/cmd/herdr-hitl@latest
```

Then configure one messenger and check it:

```sh
$EDITOR "$(herdr plugin config-dir huketo.hitl)/config.toml"
herdr-hitl doctor
herdr-hitl ask -t "Smoke test" -m "Reply with anything." --timeout 2m
```

## Configuration

Two files, both in the config directory:

| File | Purpose |
| --- | --- |
| `config.toml` | Everything non-secret. Written by `herdr-hitl config init`. |
| `.env` | Tokens and ids. Copy from `.env.example`, `chmod 600`. |

Where they live:

| Platform | Config directory | State directory |
| --- | --- | --- |
| Linux | `$XDG_CONFIG_HOME/herdr-hitl` (default `~/.config/herdr-hitl`) | `$XDG_STATE_HOME/herdr-hitl` (default `~/.local/state/herdr-hitl`) |
| macOS | `~/Library/Application Support/herdr-hitl` | `$XDG_STATE_HOME/herdr-hitl` (default `~/.local/state/herdr-hitl`) |
| Windows | `%APPDATA%\herdr-hitl` | `%LOCALAPPDATA%\herdr-hitl` |

Under Herdr, `HERDR_PLUGIN_CONFIG_DIR` and `HERDR_PLUGIN_STATE_DIR` win, so the plugin keeps its files where `herdr plugin config-dir huketo.hitl` says. `HITL_CONFIG_DIR` and `HITL_STATE_DIR` override everything. Run `herdr-hitl config path` to see the resolved paths.

### `config.toml`

```toml
# Transports used when `ask` omits --transport, in delivery order.
transports = ["telegram"]
# Default deadline for `ask`. 0 waits forever.
timeout = "30m"

[telegram]
enabled = true
bot_token = ""            # prefer .env
chat_id = "123456789"
allowed_user_ids = ["123456789"]   # empty = anyone in the chat may answer
api_base = ""             # override only for a local Bot API server

[discord]
enabled = false
bot_token = ""            # prefer .env
channel_id = ""           # blank + user_id set = deliver by DM
user_id = ""
allowed_user_ids = []
message_content_intent = false   # true only for free-text replies in a guild channel

[daemon]
idle_shutdown = "0s"      # 0 = stay resident; otherwise exit after this long with nothing pending
log_level = "info"        # debug | info | warn | error

[herdr]
notifications = true      # mirror questions to Herdr's notification surface
pane_tokens = true        # expose pending state as a pane token for the status bar
```

### Environment variables

| Variable | Overrides | Alias |
| --- | --- | --- |
| `HITL_TELEGRAM_BOT_TOKEN` | `telegram.bot_token` | `TELEGRAM_BOT_TOKEN` |
| `HITL_TELEGRAM_CHAT_ID` | `telegram.chat_id` | `TELEGRAM_CHAT_ID` |
| `HITL_DISCORD_BOT_TOKEN` | `discord.bot_token` | `DISCORD_BOT_TOKEN` |
| `HITL_DISCORD_CHANNEL_ID` | `discord.channel_id` | `DISCORD_CHANNEL_ID` |
| `HITL_DISCORD_USER_ID` | `discord.user_id` | — |
| `HITL_TRANSPORTS` | `transports` (comma-separated) | — |
| `HITL_TIMEOUT` | `timeout` | — |
| `HITL_LOG_LEVEL` | `daemon.log_level` | — |
| `HITL_IDLE_SHUTDOWN` | `daemon.idle_shutdown` | — |
| `HITL_HERDR_NOTIFICATIONS` | `herdr.notifications` | — |
| `HITL_HERDR_PANE_TOKENS` | `herdr.pane_tokens` | — |
| `HITL_CONFIG_DIR` | config directory | `HERDR_PLUGIN_CONFIG_DIR` |
| `HITL_STATE_DIR` | state directory | `HERDR_PLUGIN_STATE_DIR` |
| `HITL_SOCKET` | daemon endpoint path | — |
| `HITL_AGENT` | default `--agent` label | — |

## CLI reference

### `ask` — post a question and block until it is answered

| Flag | Meaning |
| --- | --- |
| `-t, --title string` | One-line summary. Shown as the message heading. |
| `-m, --message string` | Question body, Markdown. `-` reads stdin. |
| `--message-file PATH` | Read the body from a file instead. |
| `-c, --choice strings` | Repeatable. `id=Label` or bare `Label` (id slugified from the label). |
| `--primary strings` | Choice ids rendered as the primary/affirmative button. |
| `--danger strings` | Choice ids rendered as the destructive button. |
| `--free` | Allow a free-text answer. Default `true`; `--free=false` forces a choice. |
| `-a, --attach strings` | Repeatable path to an image or document (`.md`, `.html`, `.png`, …). |
| `--timeout duration` | Deadline. Default from config (`30m`). `0` waits forever. |
| `--transport strings` | `telegram`, `discord`. Default from config. |
| `--agent string` | Label shown to the human. Default `$HITL_AGENT`, else `agent`. |
| `--default string` | Text to print if the deadline passes, instead of failing. |
| `-o, --format string` | `text` or `json`. Default `text`. |

`-o text` prints **only** the answer text on stdout, so command substitution works. Every log line and diagnostic goes to stderr. `-o json` prints the `hitl.Answer` object.

```sh
ANSWER=$(herdr-hitl ask \
  -t "Force-push to main?" \
  -m "Rebase dropped 2 merge commits. Force-push, or open a PR instead?" \
  -c "push=Force-push" -c "pr=Open a PR" \
  --danger push --primary pr --free=false --timeout 15m)
```

### `notify` — fire-and-forget message, never blocks

Flags: `-t/--title`, `-m/--message`, `--message-file`, `-a/--attach`, `--transport`, `--agent`.

```sh
herdr-hitl notify -t "Migration finished" -m "42 tables, 0 errors, 6m12s." -a report.md
```

### `pending` — list questions currently awaiting an answer

```sh
herdr-hitl pending -o json
```

### `answer` — resolve a question from the terminal instead of the messenger

```sh
herdr-hitl answer a3f19c7b0e42 --choice pr
herdr-hitl answer a3f19c7b0e42 --text "use pgbouncer"
```

### `cancel` — withdraw a question

```sh
herdr-hitl cancel a3f19c7b0e42 --reason "resolved by reading the migration guide"
```

### `serve` — run the daemon in the foreground

Holds the messenger connections and serves CLI clients. Exits `0` quietly when
another daemon already owns the endpoint. Use `daemon start` to spawn a
detached one instead.

```sh
herdr-hitl serve
```

### `daemon` — lifecycle

```sh
herdr-hitl daemon start     # idempotent; spawns a detached serve if none is live
herdr-hitl daemon status -o json   # -o text|json, default text
herdr-hitl daemon restart   # after editing config or rotating a token
herdr-hitl daemon stop
```

### `doctor` — diagnose

```sh
herdr-hitl doctor -o json
```

### `config` — inspect and scaffold

```sh
herdr-hitl config path   # path of config.toml, whether or not it exists
herdr-hitl config show   # effective config after env overrides, tokens redacted
herdr-hitl config init   # write a starter config.toml; never overwrites without --force
```

### `install-cli` — put the binary on PATH

```sh
herdr-hitl install-cli --dir ~/.local/bin
```

### `version`

```sh
herdr-hitl version -o json
```

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Answered. (Or: `notify` delivered, other command succeeded.) |
| `1` | Error — no transport, bad token, daemon unreachable, delivery failed. |
| `2` | Usage error — unknown flag, missing required flag, bad value. |
| `3` | Timeout — the deadline passed with no answer. `--default` converts this to `0`. |
| `4` | Canceled or declined — the human dismissed it, or the daemon was told to cancel. |

## Messenger setup

- [docs/setup-telegram.md](docs/setup-telegram.md)
- [docs/setup-discord.md](docs/setup-discord.md)

## Agent Skill

The skill lives in-repo at [`skills/herdr-hitl/SKILL.md`](skills/herdr-hitl/SKILL.md) — it is the file that teaches an agent when and how to ask. Install it by symlinking it into your agent's skill directory:

```sh
# From an installed plugin — plugin_root is where Herdr keeps the checkout.
ROOT=$(herdr plugin list --plugin huketo.hitl --json | jq -r '.result.plugins[0].plugin_root')
ln -s "$ROOT/skills/herdr-hitl" ~/.claude/skills/herdr-hitl

# From a git checkout
ln -s "$PWD/skills/herdr-hitl" ~/.claude/skills/herdr-hitl
```

Symlink rather than copy so a plugin upgrade updates the skill too. See [ADR-0003](docs/adr/0003-agent-skill-distribution.md) for why the skill ships in-repo instead of being generated.

## Architecture

The daemon exists because the messenger APIs demand a single long-lived connection per bot token — Telegram's update queue is destructive and per-token, Discord rate-limits IDENTIFY at 1000/24h and punishes overruns with a token reset. See [ADR-0001](docs/adr/0001-blocking-cli-over-a-resident-daemon.md).

```mermaid
sequenceDiagram
    participant A as Agent
    participant C as herdr-hitl ask
    participant D as daemon (serve)
    participant M as Telegram / Discord
    participant H as Human

    A->>C: exec, blocks on stdout
    C->>D: connect (unix socket / named pipe), ipc.OpAsk
    Note over C,D: connection held open for the whole ask;<br/>client EOF cancels the question
    D->>M: Poster.Post — message + buttons + attachments
    M->>H: notification on phone
    H->>M: taps a button or types a reply
    M->>D: update / interaction
    D->>M: Poster.Settle — disable controls, show outcome
    D-->>C: ipc.Response with hitl.Answer
    C-->>A: answer on stdout, status as exit code
```

## Development

```sh
make build   # -> bin/herdr-hitl
make test    # go test -race
make lint    # golangci-lint
make cover   # coverage.out + summary
make check   # vet + lint + test
```

Link the working tree into Herdr instead of installing it — `plugin link` skips build commands, so build first:

```sh
make build && herdr plugin link "$PWD"
```

Domain vocabulary is in [CONTEXT.md](CONTEXT.md); decisions are in [docs/adr/](docs/adr/).

## License

MIT. See [LICENSE](LICENSE).
