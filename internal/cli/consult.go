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

// feedIndent aligns continuation lines under the body column of a feed row.
const feedIndent = 18

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
			return watchConsult(cmd.Context(), cfg.StatePath(), prefix, consultStdout)
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
	fmt.Fprintln(w, "ID\tCALLER\tTEMPLATE\tMODEL\tELAPSED\tSTATUS")
	for _, record := range ordered {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			record.ID, orDash(record.Caller), record.Template, record.Model,
			formatOffset(record.Elapsed(now)), record.Status)
	}
	return w.Flush()
}

// inFlightFirst partitions records into unfinished then finished, keeping
// the newest-first order Load supplies within each group.
func inFlightFirst(records []consult.Record) []consult.Record {
	ordered := make([]consult.Record, 0, len(records))
	for _, record := range records {
		if !record.Status.Terminal() {
			ordered = append(ordered, record)
		}
	}
	for _, record := range records {
		if record.Status.Terminal() {
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

	feed := &consultFeed{out: out, renderer: rendererFor(record.Harness)}
	tail := &streamTailer{f: stream}

	for {
		// Read the status *before* draining. A consult only reaches a
		// terminal status once its stream is fully written, so a terminal
		// reading here guarantees the drain below sees everything.
		current, err := currentRecord(stateDir, record.ID)
		if err == nil {
			record = current
		}
		events, err := tail.drain()
		if err != nil {
			return err
		}
		for _, event := range events {
			feed.emit(event)
		}
		if record.Status.Terminal() {
			fmt.Fprintf(out, "[%s after %s]\n", record.Status, formatOffset(record.Elapsed(time.Now())))
			if record.Error != "" {
				fmt.Fprintln(out, record.Error)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(consultPollInterval):
		}
	}
}

// resolveConsult picks the consult to watch. An empty prefix prefers work
// still in flight, which is what someone typing `leo consult watch` almost
// always means.
func resolveConsult(records []consult.Record, prefix string) (consult.Record, error) {
	if len(records) == 0 {
		return consult.Record{}, errors.New("no consults recorded yet")
	}
	if prefix == "" {
		for _, record := range records {
			if !record.Status.Terminal() {
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

func currentRecord(stateDir, id string) (consult.Record, error) {
	records, err := consult.Load(stateDir)
	if err != nil {
		return consult.Record{}, err
	}
	for _, record := range records {
		if record.ID == id {
			return record, nil
		}
	}
	return consult.Record{}, fmt.Errorf("consult %s is no longer recorded", id)
}

// rendererFor resolves a harness's live-feed renderer. A harness without
// one makes the feed fall back to raw event lines.
func rendererFor(name string) harness.EventRenderer {
	h, err := harness.Get(name)
	if err != nil {
		return nil
	}
	renderer, ok := h.(harness.EventRenderer)
	if !ok {
		return nil
	}
	return renderer
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

// consultFeed renders recorded events as readable rows.
type consultFeed struct {
	out io.Writer
	// renderer is nil for a harness with no live-feed mapping; its events
	// print as raw JSON rather than not at all.
	renderer harness.EventRenderer
}

func (f *consultFeed) emit(event consult.StreamEvent) {
	if event.Raw != "" {
		f.row(event.Offset, "raw", event.Raw)
		return
	}
	if len(event.Data) == 0 {
		return
	}
	if f.renderer == nil {
		f.row(event.Offset, "raw", string(event.Data))
		return
	}
	for _, rendered := range f.renderer.RenderEvent(event.Data) {
		label := string(rendered.Kind)
		if rendered.Kind == harness.EventTool {
			// Lower-cased so the column reads the same across harnesses,
			// which disagree on tool-name casing.
			label = strings.ToLower(rendered.Tool)
		}
		f.row(event.Offset, label, rendered.Summary)
	}
}

func (f *consultFeed) row(offset time.Duration, label, body string) {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	fmt.Fprintf(f.out, "%7s  %-8s %s\n", formatOffset(offset), label, lines[0])
	for _, extra := range lines[1:] {
		fmt.Fprintf(f.out, "%s%s\n", strings.Repeat(" ", feedIndent), extra)
	}
}

// formatOffset renders a duration as m:ss, growing to h:mm:ss only when
// needed.
func formatOffset(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	hours, minutes, seconds := total/3600, (total%3600)/60, total%60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
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
