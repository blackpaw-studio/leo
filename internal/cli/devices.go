package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"rsc.io/qr"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/devices"
)

// newPairCmd pairs a phone (the Leo iOS app) with this machine's daemon.
func newPairCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Pair a phone with this daemon (prints a QR code)",
		Long: `Print a QR code and text payload for the Leo iOS app.

The payload carries this daemon's id and name, the URLs the phone should reach
it at (web.public_urls, or the web listener's LAN addresses), and a pairing
code. The code works once and expires after 5 minutes. Pairing issues the phone
its own bearer token; list and revoke paired phones with 'leo devices'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if asJSON {
				return runJSON(cmd.OutOrStdout(), func() (any, error) {
					cfg, err := loadConfig()
					if err != nil {
						return nil, failJSON("config_error", err)
					}
					return pairJSON(cfg, daemonDisplayName(), net.InterfaceAddrs)
				})
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return runPair(cmd.OutOrStdout(), cmd.ErrOrStderr(), cfg, daemonDisplayName(), net.InterfaceAddrs)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the payload, warnings, expiry and the exact QR string as one JSON object instead of a QR code")
	return markJSON(cmd)
}

// errWebDisabled is a pairing request against a daemon with no web listener.
var errWebDisabled = errors.New("the web listener is disabled (web.enabled: false); a phone has nothing to connect to")

// buildPairing mints a pairing code and assembles the payload the phone
// scans, with a warning for each URL the phone will not be able to use.
func buildPairing(cfg *config.Config, name string, ifaceAddrs func() ([]net.Addr, error)) (devices.Payload, []string, time.Time, error) {
	if !cfg.Web.Enabled {
		return devices.Payload{}, nil, time.Time{}, errWebDisabled
	}
	urls, localOnly, err := devices.PairingURLs(cfg.Web.PublicURLs, cfg.WebBind(), cfg.WebPort(), ifaceAddrs)
	if err != nil {
		return devices.Payload{}, nil, time.Time{}, err
	}
	daemonID, err := devices.EnsureDaemonID(cfg.StatePath())
	if err != nil {
		return devices.Payload{}, nil, time.Time{}, err
	}
	code, expires, err := devices.NewStore(cfg.StatePath(), nil).NewPairingCode()
	if err != nil {
		return devices.Payload{}, nil, time.Time{}, fmt.Errorf("creating pairing code: %w", err)
	}

	warnings := []string{}
	if localOnly || len(urls) == 0 {
		warnings = append(warnings, "the web listener is loopback-only, so a phone cannot reach it. "+
			"Set web.public_urls (e.g. a Cloudflare Tunnel hostname) or web.bind + web.allowed_hosts.")
	}
	for _, u := range unreachableHosts(cfg, urls) {
		warnings = append(warnings, fmt.Sprintf("%s is not in web.allowed_hosts; the daemon will refuse requests to it", u))
	}
	return devices.Payload{DaemonID: daemonID, Name: name, URLs: urls, PairingCode: code}, warnings, expires, nil
}

// runPair prints the pairing payload as a QR code and as one line of JSON.
func runPair(out, errOut io.Writer, cfg *config.Config, name string, ifaceAddrs func() ([]net.Addr, error)) error {
	payload, warnings, expires, err := buildPairing(cfg, name, ifaceAddrs)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintf(errOut, "warning: %s\n", w)
	}
	if err := renderQR(out, string(encoded)); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nScan with the Leo app, or paste this payload:\n\n%s\n\n", encoded)
	fmt.Fprintf(out, "The code works once and expires at %s.\n", expires.Local().Format(time.Kitchen))
	return nil
}

// pairResult is `leo pair --json`: the payload's own fields, plus what the
// caller needs to show it. QRPayload is the exact string to encode as a QR.
type pairResult struct {
	devices.Payload
	Warnings  []string `json:"warnings"`
	ExpiresAt string   `json:"expires_at"`
	QRPayload string   `json:"qr_payload"`
}

// pairJSON builds the --json result. A disabled web listener fails with the
// stable code web_disabled and no message.
func pairJSON(cfg *config.Config, name string, ifaceAddrs func() ([]net.Addr, error)) (any, error) {
	payload, warnings, expires, err := buildPairing(cfg, name, ifaceAddrs)
	if errors.Is(err, errWebDisabled) {
		return nil, failJSON("web_disabled", nil)
	}
	if err != nil {
		return nil, failJSON("pairing_failed", err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, failJSON("pairing_failed", err)
	}
	return pairResult{Payload: payload, Warnings: warnings, ExpiresAt: expires.UTC().Format(time.RFC3339), QRPayload: string(encoded)}, nil
}

// unreachableHosts lists the URLs (other than web.public_urls, which the
// daemon always accepts) whose host the Host check would refuse.
func unreachableHosts(cfg *config.Config, urls []string) []string {
	var out []string
	for _, raw := range urls {
		if slices.Contains(cfg.Web.PublicURLs, raw) || slices.Contains(cfg.Web.PublicURLs, raw+"/") {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		ip := net.ParseIP(host)
		if (ip != nil && ip.IsLoopback()) || host == "localhost" || slices.ContainsFunc(cfg.Web.AllowedHosts, func(h string) bool {
			return strings.EqualFold(strings.TrimSpace(h), host)
		}) {
			continue
		}
		out = append(out, raw)
	}
	return out
}

// daemonDisplayName is the name the app shows for this daemon: the short
// hostname.
func daemonDisplayName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "leo"
	}
	return strings.TrimSuffix(h, ".local")
}

// renderQR prints text as a QR code using half-block characters, two
// modules per character cell, black on white regardless of terminal theme.
func renderQR(w io.Writer, text string) error {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return fmt.Errorf("encoding QR code: %w", err)
	}
	const quiet = 2
	dark := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
	}
	var b strings.Builder
	for y := -quiet; y < code.Size+quiet; y += 2 {
		b.WriteString("\x1b[30;47m")
		for x := -quiet; x < code.Size+quiet; x++ {
			top, bottom := dark(x, y), dark(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\x1b[0m\n")
	}
	_, err = io.WriteString(w, b.String())
	return err
}

// newDevicesCmd manages paired phones.
func newDevicesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "List and revoke paired phones",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List paired devices",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return runDevicesList(cmd.OutOrStdout(), devices.NewStore(cfg.StatePath(), nil))
		},
	}, &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke a paired device's token immediately",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return runDevicesRevoke(cmd.OutOrStdout(), devices.NewStore(cfg.StatePath(), nil), args[0])
		},
	})
	return cmd
}

func runDevicesList(out io.Writer, store *devices.Store) error {
	list, err := store.List()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(out, "No paired devices. Pair one with: leo pair")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tPAIRED\tPUSH")
	for _, d := range list {
		push := "-"
		if d.Push != nil {
			push = "registered"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", d.ID, d.Name, d.CreatedAt.Local().Format("2006-01-02 15:04"), push)
	}
	return tw.Flush()
}

func runDevicesRevoke(out io.Writer, store *devices.Store, id string) error {
	if err := store.Revoke(id); err != nil {
		return fmt.Errorf("revoking %s: %w", id, err)
	}
	fmt.Fprintf(out, "Revoked device %s.\n", id)
	return nil
}
