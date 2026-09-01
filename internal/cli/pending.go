package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/huketo/herdr-hitl/internal/hitl"
	"github.com/huketo/herdr-hitl/internal/ipc"
)

func newPendingCommand(g *globals) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:          "pending",
		Short:        "List questions waiting for a human",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != formatText && format != formatJSON {
				return usagef("--format %q: expected %q or %q", format, formatText, formatJSON)
			}
			ctx, stop := signalContext(cmd.Context())
			defer stop()

			client, _, err := connect(ctx, g, cmd.ErrOrStderr(), false)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()

			resp, err := client.Do(ctx, &ipc.Request{Op: ipc.OpPending})
			if err != nil {
				return failf("pending: %w", err)
			}
			if format == formatJSON {
				pending := resp.Pending
				if pending == nil {
					pending = []*hitl.Request{}
				}
				return writeJSON(cmd.OutOrStdout(), pending)
			}
			return writePendingTable(cmd.OutOrStdout(), resp.Pending)
		},
	}
	cmd.Flags().StringVarP(&format, "format", "o", formatText, "output format: text | json")
	return cmd
}

// writePendingTable renders the outstanding questions as an aligned table.
func writePendingTable(w io.Writer, pending []*hitl.Request) error {
	if len(pending) == 0 {
		_, err := fmt.Fprintln(w, "no pending questions")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tAGE\tORIGIN\tCHOICES\tTITLE")
	now := time.Now()
	for _, req := range pending {
		title := req.Title
		if title == "" {
			title = firstLine(req.Body)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			req.ID, age(now, req.CreatedAt), req.Origin.Label(), len(req.Choices), truncate(title, 60))
	}
	return tw.Flush()
}

// age renders how long a question has been waiting, at second resolution.
func age(now, created time.Time) string {
	if created.IsZero() {
		return "-"
	}
	d := now.Sub(created)
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func truncate(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit-1]) + "…"
}

func newAnswerCommand(g *globals) *cobra.Command {
	var choice, text string
	cmd := &cobra.Command{
		Use:   "answer <request-id>",
		Short: "Answer a pending question from the terminal",
		Long: "Resolve a question without touching the messenger. Exactly one of\n" +
			"--choice or --text is required.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case choice == "" && text == "":
				return usagef("answer: pass --choice or --text")
			case choice != "" && text != "":
				return usagef("answer: --choice and --text are mutually exclusive")
			}
			ctx, stop := signalContext(cmd.Context())
			defer stop()

			client, _, err := connect(ctx, g, cmd.ErrOrStderr(), false)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()

			params := &ipc.AnswerParams{
				RequestID: args[0],
				ChoiceID:  choice,
				Text:      text,
				Responder: firstEnv("USER", "USERNAME", "LOGNAME"),
			}
			if _, err := client.Do(ctx, &ipc.Request{Op: ipc.OpAnswer, Answer: params}); err != nil {
				return failf("answer: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&choice, "choice", "", "id of the predefined choice to pick")
	cmd.Flags().StringVar(&text, "text", "", "free-text answer")
	return cmd
}

func newCancelCommand(g *globals) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:          "cancel <request-id>",
		Short:        "Withdraw a pending question",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signalContext(cmd.Context())
			defer stop()

			client, _, err := connect(ctx, g, cmd.ErrOrStderr(), false)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()

			params := &ipc.CancelParams{RequestID: args[0], Reason: reason}
			if _, err := client.Do(ctx, &ipc.Request{Op: ipc.OpCancel, Cancel: params}); err != nil {
				return failf("cancel: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "explanation shown to the human")
	return cmd
}
