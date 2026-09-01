package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// docFlag matches a long flag as it appears in prose or a code block. It stops
// at "=" so that "--free=false" is checked as "--free".
var docFlag = regexp.MustCompile(`--([a-z][a-z0-9-]*)`)

// foreignCommand marks a documentation line as belonging to another program
// (`herdr plugin list --json`, `git push --force-with-lease`). Its flags are
// not ours to validate.
var foreignCommand = regexp.MustCompile("(?:^|[\\s`(|$])(?:herdr|git|gh|jq|curl|docker|npm|go|make|brew)\\s")

// docsAllowedFlags are documented flags with no counterpart in the tree:
// cobra adds --help itself, only at execute time.
var docsAllowedFlags = map[string]struct{}{
	"help": {},
}

// TestDocsNameOnlyRealFlags is the flag-drift guard recorded in
// docs/adr/0003-agent-skill-distribution.md: prose that promises a flag the
// binary does not have is worse than no prose at all, because an agent will
// run it.
func TestDocsNameOnlyRealFlags(t *testing.T) {
	t.Parallel()

	known := knownFlags(NewRootCommand(BuildInfo{}))
	for _, doc := range []string{"README.md", filepath.Join("skills", "herdr-hitl", "SKILL.md")} {
		t.Run(doc, func(t *testing.T) {
			t.Parallel()

			text, ok := readDoc(t, doc)
			if !ok {
				return
			}
			for i, line := range strings.Split(text, "\n") {
				if foreignCommand.MatchString(line) {
					continue
				}
				for _, m := range docFlag.FindAllStringSubmatch(line, -1) {
					name := m[1]
					if _, allowed := docsAllowedFlags[name]; allowed {
						continue
					}
					if _, exists := known[name]; !exists {
						t.Errorf("%s:%d documents --%s, which no command defines (flags: %s)",
							doc, i+1, name, strings.Join(sortedKeys(known), ", "))
					}
				}
			}
		})
	}
}

// TestReadmeCoversEveryCommand keeps the reference section honest. SKILL.md is
// deliberately narrower — it documents only the agent-facing subset — so the
// coverage half of the guard applies to the README alone.
func TestReadmeCoversEveryCommand(t *testing.T) {
	t.Parallel()

	text, ok := readDoc(t, "README.md")
	if !ok {
		return
	}
	root := NewRootCommand(BuildInfo{})
	for _, path := range commandPaths(root) {
		if !strings.Contains(text, path) {
			t.Errorf("README.md never mentions `%s`", path)
		}
	}
}

// readDoc loads a repository-root document. A doc that does not exist yet is
// skipped rather than failed: the guard protects against drift, it does not
// mandate a file layout.
func readDoc(t *testing.T, rel string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Skipf("%s is not present: %v", rel, err)
		return "", false
	}
	return string(data), true
}

// knownFlags collects every long flag defined anywhere in the tree.
func knownFlags(root *cobra.Command) map[string]struct{} {
	flags := make(map[string]struct{})
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		cmd.InitDefaultHelpFlag()
		cmd.Flags().VisitAll(func(f *pflag.Flag) { flags[f.Name] = struct{}{} })
		cmd.PersistentFlags().VisitAll(func(f *pflag.Flag) { flags[f.Name] = struct{}{} })
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(root)
	return flags
}

// commandPaths lists every runnable command as it is written in the docs,
// e.g. "herdr-hitl daemon status".
func commandPaths(root *cobra.Command) []string {
	var out []string
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, sub := range cmd.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			out = append(out, sub.CommandPath())
			walk(sub)
		}
	}
	walk(root)
	return out
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
