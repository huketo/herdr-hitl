package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// versionInfo is the JSON shape of `version -o json`.
type versionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

func newVersionCommand(g *globals) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:          "version",
		Short:        "Print version and build information",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != formatText && format != formatJSON {
				return usagef("--format %q: expected %q or %q", format, formatText, formatJSON)
			}
			info := versionInfo{
				Version: versionOr(g.info.Version),
				Commit:  g.info.Commit,
				Date:    g.info.Date,
				Go:      runtime.Version(),
				OS:      runtime.GOOS,
				Arch:    runtime.GOARCH,
			}
			if format == formatJSON {
				return writeJSON(cmd.OutOrStdout(), info)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "herdr-hitl %s (%s, %s) %s %s/%s\n",
				info.Version, info.Commit, info.Date, info.Go, info.OS, info.Arch)
			return err
		},
	}
	cmd.Flags().StringVarP(&format, "format", "o", formatText, "output format: text | json")
	return cmd
}
