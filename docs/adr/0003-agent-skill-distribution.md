# 0003 — The Agent Skill ships in-repo, hand-written

## Status

Accepted — 2026-09-01.

## Context

A blocking CLI is useless if the agent never calls it, calls it wrongly, or calls it for things it should have figured out itself. The behaviour we need from the agent is mostly *judgement*: recognise that a decision is not yours to make, compose one composite question instead of five, attach the diff rather than pasting 300 lines, branch correctly on exit code 3 versus 4. None of that is discoverable from `--help`.

Agent harnesses have converged on a file for exactly this — a Markdown document with YAML frontmatter (`name`, `description`) that the harness loads when the description matches what the agent is about to do. Claude Code calls them skills; the shape is portable enough that other harnesses read the same file.

So the question is not *whether* to ship a skill, but where it lives and how it stays true.

Three failure modes to avoid:

1. **Drift.** The skill documents `--choices`, the CLI accepts `--choice`. The agent's command fails, the agent gives up asking, the feature silently stops existing.
2. **Duplication with a different voice.** README, `--help`, and skill all describe the same flags. Three places to edit, and the skill is the one nobody remembers.
3. **A generated skill that reads like generated text.** The valuable half of the skill is the judgement — when *not* to ask, how to phrase a decision, what to attach. A generator can emit a flag table. It cannot emit "never block on a question the user already answered".

## Decision

The skill is a hand-written file committed at `skills/herdr-hitl/SKILL.md`. It is not generated, not templated, and not assembled at build time.

**The CLI surface is the contract.** Cobra's flag definitions in `internal/cli` are the single source of truth. The skill, the README's CLI reference, and `--help` all describe that surface; where they disagree, the code wins and the docs are the bug.

**Drift is guarded by a test, not by generation.** A docs test walks the cobra command tree, extracts every command and flag name, and asserts that `skills/herdr-hitl/SKILL.md` and `README.md` mention no flag that does not exist and omit no command that does. It checks *names*, deliberately not prose: it catches the rename that breaks the agent, and stays out of the way of the judgement content that a generator would flatten. CI runs it with the rest of the suite, so a flag rename fails the build until the skill is updated in the same commit.

**Distribution is a symlink from the plugin root.** `herdr plugin list --plugin huketo.hitl --json` reports `plugin_root`; the user symlinks `$plugin_root/skills/herdr-hitl` into their harness's skill directory. Symlink, not copy, so `herdr plugin install` refreshing the checkout also refreshes the skill.

**One skill, not per-harness variants.** The file uses the common frontmatter subset and plain Markdown with bash examples. A harness that wants something else can wrap it; we do not fork it.

## Consequences

- Version skew is impossible in practice: the skill travels in the same git tree, the same tag, and the same plugin checkout as the binary it documents.
- The skill can hold the part that matters — worked examples, question-quality rules, exit-code branching — in a human voice, because a human wrote it.
- The drift test makes flag renames a two-file change, enforced. That is friction on purpose: renaming an agent-facing flag *is* a breaking change.
- Cost: prose can still go stale in ways a name check misses. A flag that changes meaning without changing name will not be caught. Accepted — the alternative catches nothing at all.
- Cost: the skill duplicates the flag list that also appears in the README. Kept because the two audiences differ (an agent reading a skill has no browser) and because the drift test covers both.
- Cost: installation is a manual symlink rather than automatic. Deliberate — writing into a user's agent config directory without being asked is not a plugin's business. `install-cli` handles the binary, which is the part the agent genuinely cannot work around.

## Alternatives considered

**Generate `SKILL.md` from the cobra tree at build time.** Rejected: produces a flag reference, which is the least valuable half. The judgement content would have to live in a template anyway, so the generator only moves the hand-written text somewhere less readable while adding a build step and a generated file in git.

**Ship the skill as a separate repository or package.** Rejected: reintroduces exactly the version skew this decision exists to prevent, and doubles the release process for one Markdown file.

**Embed the skill in the binary and add `herdr-hitl skill install`.** Tempting, and it makes installation one command. Rejected for now: it makes the skill invisible to anyone browsing the repo or the plugin marketplace, and it writes into the user's agent config without a clear place to put it (every harness differs). Reconsider if harnesses standardise a skill directory.

**Rely on `--help` alone and ship no skill.** Rejected: the agent has to already know the tool exists and already have decided to ask. The skill's `description` frontmatter is the trigger mechanism — without it, nothing fires.

**Put the skill under `.herdr/` or the plugin config directory.** Rejected: `skills/` at the repo root is where a human looks, and the config directory is for user-editable state, not shipped source.
