# 0004 — Channel routing by declared presence

## Status

Accepted — 2026-09-03.

## Context

Every Question used to go to a messenger. That is correct when nobody is watching the agent's interface, but wrong when the human is already there: the human gets a phone notification for a decision they could answer in the interface in front of them. Presence changes throughout the day, so a startup-time choice cannot settle every later Question.

The routing decision belongs to one invocation, not to the resident Daemon. The Daemon is shared by every asking process on the machine. It cannot tell whether a particular caller came from an attended pane, a scheduler, or a detached run, and one process's presence must not change another process's route. The asking CLI has the caller's flags, environment, and operator context before it opens the Endpoint.

Machine state does not answer the question we need to ask: "will this human see a request in the agent's own interface?"

- Window focus reports which application receives input at one instant. It does not report whether the human is watching the agent's interface, and it has no reliable meaning for remote or headless clients.
- Idle time reports keyboard and pointer activity. A human reading the agent's output can be idle, while a human active in another application can be absent from the agent's interface.
- An attached client reports that a terminal, multiplexer, or pseudo-terminal connection exists. Persistent clients remain attached while the human walks away, and unattended launchers can allocate clients of their own.

All three are guesses with the dangerous error in the same direction: they can route to a terminal that nobody will answer.

## Decision

Route each Question in the asking CLI before it contacts the Daemon. `ask` resolves a `channel.Decision` and applies it before building the request or opening the Endpoint. `notify` uses the same gate. The Daemon, Broker, and Transports remain responsible only for messenger delivery; they do not infer presence and do not choose a Channel.

**Presence is declared, never probed.** An unattended launcher declares its route with `HITL_CHANNEL`. A human leaving or returning declares presence with the Away marker, through `herdr-hitl away` and `herdr-hitl here`. The marker contains `forever` or an RFC 3339 expiry in the state directory's `away` file.

**Policy resolves in a fixed order.** The first value present wins:

1. `--channel messenger|terminal|auto` on `ask` or `notify`.
2. `HITL_CHANNEL`.
3. `channel` in `config.toml`.
4. No value: `messenger`.

The `messenger` default is deliberate backward compatibility. An existing installation that never configures Channel routing continues to deliver every Question exactly as before.

A `messenger` or `terminal` Policy resolves directly to that Channel. An `auto` Policy consults the Away marker for each invocation: a set and unexpired marker resolves to `messenger`; no marker or an expired marker resolves to `terminal`.

**A malformed marker counts as away.** The failure modes are asymmetric. Mistakenly choosing `terminal` when nobody is there strands the run with no delivered Question. Mistakenly choosing `messenger` when the human is present costs one unwanted notification. The safer failure is therefore `messenger`.

**Terminal routing is a refusal to deliver.** When the resolved Channel is `terminal`, `ask` and `notify` do not contact the Daemon and send nothing. They explain on stderr that the agent must ask in its own interface and exit `5`. `--channel messenger` can override one invocation.

Exit code `5` extends the CLI contract alongside `0` answered, `1` error, `2` usage, `3` timeout, and `4` canceled. It means "nothing was sent; ask in your own interface." It is never an Answer, an approval, a delivery failure, or permission to continue.

## Consequences

- Two concurrent callers can make different routing decisions without changing shared Daemon state.
- A terminal decision fails before daemon startup, IPC, or messenger delivery. It cannot create a Pending Question or a stale messenger message.
- Existing configurations keep messenger delivery because an unset Policy resolves to `messenger`.
- Schedulers and detached launchers must declare their route with `HITL_CHANNEL`; humans must maintain the Away marker when using `auto`. Presence is explicit rather than guessed.
- A stale or malformed Away marker can produce an unwanted notification. This is accepted because it preserves a path to the human instead of stranding the caller.
- Callers must handle exit code `5` separately from errors, timeouts, cancellations, and Answers. Agent instructions must direct the Question to the current interface rather than retrying the CLI.
- The Daemon and IPC ask protocol do not acquire a presence model. Channel routing remains a client concern.

## Alternatives considered

**Resolve the Channel in the Daemon.** Rejected: the Daemon is shared state, while presence belongs to the individual caller. Moving the caller's flags and environment across IPC would add protocol surface only to let the Daemon return a refusal that the CLI can determine before connecting.

**Probe window focus.** Rejected: focus identifies an input target, not whether the human will see and answer the agent's Question. It is also unavailable or misleading for remote and headless use.

**Probe idle time.** Rejected: inactivity includes reading and thinking, while activity can occur somewhere unrelated to the agent. The signal does not establish presence at the relevant interface.

**Probe attached clients.** Rejected: attachment is connection state, not human attention. Long-lived terminal and multiplexer clients survive absence, and unattended processes can have attached pseudo-terminals.

**Keep routing every Question to a messenger.** Rejected: it preserves delivery but sends unnecessary notifications whenever the human is already watching the agent.

**Default to `auto`.** Rejected: an upgrade with no Away marker would silently stop messenger delivery and return exit `5`. The feature must not change existing installations until the operator chooses a new Policy.
