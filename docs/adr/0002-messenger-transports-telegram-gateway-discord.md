# 0002 — Telegram long polling with HTML, Discord as a gateway bot

## Status

Accepted — 2026-09-01.

## Context

A transport must do four things: deliver a question to a phone, render predefined choices as tappable controls, accept a free-text answer, and edit the delivered message afterwards so the human can see how it was settled (`hitl.Poster.Settle`). Attachments — screenshots, diffs, Markdown plans — must ride along.

Each messenger offers more than one way in, and the choices are not interchangeable.

**Telegram: long polling vs. webhook.** A webhook needs a publicly reachable HTTPS endpoint with a valid certificate. The target user is a developer on a laptop behind NAT; requiring a tunnel to answer a yes/no question is absurd. The two modes are also mutually exclusive per token — `getUpdates` fails with `409 Conflict: can't use getUpdates method while webhook is active` — so supporting both means a config flag that silently breaks the other mode.

**Telegram: MarkdownV2 vs. HTML.** Question bodies are written by an agent and contain code, paths, and diff fragments. MarkdownV2 requires escaping eighteen characters — `_ * [ ] ( ) ~ ` > # + - = | { } . !` — *including inside* code spans for the backslash and backtick, and an unescaped one is not degraded rendering, it is `400 Bad Request: can't parse entities` and a question that never arrives. Every path with an underscore, every bullet list, every sentence ending in a period is a landmine.

**Discord: incoming webhook vs. gateway bot.** An incoming webhook is a single URL, no token management, no rate-limit ceremony. It cannot send interactive components — buttons on a webhook message require an application-owned message — and it has no receive side at all, so there is nothing to deliver a button click or a reply to. A webhook can post a question but can never learn the answer.

## Decision

**Telegram: long polling via `github.com/go-telegram/bot`, `parse_mode: HTML`.**

The daemon runs one long poller for the process lifetime (ADR-0001 covers why exactly one). No webhook mode, no config flag for it. `doctor` checks for a stale webhook on the token and tells the user to `deleteWebhook`, because that is the one way a user can break long polling from outside.

Choices render as an inline keyboard; each button's `callback_data` carries the request id and choice id. Free text arrives as an ordinary message in the chat, matched to the pending question by reply-to or by "the only open question in this chat". `Settle` edits the original message: keyboard removed, outcome appended.

Message bodies are sent with `parse_mode: HTML` and every interpolated value escaped for exactly three characters — `&`, `<`, `>`. That is the whole escaping surface, it is trivially correct, and it is what `html/template`-shaped code already does. Agent-authored Markdown in the body is converted to the small HTML subset Telegram supports (`<b> <i> <code> <pre> <a>`); anything outside the subset degrades to plain text rather than failing the send. The library choice follows: `go-telegram/bot` is context-first, has no global state, exposes the poller as a plain loop we control, and takes no dependencies beyond the standard library.

**Discord: a gateway bot via `github.com/bwmarrin/discordgo`.**

The daemon holds one gateway session. Choices are message components (buttons, with `ButtonStyle` mapped from `hitl.Choice.Style` — `primary` to Primary, `danger` to Danger, empty to Secondary), and clicks arrive as `InteractionCreate` events on that same session. `Settle` edits the message with the components disabled and the outcome shown.

Free text arrives as a `MessageCreate`. In a DM channel the content is present unconditionally; in a guild channel it requires the privileged Message Content intent, which the daemon requests only when `discord.message_content_intent` is true. The default is DM delivery with the intent off — the smaller blast radius.

The Interactions Endpoint URL must stay blank on the application. Setting it moves interaction delivery to HTTP permanently and the gateway stops receiving clicks; `doctor` cannot detect this from the bot side, so it is documented prominently in `docs/setup-discord.md`.

`discordgo` is the choice because it is the only mature Go library that covers both the gateway and the REST/interaction surface, and it handles the reconnect/resume dance — which matters a great deal given the IDENTIFY budget in ADR-0001.

## Consequences

- No inbound network exposure. Both transports are outbound-only, so the plugin works behind NAT, on a train, on a corporate VPN.
- Telegram message formatting cannot fail on punctuation. The `400 can't parse entities` class of bug is gone.
- Both transports support the full feature set the domain needs: buttons, free text, attachments, and in-place settle.
- Cost: `parse_mode: HTML` means agent-written Markdown is *translated*, not passed through, so exotic Markdown renders plainly. Acceptable — bodies are short prose plus code, and richer content belongs in an attachment.
- Cost: the Discord free-text path has two behaviours (DM vs. guild + intent) that a user can misconfigure into silence. Mitigated by defaulting to DM and by a `doctor` check that warns when `channel_id` is set, free text is possible, and the intent is off.
- Cost: `discordgo` is a large dependency with its own goroutine model. Contained behind `transport.Transport`, so replacing it touches one package.
- Adding a third transport (Slack, Matrix, ntfy) means implementing `transport.Transport` and nothing else. Neither decision here leaks into the domain.

## Alternatives considered

**Telegram webhook mode.** Rejected: requires public HTTPS ingress, and conflicts with long polling on the same token. A user who wants it can put a reverse proxy in front of nothing — there is no server to proxy to.

**Telegram MarkdownV2.** Rejected: eighteen escape characters, and a mistake is a hard send failure rather than ugly output. HTML has three.

**Telegram `parse_mode` unset (plain text).** Rejected: code blocks are the most common thing an agent needs to show, and monospace rendering is what makes a diff readable on a phone.

**Discord incoming webhook.** Rejected: cannot send interactive components and cannot receive anything. It is a one-way pipe, and this feature is fundamentally two-way.

**Discord HTTP interactions (Interactions Endpoint URL).** Rejected for the same reason as the Telegram webhook: public ingress, plus Ed25519 signature verification, plus a 3-second ACK deadline — all to avoid a gateway connection we need anyway for message events.

**Discord slash commands as the answer channel.** Rejected: buttons are one tap on a phone, a slash command is typing. Slash commands would also require registering application commands per guild.
