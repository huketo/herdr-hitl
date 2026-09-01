# Contributing to herdr-hitl

`herdr-hitl` is a [Herdr](https://herdr.dev) plugin that lets a coding agent
block on a human decision delivered over Telegram or Discord. It is a Go module
(`github.com/huketo/herdr-hitl`) that builds a single binary, `herdr-hitl`, from
`./cmd/herdr-hitl`.

## Prerequisites

- Go — the version is pinned in `go.mod`; CI uses exactly that toolchain.
- `make`.
- Herdr `0.8.0` or newer, if you want to exercise the plugin integration.
- A Telegram bot token or a Discord bot token, if you want to exercise a
  transport end to end. Tests never touch the network.

Nothing else is required. `make lint` and `make fmt` fetch their tools on demand
through `go run`, so there is no separate install step.

## Build and test

```sh
make build     # -> bin/herdr-hitl
make test      # go test -race -count=1 ./...
make cover     # writes coverage.out and prints total coverage
make fmt       # gofumpt -w .
make vet       # go vet ./...
make lint      # golangci-lint run
make check     # vet + lint + test — run this before opening a PR
make tidy      # go mod tidy
make clean
```

`make check` is the gate. CI runs the same checks plus a `gofmt` diff, a
`go mod tidy` diff, the test suite on Linux, macOS and Windows, and a
cross-compile of every release target.

Two things fail CI that are easy to miss locally:

- **Formatting.** CI fails if `gofmt -l .` prints anything. `make fmt` runs
  gofumpt, which is a strict superset, so a gofumpt-clean tree is gofmt-clean.
- **An untidy module.** CI runs `go mod tidy` and then
  `git diff --exit-code go.mod go.sum`. Run `make tidy` and commit the result.

### Test conventions

- Table-driven, with `t.Parallel()` wherever the test is safe to run in
  parallel.
- No network. Use `httptest` for the Telegram HTTP API and fakes for the
  Discord gateway.
- Test observable behaviour — exit codes, the `hitl.Answer` a broker returns,
  what a transport posts — not internal plumbing.

## Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/). This is not
cosmetic: `release-please` parses the history to decide the next version and to
write `CHANGELOG.md`. A commit that does not parse is a commit that cannot be
released.

```
<type>(<scope>): <subject>

[optional body]

[optional footer]
```

**Types**

| Type       | Meaning                                             | Release effect        |
| ---------- | --------------------------------------------------- | --------------------- |
| `feat`     | New user-visible capability                         | minor bump            |
| `fix`      | Bug fix                                             | patch bump            |
| `perf`     | Performance improvement, no behaviour change         | patch bump            |
| `refactor` | Internal restructuring, no behaviour change          | patch bump            |
| `revert`   | Reverts an earlier commit                            | patch bump            |
| `docs`     | Documentation only                                   | in changelog, no bump |
| `test`     | Tests only                                           | hidden                |
| `build`    | Build system, Makefile, goreleaser                   | hidden                |
| `ci`       | Workflows, dependabot, commitlint                    | hidden                |
| `style`    | Formatting only                                      | hidden                |
| `chore`    | Everything else, including dependency bumps          | hidden                |

Append `!` for a breaking change — `feat(cli)!: rename --free to --allow-text`.
Before `1.0.0` a breaking change is a minor bump (`bump-minor-pre-major`), not a
major one.

**Scopes** — one of:

`cli`, `daemon`, `broker`, `telegram`, `discord`, `config`, `ipc`, `skill`,
`plugin`, `docs`, `ci`, `deps`

The scope is optional, but if you set one it must be from that list.
`commitlint.config.mjs` is the source of truth; keep it and this list in sync.

Examples:

```
feat(telegram): render predefined choices as an inline keyboard
fix(daemon): drop the socket lock when the recorded pid is stale
feat(cli)!: make --format json the default for ask
docs(skill): document the ask exit codes
chore(deps): bump github.com/bwmarrin/discordgo to v0.29.0
```

### The PR title is a commit message

Merges are **squashed**, so the PR title becomes the subject of the single
commit that lands on `main`. It must itself be a valid conventional commit.
A dedicated workflow lints the PR title on every edit, and `commitlint` also
lints the individual commits in the branch — so the commits inside the branch
should be conventional too, even though only the title survives the merge.

If you want to see whether a message passes before pushing:

```sh
npm install --no-save @commitlint/cli@19 @commitlint/config-conventional@19
echo 'feat(broker): expose PendingCount' | npx commitlint --config commitlint.config.mjs
```

## How releases work

Releases are fully automated; nobody tags by hand.

1. Conventional commits land on `main`.
2. `release-please` keeps a **release PR** open, titled
   `chore(main): release X.Y.Z`. It holds the version bump and the generated
   `CHANGELOG.md` entry, and it updates itself as more commits land.
3. Merging that PR is the release decision. `release-please` then pushes the
   `vX.Y.Z` tag and creates the GitHub Release with the changelog as its notes.
4. In the same workflow run, `goreleaser` builds Linux, macOS and Windows
   binaries for `amd64` and `arm64`, packages each one with `README.md`,
   `LICENSE`, `herdr-plugin.toml`, `.env.example` and `skills/`, and appends the
   archives plus `checksums.txt` to that release.

The current version lives in `.release-please-manifest.json`. Do not edit it, or
`CHANGELOG.md`, by hand — `release-please` owns both.

Release binaries embed their provenance through `-ldflags`, into the three
package-level variables in `cmd/herdr-hitl/main.go`: `version`, `commit` and
`date`. `Makefile` and `.goreleaser.yaml` set the same three; if you rename one,
rename it in all three places.

## Developing the plugin locally

Herdr can load the plugin straight from your working copy, so you do not have to
reinstall after every build:

```sh
make build                      # bin/herdr-hitl
herdr plugin link .             # register this checkout as plugin huketo.hitl
herdr plugin list               # confirm huketo.hitl is present
```

Then iterate: edit, `make build`, and re-run. Ask a real question against your
own bot:

```sh
export TELEGRAM_BOT_TOKEN=...   # or put it in the config file
export TELEGRAM_CHAT_ID=...
bin/herdr-hitl doctor           # verifies config and connectivity; never prints tokens
bin/herdr-hitl daemon restart   # pick up a rebuilt binary
bin/herdr-hitl ask -t "Deploy?" -m "Ship **1.2.0** to prod?" -c ship=Ship -c hold=Hold
```

The daemon is resident and owns the messenger connections. After rebuilding, run
`daemon restart` — a stale daemon keeps serving the old binary. Never run a
second poller against the same bot token outside the daemon: Telegram's
`getUpdates` queue is destructive and single-consumer, and Discord penalises
excess gateway IDENTIFYs by resetting the bot token.

When you are done:

```sh
herdr plugin unlink huketo.hitl
```

## Opening a pull request

- Branch from `main`.
- Keep the change focused; one logical change per PR.
- Run `make check`.
- Give the PR a conventional-commit title.
- Fill in the PR template, including what you actually ran to verify the change.
- Update the docs, `README.md` and `skills/herdr-hitl/SKILL.md` in the same PR
  whenever you touch the CLI surface, the flags or the exit codes. The code, the
  docs and the skill must agree.

## Reporting bugs and proposing changes

Use the issue templates. Bug reports should include `herdr-hitl version` and
`herdr-hitl doctor` output. Redact bot tokens and chat IDs from anything you
paste — `doctor` redacts them for you, raw config files do not.
