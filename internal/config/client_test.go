package config

import (
	"testing"
)

func TestResolveHost(t *testing.T) {
	twoHosts := map[string]HostConfig{
		"alpha": {SSH: "u@alpha"},
		"beta":  {SSH: "u@beta"},
	}

	cases := []struct {
		name      string
		cfg       Config
		flag      string
		env       string
		wantName  string
		wantLocal bool
		wantErr   bool
	}{
		{
			name:      "no hosts configured — localhost",
			cfg:       Config{},
			wantLocal: true,
		},
		{
			name:     "flag selects named host",
			cfg:      Config{Client: ClientConfig{Hosts: twoHosts}},
			flag:     "alpha",
			wantName: "alpha",
		},
		{
			name:    "flag names unknown host — error",
			cfg:     Config{Client: ClientConfig{Hosts: twoHosts}},
			flag:    "gamma",
			wantErr: true,
		},
		{
			name:      "flag=localhost forces localhost even with hosts",
			cfg:       Config{Client: ClientConfig{Hosts: twoHosts}},
			flag:      "localhost",
			wantLocal: true,
		},
		{
			name:     "env var selects host when flag empty",
			cfg:      Config{Client: ClientConfig{Hosts: twoHosts}},
			env:      "beta",
			wantName: "beta",
		},
		{
			name:     "default_host selects host when flag + env empty",
			cfg:      Config{Client: ClientConfig{DefaultHost: "alpha", Hosts: twoHosts}},
			wantName: "alpha",
		},
		{
			name:     "first host when nothing else set",
			cfg:      Config{Client: ClientConfig{Hosts: twoHosts}},
			wantName: "alpha", // sorted: alpha < beta
		},
		{
			name:     "flag overrides default_host",
			cfg:      Config{Client: ClientConfig{DefaultHost: "alpha", Hosts: twoHosts}},
			flag:     "beta",
			wantName: "beta",
		},
		{
			name:     "env overrides default_host",
			cfg:      Config{Client: ClientConfig{DefaultHost: "alpha", Hosts: twoHosts}},
			env:      "beta",
			wantName: "beta",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("LEO_HOST", tc.env)
			} else {
				t.Setenv("LEO_HOST", "")
			}
			got, err := tc.cfg.ResolveHost(tc.flag)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Localhost != tc.wantLocal {
				t.Errorf("Localhost = %v, want %v", got.Localhost, tc.wantLocal)
			}
			if got.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
			}
		})
	}
}
