/**
 * Commit message rules for herdr-hitl.
 *
 * Merges are squashed, so the PR title becomes the commit subject on main and
 * is what release-please parses. The CI workflow therefore lints PR titles with
 * this same config.
 */
export default {
	extends: ["@commitlint/config-conventional"],
	// release-please authors "chore(main): release X.Y.Z"; the "main" scope is
	// deliberately absent from scope-enum, so skip those generated commits.
	// Bot-authored subjects. Both are generated, both are guaranteed to be
	// conventional — release-please builds its own title, and Dependabot's type
	// and scope come from commit-message.prefix in .github/dependabot.yml. What
	// is not guaranteed is length: a single-dependency group title carries an
	// "in the <group> group across 1 directory" suffix that can exceed
	// header-max-length, and Dependabot rewrites the title on every rebase and
	// recreate, so editing it by hand does not survive.
	ignores: [
		(message) => /^chore\(main\): release /.test(message),
		(message) => /^chore\((?:deps|deps-dev|ci)\): bump /.test(message),
	],
	rules: {
		"type-enum": [
			2,
			"always",
			[
				"feat",
				"fix",
				"perf",
				"refactor",
				"revert",
				"docs",
				"test",
				"build",
				"ci",
				"style",
				"chore",
			],
		],
		// Scopes track the real packages in this repo. Keep in sync with
		// CONTRIBUTING.md and internal/.
		"scope-enum": [
			2,
			"always",
			[
				"cli",
				"daemon",
				"paths",
				"herdrctl",
				"channel",
				"broker",
				"telegram",
				"discord",
				"config",
				"ipc",
				"skill",
				"plugin",
				"docs",
				"ci",
				"deps",
			],
		],
		"scope-empty": [0],
		"subject-case": [2, "never", ["upper-case", "pascal-case", "start-case"]],
		"header-max-length": [2, "always", 100],
		// Dependabot pastes upstream release notes and long metadata footers;
		// hard-wrapping machine-written bodies buys nothing.
		"body-max-line-length": [0],
		"footer-max-line-length": [0],
	},
};
