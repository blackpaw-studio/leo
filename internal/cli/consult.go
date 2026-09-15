package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/spf13/cobra"
)

// Testability seams — overridden in tests.
var (
	consultStdout       io.Writer = os.Stdout
	consultPollInterval           = 250 * time.Millisecond
)

func newConsultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consult",
		Short: "Inspect one-off consultant subagents",
		Long: `Inspect the one-off consultants agents start with the leo_consult tool.

Consults run headless inside the daemon, recording what they do to
<state>/consults. These commands read those recordings directly, so they
keep working even when the daemon does not.`,
	}
	cmd.AddCommand(newConsultListCmd(), newConsultWatchCmd())
	return cmd
}

func newConsultListCmd() *cobra.Command {
	var host string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List consults, in-flight first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				extra := []string{"list"}
				if asJSON {
					extra = append(extra, "--json")
				}
				return runRemoteGroup(res, "consult", extra)
			}
			return listConsults(cfg.StatePath(), asJSON, consultStdout)
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().BoolVar(&asJSON, "json", false, "output records as JSON")
	return cmd
}

func newConsultWatchCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:   "watch [id]",
		Short: "Watch a consultant work, live",
		Long: `Replay a consult's recorded activity, then follow it until it finishes.

With no id, the newest running consult is chosen, falling back to the most
recent one. An id may be abbreviated to any unique prefix.

Ctrl-C detaches; the consult keeps running.`,
		Example: `  # Watch whatever is running now
  leo consult watch

  # Watch a specific consult by prefix
  leo consult watch c-7f3a`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix := ""
			if len(args) == 1 {
				prefix = args[0]
			}
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				extra := []string{"watch"}
				if prefix != "" {
					extra = append(extra, prefix)
				}
				return runRemoteGroup(res, "consult", extra)
			}
			// Detaching is a normal way to finish, not an error — today
			// Ctrl-C kills the process outright, but this keeps the
			// promise true if signal handling is ever wired up.
			err = watchConsult(cmd.Context(), cfg.StatePath(), prefix, consultStdout)
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
	addHostFlag(cmd, &host)
	return cmd
}

// listConsults prints every recorded consult, in-flight ones first so the
// thing worth watching is at the top.
func listConsults(stateDir string, asJSON bool, out io.Writer) error {
	records, err := consult.Load(stateDir)
	if err != nil {
		return err
	}
	ordered := inFlightFirst(records)

	if asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(ordered)
	}
	if len(ordered) == 0 {
		fmt.Fprintln(out, "No consults recorded yet.")
		return nil
	}

	now := time.Now()
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tCALLER\tTEMPLATE\tMODEL\tMODE\tTURNS\tSTEERED\tELAPSED\tACTIVE\tSTATUS\tINPUT\tOUTPUT\tCOST_USD\tUSAGE_TURNS\tTOOLS\tPARTIAL")
	for _, record := range ordered {
		mode := record.Mode
		if mode == "" {
			mode = consult.ModeHeadless
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%t\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%t\n",
			record.ID, orDash(record.Caller), record.Template, record.Model,
			mode, len(record.Turns), record.Steered, formatOffset(record.Elapsed(now)),
			formatOffset(secondsDuration(record.LiveActiveSeconds(now))), displayStatus(record, now), optionalInt64(record.InputTokens), optionalInt64(record.OutputTokens), optionalFloat(record.CostUSD), optionalInt(record.UsageTurns), optionalInt(record.ToolCalls), record.UsageIncomplete)
	}
	return w.Flush()
}

func optionalInt64(v *int64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%d", *v)
}
func optionalInt(v *int) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%d", *v)
}
func optionalFloat(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%g", *v)
}

func secondsDuration(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}

// displayStatus reports "abandoned" for a consult whose record stopped
// being updated. Showing it as "running" would be a lie that never expires.
func displayStatus(record consult.Record, now time.Time) string {
	if record.Stale(now) {
		return "abandoned"
	}
	return string(record.Status)
}

// inFlightFirst partitions records into still-running then settled,
// keeping the newest-first order Load supplies within each group.
func inFlightFirst(records []consult.Record) []consult.Record {
	now := time.Now()
	ordered := make([]consult.Record, 0, len(records))
	for _, record := range records {
		if !record.Settled(now) {
			ordered = append(ordered, record)
		}
	}
	for _, record := range records {
		if record.Settled(now) {
			ordered = append(ordered, record)
		}
	}
	return ordered
}

