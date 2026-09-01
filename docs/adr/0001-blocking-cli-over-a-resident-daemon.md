# 0001 — A blocking CLI over a resident daemon

## Status

Accepted — 2026-09-01.

## Context

An agent mid-run needs a human decision. The agent's only reliable primitive is "run a command and read its output": every coding agent can do that, none of them agree on anything richer. So the interface the agent touches must be a CLI that blocks, prints the answer, and encodes the outcome in an exit code. This is a product requirement, not a preference — an MCP server was considered and rejected (see Alternatives).

The obvious implementation is one process per question: `ask` connects to Telegram or Discord, posts the message, waits, prints the answer, exits. Two hard platform constraints kill it.

**Telegram.** The Bot API has exactly one update queue per bot token, and reading it is destructive — `getUpdates` deletes the updates it returns. It also refuses concurrent access: a second poller on the same token gets `409 Conflict: terminated by other getUpdates request`, after which the two pollers thrash, each consuming updates the other is waiting for. Two simultaneous `ask` processes therefore do not merely contend, they lose answers outright. Only one process may ever poll a token.

**Discord.** The gateway rate-limits `IDENTIFY` to 1000 per 24 hours per bot token, and the documented penalty for exceeding it is a **bot token reset** — not a retry-after, not a temporary block. The bot goes offline until a human copies a fresh token out of the developer portal. One IDENTIFY per question means an agent asking every couple of minutes bricks the bot inside a day.

Both constraints point the same way: the messenger connections must be owned by exactly one long-lived process, independent of how many questions are in flight.

A third requirement shapes the client/daemon boundary. Agents get killed — Ctrl-C, a supervisor timeout, a crashed harness. A question whose asker is gone must not sit in the messenger forever collecting a stale answer.

## Decision

Two processes, one protocol.

`herdr-hitl serve` is a resident daemon. It owns one connection per configured transport for its entire lifetime, holds the pending-question registry (`hitl.Broker`), and listens on a unix socket (Windows: named pipe) whose path comes from `paths.Socket()`. Startup binds the endpoint through `ipc.Listen`, which returns `ErrAlreadyRunning` when a live daemon already owns it — so a race between two `daemon start` calls resolves to one winner without a separate lock protocol.

`herdr-hitl ask` is a thin client. It dials the socket, sends one `ipc.OpAsk` request, and blocks reading the response. It never speaks to Telegram or Discord. `daemon start` is idempotent: `ipc.Probe` first, and only spawn a detached `serve` if nothing answers. The Herdr `[[startup]]` hook is exactly this one-shot call, matching Herdr's startup-hook contract (initialize and exit, not a supervised daemon).

**Invariant: client EOF cancels the question.** The client holds its connection open for the whole ask rather than fire-and-forget-then-poll. The daemon derives the request context from that connection: when the client disappears — Ctrl-C, kill, harness crash — the read side hits EOF, the daemon-side context is canceled, the broker resolves the request as `StatusCanceled`, and the transport's `Settle` edits the messenger message to say the question was withdrawn. This is the entire withdrawal mechanism. No heartbeat, no lease, no reaper. It falls out of TCP-like socket semantics for free, and it is why `ask` must not background itself or reconnect between post and answer.

The connection also gives the daemon a natural place to be idle: `daemon.idle_shutdown` lets it exit after a quiet period, and the next `ask` transparently respawns it via `EnsureRunning`.

## Consequences

- Telegram is polled by one process, ever. No 409s, no stolen answers.
- Discord IDENTIFYs once per daemon lifetime. Restarting the daemon a hundred times a day is still two orders of magnitude under the limit.
- Concurrent asks are cheap: the daemon multiplexes them over one messenger connection and the broker routes each answer by request id.
- A cancelled agent cleans up after itself with no extra machinery.
- Cost: two processes to reason about, an IPC protocol to version, and a class of failure the single-process design does not have — "daemon not running". Mitigated by making `ask` auto-start it and by `herdr-hitl doctor`.
- Cost: config changes are not live. The daemon reads config at startup, so a token rotation needs `herdr-hitl daemon restart`. Documented rather than solved; live reload would mean reconnecting transports, which reintroduces IDENTIFY pressure.
- The socket is the security boundary. It is created in the user's state directory with owner-only permissions; anyone who can connect can answer any pending question. That is the same trust level as "can run commands as this user", which is already true of any plugin.

## Alternatives considered

**One process per ask.** Rejected: violates both platform constraints above. Not a performance tradeoff — it is incorrect on Telegram and destructive on Discord.

**An MCP server.** Superficially the natural fit for "agent asks a question", and it solves the single-connection problem the same way a daemon does. Rejected because the agent interface must be a CLI: MCP requires the harness to support MCP, to have the server configured, and to expose long-running tool calls without timing out. A CLI works from any agent, any shell, any script, a Makefile, or a human at a prompt. Nothing stops an MCP wrapper being added later on top of the same daemon — the IPC layer is the real API.

**Fire-and-forget CLI plus a polling `wait` subcommand.** Rejected: loses the EOF-cancels invariant, and the agent has to implement a poll loop, which is exactly the complexity we are removing from the agent's side.

**A hosted relay service.** Rejected: the tokens are the user's, the questions contain the user's source code, and a plugin should not require an account.
