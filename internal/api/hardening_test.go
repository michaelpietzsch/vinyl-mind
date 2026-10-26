package api

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/auth"
	"github.com/michaelpietzsch/vinyl-mind/internal/client"
	"github.com/michaelpietzsch/vinyl-mind/internal/config"
	"github.com/michaelpietzsch/vinyl-mind/internal/keys"
	"github.com/michaelpietzsch/vinyl-mind/internal/ratelimit"
	"github.com/michaelpietzsch/vinyl-mind/internal/store"
)

// The findings of the security review of October 2026 (A-1 to A-12), each with the test that would have found it.

// A-2: a replayed request must not use up the station's own quota.
func TestReplayDoesNotUseStationQuota(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.StationRequestsPerMin = 3 })
	s := e.active()
	h := auth.Sign(s.Key, e.srv.Fingerprint(), "GET", "/v1/me", nil, e.now)
	send := func() int {
		req, _ := http.NewRequest("GET", e.http.URL+"/v1/me", nil)
		req.Header = h.Clone()
		req.Header.Set(auth.HClient, "vinyl-check/1.1.0")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if send() != 200 {
		t.Fatal("the first request is fine")
	}
	for i := 0; i < 10; i++ {
		if send() != 401 {
			t.Fatal("a replay is refused")
		}
	}
	if r := do(t, s, "GET", "/v1/me", nil, 200); r.Status != 200 {
		t.Fatalf("ten replays must not cost the station its quota: %d", r.Status)
	}
}

// A-1: refused requests of unknown senders are counted, not listed, and nothing of them is written per request.
func TestRefusedRequestsAreCounted(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.AddressRequestsPerMin = 5 })
	before, _ := e.store.Count("access_log")
	statuses := map[int]int{}
	for i := 0; i < 40; i++ {
		resp, err := http.Get(e.http.URL + "/v1/" + strings.Repeat("x", 200))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		statuses[resp.StatusCode]++
	}
	if statuses[404] != 5 || statuses[429] != 35 {
		t.Fatalf("statuses %v", statuses)
	}
	if n, _ := e.store.Count("access_log"); n != before {
		t.Fatalf("%d rows written for refused requests before the flush", n-before)
	}
	e.srv.Flush()
	entries, _ := e.store.AccessLog(10)
	sum := 0
	for _, x := range entries {
		sum += x.N
	}
	if len(entries) != 2 || sum != 40 {
		t.Fatalf("after the flush: %d rows for %d requests: %+v", len(entries), sum, entries)
	}
	// Signed requests are still listed one by one
	e.srv.perAddress = ratelimit.New(1000, time.Minute)
	s := e.active()
	do(t, s, "GET", "/v1/me", nil, 200)
	entries, _ = e.store.AccessLog(1)
	if entries[0].KeyFP == "" || entries[0].N != 1 {
		t.Fatalf("a station's request: %+v", entries[0])
	}
}

// A-3: the path is logged as it came over the wire, without decoded control characters (unknown paths of unknown
// senders are only counted, see above; a station's request to a known route with such a path is listed).
func TestAccessLogKeepsControlCharactersEncoded(t *testing.T) {
	e := newEnv(t, nil)
	s := e.active()
	do(t, s, "PUT", "/v1/profiles/%1b%5b31mRED%0a2026-01-01%09203.0.113.5", example(t, "contribution.json"), 400)
	entries, _ := e.store.AccessLog(1)
	for _, b := range []byte(entries[0].Path) {
		if b < 0x20 || b == 0x7f {
			t.Fatalf("control character %#x in the logged path %q", b, entries[0].Path)
		}
	}
	if !strings.Contains(entries[0].Path, "%1b") {
		t.Fatalf("logged path: %q", entries[0].Path)
	}
}

// A-4: an IPv6 subscriber has a whole /64; the address limits count on it.
func TestIPv6LimitsCountOnThe64(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.TrustedProxies = []string{"127.0.0.1"}
		c.AddressRequestsPerMin = 3
	})
	get := func(addr string) int {
		req, _ := http.NewRequest("GET", e.http.URL+"/v1/server", nil)
		req.Header.Set("X-Forwarded-For", addr)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := 1; i <= 3; i++ {
		if get(fmt.Sprintf("2001:db8:1:2::%d", i)) != 200 {
			t.Fatal("within the limit")
		}
	}
	if get("2001:db8:1:2:ffff::9") != 429 {
		t.Fatal("the fourth address of the same /64 shares the bucket")
	}
	if get("2001:db8:1:3::1") != 200 {
		t.Fatal("another /64 has its own bucket")
	}
	if get("203.0.113.1") != 200 || get("203.0.113.2") != 200 {
		t.Fatal("IPv4 counts per address")
	}
}

// A-11: a forwarded entry that is not an address is an error, not a request from the proxy.
func TestMalformedForwardedForIsRefused(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.TrustedProxies = []string{"127.0.0.1"} })
	req, _ := http.NewRequest("GET", e.http.URL+"/v1/server", nil)
	req.Header.Set("X-Forwarded-For", "garbage")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("malformed X-Forwarded-For: %d", resp.StatusCode)
	}
}

