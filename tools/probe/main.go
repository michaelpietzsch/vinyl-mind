// Command probe checks a running Vinyl-Mind server from the outside, the way a station reaches it: through TLS and
// the reverse proxy.
//
//	go run ./tools/probe -url https://vinyl-mind.example.org [-fingerprint SHA256:…] [-invitation CODE]
//
// Without an invitation it checks the server key, that unsigned and unknown requests are refused, and the security
// headers. With an invitation (one use is enough) it also joins, makes signed requests and leaves again, which
// deletes the station: only this shows that the proxy passes path and body unchanged.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/client"
)

var failed bool

func check(ok bool, format string, a ...any) {
	mark := "ok  "
	if !ok {
		mark, failed = "FAIL", true
	}
	fmt.Printf("%s %s\n", mark, fmt.Sprintf(format, a...))
}

func main() {
	url := flag.String("url", "", "base URL of the server, e.g. https://vinyl-mind.example.org")
	want := flag.String("fingerprint", "", "expected fingerprint of the server key")
	invitation := flag.String("invitation", "", "invitation code for a full join, signed requests and leave")
	flag.Parse()
	if *url == "" {
		flag.Usage()
		os.Exit(2)
	}
	base := strings.TrimRight(*url, "/")
	hc := &http.Client{Timeout: 15 * time.Second}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	st := &client.Station{Base: base, Key: key, HTTP: hc}

	info, err := st.Connect()
	if err != nil {
		fmt.Println("FAIL GET /v1/server:", err)
		os.Exit(1)
	}
	check(true, "GET /v1/server: server key %s, joining by %s", info.Fingerprint, info.Join)
	if *want != "" {
		check(info.Fingerprint == *want, "fingerprint is the expected one")
	}
	skew := time.Since(time.Unix(info.Time, 0))
	check(skew < time.Minute && skew > -time.Minute, "clock difference to the server %s", skew.Round(time.Second))

	resp, err := hc.Get(base + "/v1/server")
	if err == nil {
		resp.Body.Close()
		h := resp.Header
		check(h.Get("X-Content-Type-Options") == "nosniff" && h.Get("Cache-Control") == "no-store",
			"security headers on answers")
		check(strings.Contains(h.Get("Strict-Transport-Security"), "max-age="), "HSTS set by the proxy")
	}
	for path, wantStatus := range map[string]int{"/admin": 403, "/healthz": 404, "/nowhere": 404} {
		if resp, err := hc.Get(base + path); err == nil {
			resp.Body.Close()
			check(resp.StatusCode == wantStatus, "GET %s from outside answers %d (got %d)", path, wantStatus,
				resp.StatusCode)
		}
	}
	if resp, err := hc.Get(base + "/v1/me"); err == nil {
		resp.Body.Close()
		check(resp.StatusCode == 401, "unsigned GET /v1/me is refused (got %d)", resp.StatusCode)
	}
	if r, err := st.Do("GET", "/v1/me", nil, nil); err == nil {
		check(r.Status == 401 && strings.Contains(string(r.Body), "unknown_key"),
			"signed request with an unknown key is refused (got %d)", r.Status)
	}
	big, _ := http.NewRequest("PUT", base+"/v1/profiles/profile-probe-000000", strings.NewReader(strings.Repeat(" ", 20<<10)))
	if resp, err := hc.Do(big); err == nil {
		resp.Body.Close()
		check(resp.StatusCode == 413, "a 20 KiB body is refused (got %d)", resp.StatusCode)
	}

	if *invitation != "" {
		r, err := st.Join(invitation)
		check(err == nil && r != nil && r.Status == 201, "join with the invitation (got %v)", status(r, err))
		r, err = st.Do("GET", "/v1/me?probe=1", nil, nil)
		check(err == nil && r.Status == 200, "signed GET with a query reaches the server unchanged (got %v)",
			status(r, err))
		r, err = st.Do("GET", "/v1/models?feature_version=1&detector_version=1", nil, nil)
		check(err == nil && r.Status == 200, "signed GET /v1/models (got %v)", status(r, err))
		r, err = st.Do("DELETE", "/v1/me", nil, nil)
		check(err == nil && r.Status == 204, "leave again: the station is deleted (got %v)", status(r, err))
	}
	if failed {
		os.Exit(1)
	}
}

func status(r *client.Response, err error) string {
	if err != nil {
		return err.Error()
	}
	if r == nil {
		return "no answer"
	}
	return fmt.Sprintf("%d %s", r.Status, strings.TrimSpace(string(r.Body)))
}
