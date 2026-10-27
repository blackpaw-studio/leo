package config

import (
	"strings"
	"testing"
	"time"
)

func TestFollowInterval(t *testing.T) {
	cases := []struct {
		follow *FollowConfig
		want   time.Duration
	}{
		{nil, 0},
		{&FollowConfig{Host: "lead"}, 5 * time.Minute},
		{&FollowConfig{Host: "lead", Interval: "45s"}, 45 * time.Second},
		{&FollowConfig{Host: "lead", Interval: "2h"}, 2 * time.Hour},
	}
	for _, tc := range cases {
		cfg := &Config{Follow: tc.follow}
		if got := cfg.FollowInterval(); got != tc.want {
			t.Errorf("FollowInterval(%+v) = %v, want %v", tc.follow, got, tc.want)
		}
	}
}

func TestValidateFollow(t *testing.T) {
	hosts := map[string]HostConfig{
		"lead":   {SSH: "evan@10.0.2.10"},
		"self":   {SSH: "evan@localhost"},
		"self4":  {SSH: "127.0.0.1"},
		"self6":  {SSH: "evan@[::1]"},
		"selfv6": {SSH: "::1"},
		"selfu":  {SSH: "ssh://evan@127.0.0.2:2222"},
	}
	cases := []struct {
		name    string
		follow  *FollowConfig
		wantErr string
	}{
		{"absent", nil, ""},
		{"valid", &FollowConfig{Host: "lead"}, ""},
		{"valid interval", &FollowConfig{Host: "lead", Interval: "30s"}, ""},
		{"host required", &FollowConfig{}, "follow.host is required"},
		{"unknown host", &FollowConfig{Host: "nope"}, `follow.host "nope" is not defined in client.hosts`},
		{"localhost sentinel", &FollowConfig{Host: "localhost"}, `follow.host "localhost" names this host`},
		{"interval too short", &FollowConfig{Host: "lead", Interval: "29s"}, "follow.interval \"29s\" must be a duration of at least 30s"},
		{"interval unparseable", &FollowConfig{Host: "lead", Interval: "soon"}, "follow.interval \"soon\" must be a duration of at least 30s"},
		{"loopback name", &FollowConfig{Host: "self"}, "follow.host \"self\" points at this host"},
		{"loopback v4", &FollowConfig{Host: "self4"}, "points at this host"},
		{"loopback v6 bracket", &FollowConfig{Host: "self6"}, "points at this host"},
		{"loopback v6 bare", &FollowConfig{Host: "selfv6"}, "points at this host"},
		{"loopback url", &FollowConfig{Host: "selfu"}, "points at this host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Client: ClientConfig{Hosts: hosts}, Follow: tc.follow}
			err := cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestFollowAbsentRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "leo.yaml", "defaults:\n  model: sonnet\ntasks: {}\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Follow != nil || cfg.IsFollower() {
		t.Fatalf("Follow = %+v, want nil", cfg.Follow)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, path), "follow") {
		t.Fatalf("saved config gained a follow key:\n%s", readFile(t, path))
	}
}
