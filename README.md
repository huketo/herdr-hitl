# herdr-hitl

<p align="center">
  <img src="assets/herdr-hitl-bot-profile.png" alt="herdr-hitl bot profile" width="220">
  <br>
  <em>When an agent needs a human, herdr-hitl brings the question to you.</em>
</p>

*English · [한국어](README.ko.md)*

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
$EDITOR "$(herdr-hitl config path)"
herdr-hitl doctor
herdr-hitl ask -t "Smoke test" -m "Reply with anything." --timeout 2m
```

### Where questions are delivered

Both transports deliver by direct message. Their preconditions differ, and the difference is not obvious:

| | Telegram | Discord |
| --- | --- | --- |
| Day-to-day surface | DM with the bot | DM with the bot |
| Needs a group / server? | **No** | **Yes — the bot must share a server with you** |
| One-time precondition | You message the bot first (`/start`) | You invite the bot to a server you are in |
| Error when skipped | `403 bot can't initiate conversation with a user` | `50278 no mutual guilds` |

Discord's shared server is plumbing, not a destination: no question is ever posted there and you never open it. An empty server containing only you and the bot is enough. Leave that server, or remove the bot from it, and DMs break again.

Point a transport at a group or channel instead when a team should see the question — and then set `allowed_user_ids`, or anyone in that space can decide on your behalf. One target is a trap: a **Telegram channel** is broadcast-only, so it takes buttons and can never take a typed answer. `herdr-hitl doctor` says so when it detects one.

### Route questions by presence

A human at the keyboard should not be paged for a decision they can answer in the pane they are looking at. No focus, idle-time, or attached-client probe can establish that reliably, so routing uses declared state for each question.

The recommended interactive setup is:

```toml
channel = "auto"
```

With `auto`, questions stay in the terminal while you are present and go to the messenger while the Away marker is set:

```sh
herdr-hitl away
herdr-hitl here
```

`herdr-hitl away --for 2h` sets an expiry; without `--for`, the marker remains until you clear it. The marker is `<state dir>/away`, contains `forever` or an RFC 3339 expiry, and is created with mode `0600`. A malformed marker counts as away; an expired marker does not.

An unattended launcher, such as a scheduler or a detached agent run, should declare that nobody is watching:

```sh
export HITL_CHANNEL=messenger
```

Each `ask` or `notify` resolves its policy in this order: `--channel messenger|terminal|auto`, `HITL_CHANNEL`, `channel` in `config.toml`, then the default `messenger`. `HITL_CHANNEL` and `channel` accept only those three values; an invalid value is a configuration error at load time. If the result is `terminal`, nothing contacts the daemon or messenger; the command explains that the agent must ask in its own interface on stderr and exits `5`. The agent must not treat that status as approval or retry the delivery. Use `--channel messenger` to override one call.

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

These locations do not change when Herdr launches the binary. Herdr injects `HERDR_PLUGIN_CONFIG_DIR` and `HERDR_PLUGIN_STATE_DIR` for plugin actions and startup hooks, but not for the pane an agent asks from; honouring them would give those two callers different configs and — because the socket derives from the state directory — two daemons on one bot token. `HITL_CONFIG_DIR` and `HITL_STATE_DIR` are the only overrides. Run `herdr-hitl config path` to see the resolved location.

### `config.toml`

