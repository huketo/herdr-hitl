<!--
The PR title becomes the commit subject on main, because merges are squashed.
It MUST be a valid conventional commit:

  <type>(<scope>): <subject>

types:  feat fix perf refactor revert docs test build ci style chore
scopes: cli daemon paths herdrctl channel broker telegram discord config ipc skill plugin docs ci deps

Examples:
  feat(telegram): add inline keyboard for predefined choices
  fix(daemon): release the socket lock when a stale pid is detected
  docs(skill): document the ask exit codes
-->

## What

<!-- One or two sentences. What does this change do? -->

## Why

<!-- The problem this solves. Link the issue: Closes #123 -->

## How

<!-- Anything a reviewer needs to follow the diff: design choices, trade-offs,
     alternatives you rejected. Delete if the diff speaks for itself. -->

## Verification

<!-- What you actually ran. Replace the placeholders with real output/results. -->

- [ ] `make check` passes (`vet` + `lint` + race tests)
- [ ] Exercised the change end to end (describe how):

## Notes for reviewers

- [ ] Touches the messenger transports (needs a live bot token to verify)
- [ ] Changes the CLI surface, flags, or exit codes (docs + skill updated)
- [ ] Changes the daemon IPC protocol (client/daemon compatibility considered)
- [ ] Breaking change (the title uses `!`, e.g. `feat(cli)!: ...`)
