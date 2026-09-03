---
name: herdr-hitl
description: Ask the human a question and block until they answer, either in the agent's interface or over Telegram or Discord. Use when you need a decision, approval, credential, missing requirement, or judgement call you cannot make alone — and especially when the human is away from the terminal and would otherwise never see the prompt. Triggers on needing permission for a destructive or irreversible action, choosing between designs with real tradeoffs, a secret or value only the human has, an ambiguous or contradictory requirement, or announcing the end of a long unattended run.
---

# Asking a human

You may be running unattended. When you hit something you must not decide alone, do not guess and do not stop silently in a terminal nobody is watching. Ask, block, and continue with the answer.

## When to ask

Ask when the decision is genuinely not yours:

- **Irreversible or destructive.** Force-push, drop a table, delete a branch, rewrite history, `rm -rf` outside the workspace, publish, deploy.
- **A real tradeoff with no repo-visible answer.** Two designs that both work and differ in cost the repo does not encode.
- **Something only the human has.** A credential, an API key, an account id, a production hostname.
- **An ambiguous or contradictory requirement.** The issue says one thing, the code says another, and picking wrong wastes the whole run.
- **Scope you were not given.** The fix requires touching a system outside the assignment.

## When NOT to ask

Every unnecessary question costs the human an interruption and costs you minutes. Do not ask when:

- **The repo, tools, or context can answer it.** Read the code, run the test, check the config, grep the history first. "Which test runner?" is in `package.json`.
- **One composite question would do.** Never fire three questions in a row. Bundle them: state the decision once, list the options, ask once.
- **The human already answered it.** Anything in the conversation, the issue, an ADR, or `CONTEXT.md` is answered. Do not re-ask for confirmation of an instruction you were already given.
- **You are asking for reassurance.** "Shall I continue?" is not a decision. Continue.
- **It is a style or naming detail.** Follow the surrounding code and move on.
- **It is cheap and reversible.** Just do it; report it afterwards.

If you cannot state a concrete consequence for each option, you do not have a question yet. Investigate more.

## Resolve the binary

1. `herdr-hitl` on `PATH` — use it.
2. Else `"$HERDR_PLUGIN_ROOT/bin/herdr-hitl"` when `HERDR_PLUGIN_ROOT` is set.
3. Else run `herdr plugin action invoke huketo.hitl.install-cli`, then retry step 1.

```sh
HITL=$(command -v herdr-hitl || echo "${HERDR_PLUGIN_ROOT:-}/bin/herdr-hitl")
[ -x "$HITL" ] || { herdr plugin action invoke huketo.hitl.install-cli; HITL=$(command -v herdr-hitl); }
```
Before every question, resolve the channel with `herdr-hitl channel`:

1. `messenger` — deliver it with `herdr-hitl ask`.
2. `terminal` — the human is at your own interface. Ask there and do not call `herdr-hitl ask`.

The human toggles their presence with `herdr-hitl away` and `herdr-hitl here`. These are human commands; never run them. The `ask` examples below apply only to the `messenger` branch.

## Command surface

```
herdr-hitl ask [flags]
  -t, --title string        one-line summary
  -m, --message string      question body, Markdown; "-" reads stdin
      --message-file PATH   read the body from a file
  -c, --choice strings      repeatable, "id=Label" or bare "Label"
      --primary strings     choice ids rendered as the primary/affirmative button
      --danger strings      choice ids rendered as the destructive button
      --free                allow a free-text answer (default true; --free=false forces a choice)
  -a, --attach strings      repeatable path to an image or document
      --timeout duration    default 30m; 0 waits forever
      --transport strings   telegram | discord (default: config)
      --agent string        label shown to the human (default $HITL_AGENT, else "agent")
      --default string      text to print if the deadline passes, instead of failing
      --channel string      messenger | terminal | auto (default: config)
  -o, --format string       text | json (default text)
herdr-hitl notify [-t|-m|--message-file|-a|--transport|--agent|--channel]
herdr-hitl channel [-o text|json]
herdr-hitl pending [-o text|json]
herdr-hitl answer <request-id> [--choice ID] [--text TEXT]
herdr-hitl cancel <request-id> [--reason TEXT]
herdr-hitl doctor [-o text|json]
```

`ask -o text` prints **only** the answer on stdout; logs go to stderr. So `ANSWER=$(herdr-hitl ask …)` is the idiomatic call. With `-c`, the answer text is the chosen label; use `-o json` and read `.choice_id` when you need to branch on a stable id.

## Examples

**Yes/no approval before something irreversible.**

```sh
ANSWER=$(herdr-hitl ask -o json \
  -t "Force-push to main?" \
  -m "The rebase dropped 2 merge commits (a1b2c3d, e4f5g6h). Force-pushing rewrites main for everyone who pulled today." \
  -c "push=Force-push" -c "abort=Abort and leave main alone" \
  --danger push --primary abort --free=false --timeout 20m)
case "$(printf '%s' "$ANSWER" | jq -r .choice_id)" in
  push)  git push --force-with-lease ;;
  abort) echo "leaving main alone" ;;
esac
```

**Multi-choice design decision with consequences spelled out.**