```toml
# Route questions through messenger (default), terminal, or the Away marker (auto).
channel = "auto"
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
| `HITL_CHANNEL` | `channel` | — |
| `HITL_TIMEOUT` | `timeout` | — |
| `HITL_LOG_LEVEL` | `daemon.log_level` | — |
| `HITL_IDLE_SHUTDOWN` | `daemon.idle_shutdown` | — |
| `HITL_HERDR_NOTIFICATIONS` | `herdr.notifications` | — |
| `HITL_HERDR_PANE_TOKENS` | `herdr.pane_tokens` | — |
| `HITL_CONFIG_DIR` | config directory | — |
| `HITL_STATE_DIR` | state directory | — |
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
| `--channel string` | `messenger`, `terminal`, or `auto`. Overrides routing for this call. |
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

Flags: `-t/--title`, `-m/--message`, `--message-file`, `-a/--attach`, `--transport`, `--channel`, `--agent`.

```sh
herdr-hitl notify -t "Migration finished" -m "42 tables, 0 errors, 6m12s." -a report.md
```

### `channel` — print the resolved destination

```sh
herdr-hitl channel
herdr-hitl channel -o json
```

Text output is exactly `messenger` or `terminal`. JSON output contains `channel`, `policy`, `reason`, and, when set, `away_until`. The reason is `flag`, `config`, `away marker`, `no away marker`, `away marker expired`, or `default`.

### `away` — declare that nobody is watching the terminal

```sh
herdr-hitl away
herdr-hitl away --for 2h
```

Without `--for`, the marker remains until cleared. The command prints what it wrote and the resolved channel, so a marker that does not affect `channel = "messenger"` is visible immediately.

### `here` — declare that a human is watching the terminal

```sh
herdr-hitl here
```

Clears the Away marker and prints the resolved channel.

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

The `channel` check is `OK` when the resolved route delivers and `WARN` when it does not. It includes the reason and a hint to run `herdr-hitl away`.

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
| `5` | Terminal channel — nothing was sent. Ask in the current interface; this is never approval. |

## Messenger setup

- [docs/setup-telegram.md](docs/setup-telegram.md)
- [docs/setup-discord.md](docs/setup-discord.md)

## Agent Skill

The skill lives in-repo at [`skills/herdr-hitl/SKILL.md`](skills/herdr-hitl/SKILL.md) — it is the file that teaches an agent *when* to ask, not just how. An agent that never loads it never asks.

### With `npx skills` (recommended)

[`skills`](https://github.com/vercel-labs/skills) is a skill installer that works across Claude Code, Cursor, Codex, Copilot, opencode, and 70-odd other harnesses.

```sh
npx skills add huketo/herdr-hitl
```

That clones the repo, finds `skills/herdr-hitl/SKILL.md` by the `skills/` convention — no manifest needed — and links it into your agent's skill directory. Useful variants:

```sh
npx skills add huketo/herdr-hitl -g                                   # install for every project
npx skills add huketo/herdr-hitl --skill herdr-hitl -a claude-code -y # non-interactive
npx skills add huketo/herdr-hitl#v0.1.0                               # pin to a tag
npx skills add ./                                                     # from a local checkout
npx skills list                                                       # what is installed
npx skills update herdr-hitl                                          # pull a newer version
```

It installs a canonical copy at `.agents/skills/herdr-hitl` (or `~/.agents/skills/…` with `-g`) and symlinks each selected agent's directory at it, so one update reaches every harness.

> The package is `skills`, plural. `npx skill` — singular — is an unrelated package that can only install from one hardcoded Vercel repository, and it will reject `huketo/herdr-hitl`.

### By hand

No Node, or you want the skill to track a Herdr-managed checkout:

```sh
# From an installed plugin — plugin_root is where Herdr keeps the checkout.
ROOT=$(herdr plugin list --plugin huketo.hitl --json | jq -r '.result.plugins[0].plugin_root')
ln -s "$ROOT/skills/herdr-hitl" ~/.claude/skills/herdr-hitl

# From a git checkout
ln -s "$PWD/skills/herdr-hitl" ~/.claude/skills/herdr-hitl
```

Symlink rather than copy so a plugin upgrade updates the skill too.

The skill is English-only on purpose: the CLI flags and exit codes are English, and two skills with overlapping trigger descriptions would both fire and drift apart. See [ADR-0003](docs/adr/0003-agent-skill-distribution.md) for why it ships in-repo instead of being generated.

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
    Note over C,D: connection held open for the whole ask —<br/>client EOF cancels the question
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