// watchConsult replays a consult's recording and then follows it to
// completion.
func watchConsult(ctx context.Context, stateDir, prefix string, out io.Writer) error {
	records, err := consult.Load(stateDir)
	if err != nil {
		return err
	}
	record, err := resolveConsult(records, prefix)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "[consult %s · %s/%s%s · %s]\n",
		record.ID, record.Harness, record.Model, callerSuffix(record), record.Status)
	if record.Prompt != "" {
		fmt.Fprintf(out, "%s\n\n", harness.FirstLine(record.Prompt))
	}

	stream, err := os.Open(consult.StreamPath(stateDir, record.ID))
	if err != nil {
		return fmt.Errorf("opening consult stream: %w", err)
	}
	defer stream.Close()

	renderer := consult.NewRenderer(record.Harness)
	tail := &streamTailer{f: stream}

	for {
		// Read the status *before* draining. A consult only reaches a
		// terminal status once its stream is fully written, so a terminal
		// reading here guarantees the drain below sees everything.
		record, err = consult.LoadOne(stateDir, record.ID)
		if err != nil {
			// The record is gone — removed, or the state directory was
			// cleared. Say so instead of polling a stale copy forever.
			return err
		}
		events, err := tail.drain()
		if err != nil {
			return err
		}
		for _, event := range events {
			for _, line := range renderer.Render(event) {
				fmt.Fprintln(out, line)
			}
		}
		if now := time.Now(); record.Settled(now) {
			reportOutcome(out, record, now)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(consultPollInterval):
		}
	}
}

// reportOutcome closes out a feed. An abandoned consult is called out
// rather than reported as still running: nothing will ever update it.
func reportOutcome(out io.Writer, record consult.Record, now time.Time) {
	if record.Stale(now) {
		fmt.Fprintf(out, "[abandoned — no result after %s; the daemon likely died mid-consult]\n",
			formatOffset(record.Elapsed(now)))
		return
	}
	fmt.Fprintf(out, "[%s after %s]\n", record.Status, formatOffset(record.Elapsed(now)))
	if record.Error != "" {
		fmt.Fprintln(out, record.Error)
	}
}

// resolveConsult picks the consult to watch. An empty prefix prefers work
// still in flight, which is what someone typing `leo consult watch` almost
// always means.
func resolveConsult(records []consult.Record, prefix string) (consult.Record, error) {
	if len(records) == 0 {
		return consult.Record{}, errors.New("no dispatches recorded yet")
	}
	if prefix == "" {
		now := time.Now()
		for _, record := range records {
			// Settled, not merely terminal: an abandoned consult must not
			// win over a real one just because nothing closed its record.
			if !record.Settled(now) {
				return record, nil
			}
		}
		return records[0], nil
	}

	var matches []consult.Record
	for _, record := range records {
		if strings.HasPrefix(record.ID, prefix) {
			matches = append(matches, record)
		}
	}
	switch len(matches) {
	case 0:
		return consult.Record{}, fmt.Errorf("no consult matches %q", prefix)
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.ID)
		}
		return consult.Record{}, fmt.Errorf("%q matches %d consults: %s", prefix, len(matches), strings.Join(ids, ", "))
	}
}

// streamTailer reads complete framed lines from a stream the daemon may
// still be appending to, holding a torn trailing line until it completes.
type streamTailer struct {
	f       *os.File
	pending []byte
}

func (t *streamTailer) drain() ([]consult.StreamEvent, error) {
	chunk, err := io.ReadAll(t.f)
	if err != nil {
		return nil, fmt.Errorf("reading consult stream: %w", err)
	}
	t.pending = append(t.pending, chunk...)

	var events []consult.StreamEvent
	for {
		i := bytes.IndexByte(t.pending, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimRight(t.pending[:i], "\r")
		t.pending = t.pending[i+1:]
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if event, ok := consult.DecodeEvent(line); ok {
			events = append(events, event)
		}
	}
	return events, nil
}

func formatOffset(d time.Duration) string { return consult.FormatOffset(d) }

// Compatibility adapter for the older watch tests; all event formatting lives
// in consult.Renderer.
const feedIndent = consult.FeedIndent

type consultFeed struct {
	out      io.Writer
	renderer harness.EventRenderer
}

func (f *consultFeed) emit(event consult.StreamEvent) {
	for _, line := range consult.NewRendererFor(f.renderer).Render(event) {
		fmt.Fprintln(f.out, line)
	}
}

func callerSuffix(record consult.Record) string {
	if record.Caller == "" {
		return ""
	}
	return " · from " + record.Caller
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
