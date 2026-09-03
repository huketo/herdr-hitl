# Domain context

`herdr-hitl` moves one decision from a machine to a human and back. Everything in this codebase is named after some part of that round trip. Use these words; the glossary is the vocabulary of issue titles, test names, log messages, and user-facing text.

Decisions live in [`docs/adr/`](docs/adr/).

## Glossary

### Question / Request

The unit of work. A human-answerable decision with a title, a body, zero or more Choices, an optional free-text affordance, optional Attachments, a Timeout, and an Origin. `hitl.Request` in code; **Question** in prose and in messages to humans. Identified by a `hitl.NewID()` — 12 hex characters, short enough to type into `herdr-hitl answer`.

A Question is created by the CLI, validated once (`Request.Validate`), handed to the Broker, and lives in memory until it is settled. Questions are never persisted: an unanswered Question whose daemon died is gone, and the agent that asked it has already exited with an error.

### Answer

The terminal state of a Question. `hitl.Answer` carries a Status, whichever of Choice or free text was supplied, the Responder, and a timestamp. Every Question produces exactly one Answer — including timeouts and cancellations, which are Answers with a non-`answered` Status rather than an absence of one. `Answer.Err()` maps a non-answered Status back to a sentinel error, which is how the CLI derives its exit code.

### Choice

A predefined option, rendered as a button. `hitl.Choice` is an `ID` (stable, what code branches on), a `Label` (what the human reads), and a `Style` — `""`, `"primary"`, or `"danger"`. Style is a semantic hint, not a colour: each Transport maps it to its own affordance. Max 25 Choices per Question, because that is Discord's component ceiling.

`-c "id=Label"` gives both; `-c "Label"` derives the id by slugifying the label.

### Free text

The other way to answer: the human types instead of tapping. `Request.AllowFreeText`, default on. A Question may offer Choices, free text, or both; it may not offer neither. Free text and Choices are alternatives for the *human*, not for the code — `Answer` carries whichever arrived.

### Attachment

A file delivered alongside the Question so the human can see the evidence without reading a wall of text. `hitl.Attachment`, classified by `NewAttachment` into one of two Kinds: `image` (rendered inline) or `document` (Markdown, HTML, anything else — sent as a file). Capped at 10 attachments and 10 MiB each.

Attachments are the intended way to include a diff, a plan, a log, or a screenshot. Pasting them into the body is the anti-pattern.

### Origin

Who is asking, and from where. `hitl.Origin` — host, user, cwd, agent label, and the Herdr workspace/tab/pane ids when running under Herdr. `Origin.Label()` renders the one line the human sees so they can tell which of several concurrent runs is blocked.

### Channel

The class of destination for a Question. `channel.Channel` is either `messenger`, which delivers through a Transport, or `terminal`, which means the human is at the agent's own interface and the agent must ask there. `channel.Policy` is the setting before presence is considered: `messenger`, `terminal`, or `auto`. `channel.Decision` carries the resolved Channel, the effective Policy, the reason, and any Away marker expiry.

A resolved Decision is never `auto`: that Policy resolves per Question to `messenger` or `terminal`, in the asking CLI process rather than the shared Daemon. A `terminal` Decision never reaches the Daemon or a Transport. It exits `5`, which means "ask in your own interface", never approval. See [ADR-0004](docs/adr/0004-channel-routing-by-declared-presence.md).

### Away marker

The human's declaration that nobody is watching the agent's interface. `channel.Marker` is read from the `away` file in the state directory and records whether it exists, when it expires, and whether its contents were malformed. `herdr-hitl away` writes `forever` or an RFC 3339 expiry; `herdr-hitl here` removes it.

The Away marker is consulted only under the `auto` Policy. Set and unexpired means `messenger`; absent or expired means `terminal`. A malformed marker counts as away, because one unwanted notification is safer than stranding an unattended run.

### Transport

A messenger integration: Telegram or Discord. `transport.Transport` = `hitl.Poster` plus `Start`, `Close`, and `Describe`. A Transport owns a connection and translates between the domain and one messenger's API. It never interprets a Question's meaning.

`Describe()` returns one line for `doctor` and **never** contains the token.

### Poster

The delivery half of a Transport, as the domain sees it. `hitl.Poster` has `Name`, `Post`, and `Settle`. `Post` delivers a Question and returns as soon as it is delivered — it must never block waiting for the human. That separation is what lets one daemon hold many Questions open at once.

### Resolver

The inbound half, as a Transport sees it. `hitl.Resolver` has `Resolve` (push an Answer in) and `Lookup` (find a Question by id). Transports are constructed with a Resolver and know nothing else about the Broker. It is the seam that makes a Transport testable without a Broker and a Broker testable without a network.

### Broker

The registry and the rendezvous point. `hitl.Broker` holds every pending Question, fans `Post` out across registered Posters, blocks the caller in `Submit` until an Answer arrives or the deadline passes, and routes an inbound `Resolve` to the right waiter. It implements `Resolver`. It is the only place that knows a Question is pending, and the only place that decides a Question is over.

First Answer wins: a second `Resolve` for the same Question returns `ErrAlreadyAnswered`.

### Daemon

