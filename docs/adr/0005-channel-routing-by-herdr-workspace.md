# 0005 — Channel routing by Herdr workspace

## Status

Accepted — 2026-10-16.

## Context

ADR 0004 routes a Question by declared presence: one `channel` for the installation, moved by the Away marker. Presence is not the only thing that decides where a Question belongs. Some workspaces run agent sessions that another agent drives and watches: a coordinator reads the session's pane, answers what it can, and puts the rest to the right people in the team's own discussion, where the work is tracked. A Question from such a session belongs in its pane, where the coordinator sees it, even while the operator is away and every other workspace's Questions go to the messenger.

Herdr names the unit those projects are organised by: the workspace. Every agent pane carries `HERDR_WORKSPACE_ID`. Its label is what a human chose and recognises; the id is an opaque handle that changes when a workspace is recreated. Only plugin processes receive the label directly, through `HERDR_PLUGIN_CONTEXT_JSON`; an agent pane does not.

## Decision

**A workspace route sets the Channel Policy for named workspaces.** `config.toml` takes `[[workspace]]` routes, each a list of `labels` and a `channel` that is `messenger`, `terminal`, or `auto`. The asking CLI resolves its workspace's label and, when a route names it, uses the route's Policy in place of `channel` from the config file. Everything after that is ADR 0004 unchanged: `auto` consults the Away marker, `terminal` exits `5`, and active AFK overrides the result.

**A route ranks below `HITL_CHANNEL`.** The resolution order is `--channel`, `HITL_CHANNEL`, the workspace route, `channel` in the config file, then `messenger`. An unattended launcher exports `HITL_CHANNEL=messenger` to say that nobody watches any pane, including a routed one, so it must win. The config loader records whether `HITL_CHANNEL` supplied the value, since the two sources otherwise merge into one field.

**The label comes from Herdr.** The CLI reads `workspace_label` from the plugin context when present, and otherwise asks `herdr workspace get <HERDR_WORKSPACE_ID>`. A failed lookup means no route, which leaves the installation's own policy in force. Labels match exactly; a pattern language would make a route's reach harder to read than a list.

**Ambiguity is a configuration error.** A label in two routes, a route without labels, and a route without a valid `channel` are rejected at load time, so the route a reader edits is the route that applies.

**The Decision says it was a route.** Its reason is `workspace route`, so `herdr-hitl channel -o json`, which agent harnesses read to gate their own ask tools, and the exit-`5` message explain the outcome instead of advising `herdr-hitl away`, which does not move a fixed route.

## Consequences

- A project whose sessions are driven by a coordinating agent keeps their Questions in the pane for that agent, while the operator's other projects keep messenger delivery.
- Adding or removing a label switches a workspace on the next invocation; no daemon restart is involved, because the Daemon never sees the route.
- `ask`, `notify`, `channel`, and `doctor` from an agent pane run one extra `herdr workspace get`, bounded by the existing five-second Herdr call timeout.
- Renaming a workspace changes its route. That is intended: the label is the human-chosen name the route was written against.
- A route chooses the Channel only. Which messenger a delivered Question uses is still `transports`.