```sh
herdr-hitl ask -o json --free=false --timeout 1h \
  -t "Session storage for the new auth flow" \
  -m 'Three options, all implementable today:

- **Redis** — fastest, but adds a service to deploy and to the dev setup.
- **Postgres table** — no new infra, ~4ms slower per request, needs a cleanup job.
- **Signed cookies** — no storage at all, but sessions cannot be revoked server-side.

No ADR covers this. Revocation is not in the issue requirements.' \
  -c "redis=Redis" -c "pg=Postgres table" -c "cookie=Signed cookies" \
  --primary pg --danger cookie
```

**Free-text question asking for a value.**

```sh
HOST=$(herdr-hitl ask --free \
  -t "Staging database host" \
  -m "Migration is ready. I need the staging Postgres host — it is not in the repo or the env. Reply with the hostname only." \
  --timeout 30m) || exit $?
```

**Question with a screenshot attached.**

```sh
herdr-hitl ask -o json --timeout 15m \
  -t "Is this layout right?" \
  -m "The sidebar collapses below 900px instead of 768px as the issue asked. Screenshot at 880px attached. Keep 900px or change to 768px?" \
  -a /tmp/sidebar-880.png \
  -c "keep=Keep 900px" -c "fix=Change to 768px" --primary fix --free=false
```

**Question with a Markdown plan attached — attach, do not paste.**

```sh
herdr-hitl ask -o json --timeout 2h \
  -t "Approve the 9-step refactor plan?" \
  -m "Full plan attached (9 commits, touches 34 files, no behaviour change intended). Steps 6 and 7 change the public API of \`internal/store\`." \
  -a /tmp/refactor-plan.md \
  -c "go=Approved, start" -c "revise=Revise it" -c "drop=Do not do this" \
  --primary go --danger drop --free
```

**Fire-and-forget at the end of a long run.** Never blocks, no exit-code branching.

```sh
herdr-hitl notify -t "Migration finished" \
  -m "42 tables migrated, 0 errors, 6m12s. Report attached." -a /tmp/report.md
```

## Exit codes

| Code | Meaning | What you do |
| --- | --- | --- |
| `0` | Answered | Use the answer on stdout. |
| `1` | Error | Delivery or config failure. Run `doctor`. Do not retry blindly. |
| `2` | Usage error | Your command was wrong. Fix the flags. |
| `3` | Timeout | Nobody answered. Take the safe path or stop; do not proceed as if approved. |
| `4` | Canceled or declined | The human said no. Stop that line of work. |
| `5` | Terminal channel | Nothing was sent. Ask in your own interface; do not retry. |

```sh
set +e
ANSWER=$(herdr-hitl ask -t "Deploy to prod?" -m "…" -c "go=Deploy" --free=false --timeout 30m)
CODE=$?
set -e
case $CODE in
  0) echo "proceeding: $ANSWER" ;;
  3) echo "no answer in 30m — skipping the deploy and reporting instead" ;;
  4) echo "declined — stopping" ;;
  5) echo "nothing sent — ask in your own interface; do not retry" ;;
  *) echo "hitl failed ($CODE)" >&2; exit "$CODE" ;;
esac
```

Never treat `3`, `4`, or `5` as approval. Exit `5` is not a failure to retry; it means ask in your own interface. If a timeout has a safe default, encode it with `--default` so the command exits `0` and prints that value.

## Writing a good question

The human may be on a phone, in a queue, with ten seconds of attention.

- **Title: the decision, not the topic.** "Force-push to main?" beats "Question about git".
- **State the decision in the first sentence.** No preamble, no recap of what you have been doing.
- **Give each option its consequence.** An option without a cost is not a choice, it is a quiz.
- **Attach the evidence, do not paste it.** Diff, plan, log, screenshot — `-a FILE`. Pasting 300 lines into the body makes it unreadable on a phone. Up to 10 attachments, 10 MiB each.
- **Keep the body under ~1500 characters.** If it does not fit, the excess belongs in an attachment.
- **Set `--timeout` deliberately.** Match it to how long you can afford to wait and to how likely the human is nearby: 15–30m for something blocking the run, 1–2h for a design decision, `0` only when waiting forever is genuinely correct. Never leave it to the default without thinking.
- **Use `--danger` for the destructive option and `--primary` for the safe one.** The colours are the only cue the human gets before tapping.
- **Use `--free=false` when a free-text answer is not actionable.** Otherwise leave free text on so the human can say something you did not anticipate.
- **Give `--agent` a useful label** so the human knows which of your runs is asking.

## Troubleshooting

- `herdr-hitl doctor` first — it checks config, credentials, daemon reachability, and each transport, and it never prints tokens.
- Exit `1` with "daemon unavailable": `herdr-hitl daemon start`, then retry once.
- Question delivered but no answer ever arrives: `herdr-hitl pending` to confirm it is still open. The human may not be allowed to answer (`allowed_user_ids`), or the messenger is misconfigured — see `docs/setup-telegram.md` / `docs/setup-discord.md`.
- You were killed mid-ask: nothing to clean up. The daemon sees your connection close and withdraws the question automatically.
- You changed your mind: `herdr-hitl cancel <request-id> --reason "…"`.
