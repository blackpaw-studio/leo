package devices

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Payload is what `leo pair` shows the phone, as a QR code and as text.
type Payload struct {
	DaemonID    string   `json:"daemon_id"`
	Name        string   `json:"name"`
	URLs        []string `json:"urls"`
	PairingCode string   `json:"pairing_code"`
}

// PairingURLs returns the origins a phone should try, in order:
// web.public_urls when set (trailing slash trimmed), otherwise the web
// listener's own address — every non-loopback, non-link-local interface
// address for a wildcard bind. localOnly reports a loopback bind, which no
// phone can reach. ifaceAddrs is net.InterfaceAddrs in production.
func PairingURLs(publicURLs []string, bind string, port int, ifaceAddrs func() ([]net.Addr, error)) (urls []string, localOnly bool, err error) {
	if len(publicURLs) > 0 {
		out := make([]string, 0, len(publicURLs))
		for _, u := range publicURLs {
			out = append(out, strings.TrimSuffix(u, "/"))
		}
		return out, false, nil
	}
	ip := net.ParseIP(bind)
	switch {
	case ip == nil:
		return nil, false, fmt.Errorf("web.bind %q is not an IP address", bind)
	case ip.IsLoopback():
		return []string{listenerURL(ip, port)}, true, nil
	case !ip.IsUnspecified():
		return []string{listenerURL(ip, port)}, false, nil
	}
	addrs, err := ifaceAddrs()
	if err != nil {
		return nil, false, fmt.Errorf("listing interface addresses: %w", err)
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.IsLinkLocalUnicast() || !n.IP.IsGlobalUnicast() {
			continue
		}
		urls = append(urls, listenerURL(n.IP, port))
	}
	return urls, false, nil
}

func listenerURL(ip net.IP, port int) string {
	return "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(port))
}