The resident process, `herdr-hitl serve`. Owns exactly one connection per Transport for its whole lifetime, owns the Broker, and serves the Endpoint. It exists because Telegram allows one poller per bot token and Discord punishes repeated IDENTIFY with a token reset — see [ADR-0001](docs/adr/0001-blocking-cli-over-a-resident-daemon.md). Not optional and not an optimisation.

### Endpoint

The address the CLI and the Daemon meet at: a unix socket path on Unix, a named pipe on Windows. `paths.Socket()` resolves it; `HITL_SOCKET` overrides it. `ipc.Listen` binding it is also the daemon's mutual exclusion — it returns `ErrAlreadyRunning` when a live daemon already holds it.

### Settle

To close a Question out **in the messenger**. `Poster.Settle` edits the delivered message: buttons removed or disabled, outcome shown. Distinct from resolving — resolving unblocks the agent, settling tells the human what happened and stops a second person tapping a dead button. Every terminal Status settles, including Timeout and Cancel.

### Pending

The set of Questions posted and not yet settled. `Broker.Pending()`, surfaced by `herdr-hitl pending` and counted in `daemon status`. A Question is Pending from `Submit` until its Answer, however that Answer arrives.

### Responder

The human who answered, as the Transport identified them. `hitl.Responder` — transport name, messenger user id, username. Recorded on the Answer so an audit trail exists, and checked against `allowed_user_ids` before an Answer is accepted. `Display()` renders it for logs and for the settled message.

### Timeout vs. Cancel

Two different non-answers, deliberately not merged.

**Timeout** — the deadline passed and nobody acted. `StatusTimeout`, exit code `3`. The human's silence is the signal. `--default` converts it to a successful exit with a fallback value, for the cases where "no reply" has a safe meaning.

**Cancel** — someone actively ended the Question. `StatusCanceled`, exit code `4`. Either the human declined, or `herdr-hitl cancel` was run, or — most commonly — the asking client's connection hit EOF because the agent was killed, and the Daemon withdrew the Question on its behalf.

An agent must branch differently on the two: a Timeout may be worth retrying later, a Cancel means stop.

### Agent Skill

The Markdown document at `skills/herdr-hitl/SKILL.md` that teaches an agent *when* to ask, not just how. Hand-written, shipped in-repo, symlinked into the harness's skill directory. Its frontmatter `description` is the trigger — an agent that never loads the skill never asks. See [ADR-0003](docs/adr/0003-agent-skill-distribution.md).

### Plugin manifest

`herdr-plugin.toml` at the repo root: the contract between Herdr and this plugin. Declares the plugin id `huketo.hitl`, the build command, the `[[startup]]` hook that spawns the Daemon, and the `[[actions]]` Herdr exposes in its UI. It is not a config file — user configuration lives in `config.toml` and `.env` under the plugin config directory.

## Words we do not use

- **"Prompt."** Reserved for what an agent sends its model. What we send a human is a **Question**. Using "prompt" for both makes every sentence about this system ambiguous.
- **"Notification."** Reserved for the fire-and-forget path (`herdr-hitl notify`) — something a human reads and need not act on. A Question is not a notification; it blocks.
- **"Session," "ticket," "task."** Overloaded elsewhere in the Herdr ecosystem. A Question is a Question.
- **"Reply."** A messenger-layer word. The domain word is **Answer**.
- **"Channel" for a Discord channel id.** A **Channel** is the routing destination class for a Question. A Discord channel id is a Transport setting.

## The ask lifecycle

1. An agent runs `herdr-hitl ask -t … -m … -c …`. The CLI parses flags and resolves a `channel.Decision`: `--channel`, then `HITL_CHANNEL`, then `channel` in `config.toml`, then the `messenger` default; an `auto` Policy consults the Away marker. A `terminal` Decision sends nothing, prints an instruction to ask in the agent's own interface, and exits `5`. Only a `messenger` Decision continues: the CLI resolves attachment paths into Attachments, fills in Origin from the environment, and builds an `ipc.AskParams`.
2. The CLI ensures a Daemon is live: `ipc.Probe` on the Endpoint, and if nothing answers, spawn a detached `serve` and wait for the probe to succeed.
3. The CLI dials the Endpoint and sends `ipc.OpAsk`. **It holds the connection open for the entire ask.** This is load-bearing: the Daemon derives the Question's context from this connection, so a dead client cancels its own Question.
4. The Daemon builds a `hitl.Request`, validates it, and calls `Broker.Submit`. The Broker registers the Question as Pending and calls `Post` on each selected Poster.
5. Each Transport renders the Question its own way — Telegram an HTML message with an inline keyboard, Discord a message with components — uploads the Attachments, and returns. Nobody blocks on the human.
6. The human taps a button or types a reply. The Transport receives the interaction, checks the Responder against `allowed_user_ids`, builds an Answer, and calls `Resolver.Resolve`.
7. The Broker accepts the first Answer, marks the Question no longer Pending, and calls `Settle` on the Posters so the messages stop being actionable.
8. `Submit` returns the Answer. The Daemon writes an `ipc.Response`; the CLI prints the answer text (or the JSON) on stdout and exits with the Status's code.
9. If the deadline passes first, step 6 never happens: the Broker produces a Timeout Answer, settles, and the CLI exits `3` — or prints `--default` and exits `0`. If the client vanishes first, the Broker produces a Cancel Answer, settles, and there is nobody left to tell.
