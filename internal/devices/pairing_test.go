package devices

import (
	"errors"
	"net"
	"reflect"
	"testing"
)

func ipNet(cidr string) net.Addr {
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

func TestPairingURLs(t *testing.T) {
	ifaces := func() ([]net.Addr, error) {
		return []net.Addr{
			ipNet("127.0.0.1/8"),
			ipNet("192.168.1.20/24"),
			ipNet("fe80::1/64"),
			ipNet("fd00::20/64"),
			ipNet("10.0.2.10/24"),
		}, nil
	}
	cases := []struct {
		name   string
		public []string
		bind   string
		want   []string
		local  bool
	}{
		{"public urls win", []string{"https://leo.example.com/", "http://10.0.0.5:8370"}, "0.0.0.0", []string{"https://leo.example.com", "http://10.0.0.5:8370"}, false},
		{"specific bind", nil, "192.168.1.20", []string{"http://192.168.1.20:8370"}, false},
		{"wildcard bind lists LAN addresses", nil, "0.0.0.0", []string{"http://192.168.1.20:8370", "http://[fd00::20]:8370", "http://10.0.2.10:8370"}, false},
		{"ipv6 wildcard", nil, "::", []string{"http://192.168.1.20:8370", "http://[fd00::20]:8370", "http://10.0.2.10:8370"}, false},
		{"loopback bind is local only", nil, "127.0.0.1", []string{"http://127.0.0.1:8370"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, local, err := PairingURLs(tc.public, tc.bind, 8370, ifaces)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) || local != tc.local {
				t.Fatalf("got %q local=%v, want %q local=%v", got, local, tc.want, tc.local)
			}
		})
	}
}

func TestPairingURLsInterfaceError(t *testing.T) {
	_, _, err := PairingURLs(nil, "0.0.0.0", 8370, func() ([]net.Addr, error) { return nil, errors.New("boom") })
	if err == nil {
		t.Fatal("want error")
	}
}