// A-8: a public key of small order would verify a fixed signature for every message; it is no key.
func TestSmallOrderKeyIsRefused(t *testing.T) {
	e := newEnv(t, nil)
	identity := make([]byte, 32)
	identity[0] = 1
	pub := keys.Encode(identity)
	code := "invitation-code"
	body, _ := json.Marshal(map[string]any{"public_key": pub, "invitation": code})
	req, _ := http.NewRequest("POST", e.http.URL+"/v1/join", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.HClient, "vinyl-check/1.1.0")
	req.Header.Set(auth.HKey, keys.Fingerprint(ed25519.PublicKey(identity)))
	req.Header.Set(auth.HTimestamp, fmt.Sprint(e.now.Unix()))
	req.Header.Set(auth.HNonce, keys.Encode(make([]byte, 16)))
	req.Header.Set(auth.HSignature, keys.Encode(append(identity, make([]byte, 32)...)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 400 || out["error"] != "bad_key" {
		t.Fatalf("small-order key: %d %v", resp.StatusCode, out)
	}
}

// A-10: only plain JSON is read.
func TestContentTypeAndEncodingAreChecked(t *testing.T) {
	e := newEnv(t, nil)
	s := e.active()
	body, _ := json.Marshal(example(t, "contribution.json"))
	send := func(ct, ce string) int {
		req, _ := http.NewRequest("PUT", e.http.URL+"/v1/profiles/"+profileA, bytes.NewReader(body))
		req.Header = auth.Sign(s.Key, e.srv.Fingerprint(), "PUT", "/v1/profiles/"+profileA, body, e.now)
		req.Header.Set(auth.HClient, "vinyl-check/1.1.0")
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		if ce != "" {
			req.Header.Set("Content-Encoding", ce)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if send("text/plain", "") != 415 || send("application/json", "gzip") != 415 {
		t.Fatal("other media types and encodings are refused")
	}
	if send("application/json; charset=utf-8", "") != 200 || send("", "") != 200 {
		t.Fatal("JSON, with or without a declared type, is read")
	}
}

// A-9: joins_per_day holds under parallel joins.
func TestJoinsPerDayHoldsUnderParallelJoins(t *testing.T) {
	for run := 0; run < 5; run++ {
		e := newEnv(t, func(c *config.Config) { c.JoinsPerDay = 1 })
		var wg sync.WaitGroup
		var mu sync.Mutex
		statuses := map[int]int{}
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				code := "invitation-code"
				r, err := e.station().Join(&code)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				statuses[r.Status]++
				mu.Unlock()
			}()
		}
		wg.Wait()
		sts, _ := e.store.Stations()
		if statuses[201] != 1 || len(sts) != 1 {
			t.Fatalf("run %d: %v joined, %d stations", run, statuses, len(sts))
		}
	}
}

// A-6: admissions and departures leave a trace.
func TestEventsAreLogged(t *testing.T) {
	e := newEnv(t, nil)
	s := e.active()
	do(t, s, "DELETE", "/v1/me", nil, 204)
	events, _ := e.store.Events(10)
	actions := map[string]bool{}
	for _, ev := range events {
		actions[ev.Action] = true
		if strings.Contains(ev.Detail, "invitation-code") {
			t.Fatal("an invitation code must never be logged")
		}
	}
	if !actions["join"] || !actions["leave"] {
		t.Fatalf("events: %+v", events)
	}
}

// B-3: the hub's statement about a model's status is signed with the server key.
func TestModelStateIsSigned(t *testing.T) {
	e := newEnv(t, nil)
	stations := []*client.Station{e.active(), e.active(), e.active()}
	for i, s := range stations {
		do(t, s, "PUT", "/v1/profiles/"+profileA, contributionWith(t, 1+0.01*float64(i)), 200)
	}
	if _, err := e.hub.Combine(e.now, false); err != nil {
		t.Fatal(err)
	}
	var ms struct{ Candidate map[string]any }
	_ = do(t, stations[0], "GET", "/v1/models?feature_version=1&detector_version=1", nil, 200).JSON(&ms)
	token, _ := ms.Candidate["state_jws"].(string)
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("state_jws: %q", token)
	}
	header, _ := keys.Decode(parts[0])
	if !strings.Contains(string(header), keys.StateType) {
		t.Fatalf("header: %s", header)
	}
	payloadRaw, _ := keys.Decode(parts[1])
	var state map[string]any
	if err := json.Unmarshal(payloadRaw, &state); err != nil || state["status"] != store.Candidate ||
		state["server"] != e.srv.Fingerprint() {
		t.Fatalf("state: %s", payloadRaw)
	}
	sig, _ := keys.Decode(parts[2])
	pub := e.srv.key.Public().(ed25519.PublicKey)
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		t.Fatal("the state is not signed with the server key")
	}
}
