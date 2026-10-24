package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExampleMatchesDefaults(t *testing.T) {
	// config.example.toml documents the defaults; this keeps the two from drifting apart
	got, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.TrustedProxies, got.TrustedProxies = nil, nil
	got.proxies, want.proxies = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config.example.toml differs from Default():\n got %+v\nwant %+v", got, want)
	}
}

func TestRefusesMistakes(t *testing.T) {
	for content, want := range map[string]string{
		"listn = \":8080\"\n":                   "unknown setting(s): listn",
		"[aggregation]\nmethd = \"median\"\n":   "aggregation.methd",
		"join_mode = \"open\"\n":                "join_mode",
		"[aggregation]\ntrim = 0.5\n":           "trim",
		"trusted_proxies = [\"proxy.local\"]\n": "trusted_proxies",
	} {
		if _, err := Load(write(t, content)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want an error about %q, got %v", content, want, err)
		}
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Error("an explicitly named file that does not exist is an error")
	}
}

func TestEnvironmentAndProxies(t *testing.T) {
	t.Setenv("VINYL_MIND_DATA_DIR", "/srv/vm")
	t.Setenv("VINYL_MIND_ADMIN_TOKEN", "a-token-with-at-least-32-characters!")
	t.Setenv("VINYL_MIND_DATABASE_URL", "postgres://x")
	c, err := Load(write(t, "trusted_proxies = [\"172.18.0.0/16\", \"10.0.0.1\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "/srv/vm" || c.AdminToken != "a-token-with-at-least-32-characters!" || c.DBURL() != "postgres://x" ||
		c.ServerKeyPath() != filepath.Join("/srv/vm", "server-key.pem") {
		t.Fatalf("environment not applied: %+v", c)
	}
	for addr, want := range map[string]bool{"172.18.3.4": true, "10.0.0.1": true, "10.0.0.2": false} {
		if got := c.TrustedProxy(mustAddr(t, addr)); got != want {
			t.Errorf("%s trusted: %v", addr, got)
		}
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestShortAdminTokenIsRefused(t *testing.T) {
	t.Setenv("VINYL_MIND_ADMIN_TOKEN", "short")
	if _, err := Load(write(t, "")); err == nil || !strings.Contains(err.Error(), "32 characters") {
		t.Fatalf("a short admin token must be refused: %v", err)
	}
}
