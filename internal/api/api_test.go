package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/auth"
	"github.com/michaelpietzsch/vinyl-mind/internal/client"
	"github.com/michaelpietzsch/vinyl-mind/internal/config"
	"github.com/michaelpietzsch/vinyl-mind/internal/hub"
	"github.com/michaelpietzsch/vinyl-mind/internal/keys"
	"github.com/michaelpietzsch/vinyl-mind/internal/store"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

type env struct {
	t     *testing.T
	cfg   config.Config
	store *store.Store
	hub   *hub.Hub
	srv   *Server
	http  *httptest.Server
	now   time.Time
}

func newEnv(t *testing.T, edit func(*config.Config)) *env {
	t.Helper()
	cfg := config.Default()
	// Requests in the tests come from one address at one frozen instant; the limits get their own test
	cfg.AddressRequestsPerMin, cfg.JoinsPerHour, cfg.StationRequestsPerMin = 100_000, 100_000, 100_000
	if edit != nil {
		edit(&cfg)
	}
	if err := cfg.Check(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "vm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	e := &env{t: t, cfg: cfg, store: st, now: time.Unix(1_790_000_000, 0)}
	clock := func() time.Time { return e.now }
	e.hub = hub.New(cfg, st, key, nil)
	e.srv = New(cfg, st, key, e.hub, "test", clock, nil)
	e.http = httptest.NewServer(e.srv.Handler())
	t.Cleanup(func() { e.http.Close(); st.Close() })
	if _, err := st.AddInvitation("invitation-code", 1000, nil, e.now); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) station() *client.Station {
	e.t.Helper()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	s := &client.Station{Base: e.http.URL, Key: key, Now: func() time.Time { return e.now }}
	if _, err := s.Connect(); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// active returns a station that joined with an invitation.
func (e *env) active() *client.Station {
	e.t.Helper()
	s := e.station()
	code := "invitation-code"
	r, err := s.Join(&code)
	if err != nil || r.Status != http.StatusCreated {
		e.t.Fatalf("join: %v %s", err, r.Body)
	}
	return s
}

func do(t *testing.T, s *client.Station, method, target string, body any, want int) *client.Response {
	t.Helper()
	r, err := s.Do(method, target, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, target, r.Status, want, r.Body)
	}
	return r
}

func errorCode(r *client.Response) string {
	var e struct{ Error string }
	_ = r.JSON(&e)
	return e.Error
}

func example(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "spec", "v1", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

const profileA = "profile-aaaaaaaaaaaa"

func TestServerInfo(t *testing.T) {
	e := newEnv(t, nil)
	s := e.station()
	info, err := s.Connect()
	if err != nil {
		t.Fatal(err)
	}
	if info.Fingerprint != e.srv.Fingerprint() || info.Join != "approval" || info.Time != e.now.Unix() {
		t.Fatalf("server info: %+v", info)
	}
	resp, _ := http.Get(e.http.URL + "/nowhere")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 404 || !strings.Contains(string(body), `"not_found"`) {
		t.Fatalf("unknown path: %d %s", resp.StatusCode, body)
	}
}

func TestJoin(t *testing.T) {
	e := newEnv(t, nil)
	s := e.station()
	r, _ := s.Join(nil)
	var j struct{ Station, Status, Server string }
	_ = r.JSON(&j)
	if r.Status != http.StatusCreated || j.Status != store.Pending || j.Station != s.Fingerprint() {
		t.Fatalf("join without invitation: %d %s", r.Status, r.Body)
	}
	do(t, s, "GET", "/v1/me", nil, 200)
	r = do(t, s, "PUT", "/v1/profiles/"+profileA, example(t, "contribution.json"), 403)
	if errorCode(r) != "pending" {
		t.Fatalf("pending station must not contribute: %s", r.Body)
	}
	if _, err := e.store.SetStatus(s.Fingerprint(), store.Active, e.now); err != nil {
		t.Fatal(err)
	}
	do(t, s, "PUT", "/v1/profiles/"+profileA, example(t, "contribution.json"), 200)
	if r, _ = s.Join(nil); r.Status != http.StatusOK {
		t.Fatalf("joining again: %d %s", r.Status, r.Body)
	}

	bad := "not-a-valid-code"
	if r, _ = e.station().Join(&bad); r.Status != 403 || errorCode(r) != "invitation_invalid" {
		t.Fatalf("wrong invitation: %d %s", r.Status, r.Body)
	}

	e2 := newEnv(t, func(c *config.Config) { c.JoinMode = "invitation" })
	if r, _ = e2.station().Join(nil); r.Status != 403 || errorCode(r) != "invitation_required" {
		t.Fatalf("invitation mode: %d %s", r.Status, r.Body)
	}
}

func TestJoinProvesPossessionOfTheKey(t *testing.T) {
	e := newEnv(t, nil)
	s := e.station()
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	// Signed with s's key, but registering someone else's public key
	r := do(t, s, "POST", "/v1/join", map[string]string{"public_key": keys.Encode(other)}, 401)
	if errorCode(r) != "bad_signature" {
		t.Fatalf("foreign key: %s", r.Body)
	}
	r = do(t, s, "POST", "/v1/join", map[string]any{"public_key": keys.Encode(s.Key.Public().(ed25519.PublicKey)),
		"name": "my studio"}, 422)
	if !strings.Contains(string(r.Body), "unknown field") {
		t.Fatalf("unknown fields must be refused: %s", r.Body)
	}
}

func TestSignedRequestsAreChecked(t *testing.T) {
	e := newEnv(t, nil)
	s := e.active()
	stranger := e.station()
	if r := do(t, stranger, "GET", "/v1/me", nil, 401); errorCode(r) != "unknown_key" {
		t.Fatalf("unknown key: %s", r.Body)
	}

	send := func(h http.Header, body []byte) *http.Response {
		req, _ := http.NewRequest("PUT", e.http.URL+"/v1/profiles/"+profileA, bytes.NewReader(body))
		req.Header = h
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	body, _ := json.Marshal(example(t, "contribution.json"))
	h := auth.Sign(s.Key, e.srv.Fingerprint(), "PUT", "/v1/profiles/"+profileA, body, e.now)
	if resp := send(h, append(body[:len(body)-1:len(body)-1], ' ', '}')); resp.StatusCode != 401 {
		t.Fatalf("changed body: %d", resp.StatusCode)
	}
	if resp := send(h, body); resp.StatusCode != 200 {
		t.Fatalf("original: %d", resp.StatusCode)
	}
	if resp := send(h, body); resp.StatusCode != 401 {
		t.Fatalf("replay: %d", resp.StatusCode)
	}
	old := auth.Sign(s.Key, e.srv.Fingerprint(), "PUT", "/v1/profiles/"+profileA, body, e.now.Add(-10*time.Minute))
	if resp := send(old, body); resp.StatusCode != 401 {
		t.Fatalf("stale: %d", resp.StatusCode)
	}
	if resp := send(http.Header{}, body); resp.StatusCode != 401 {
		t.Fatalf("unsigned: %d", resp.StatusCode)
	}
}

func TestContributions(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.MaxProfilesPerStation = 2 })
	a, b := e.active(), e.active()
	c := example(t, "contribution.json")
	r := do(t, a, "PUT", "/v1/profiles/"+profileA, c, 200)
	var put struct{ Changed bool }
	_ = r.JSON(&put)
	if !put.Changed {
		t.Fatal("first contribution is a change")
	}
	if _ = do(t, a, "PUT", "/v1/profiles/"+profileA, c, 200).JSON(&put); put.Changed {
		t.Fatal("same contribution again is no change")
	}

	invalid, _ := os.ReadFile(filepath.Join("..", "..", "spec", "v1", "examples", "invalid",
		"contribution-counts-mismatch.json"))
	r = do(t, a, "PUT", "/v1/profiles/"+profileA, invalid, 422)
	if !strings.Contains(string(r.Body), "n_manual + n_master") {
		t.Fatalf("message must name the problem: %s", r.Body)
	}
	do(t, a, "PUT", "/v1/profiles/short", c, 400)
	do(t, a, "PUT", "/v1/profiles/"+profileA, bytes.Repeat([]byte(" "), spec.MaxBody+1), 413)
	do(t, a, "PUT", "/v1/profiles/profile-bbbbbbbbbbbb", c, 200)
	if r = do(t, a, "PUT", "/v1/profiles/profile-cccccccccccc", c, 409); errorCode(r) != "too_many_profiles" {
		t.Fatalf("profile limit: %s", r.Body)
	}

	// A station sees and touches only its own contributions
	do(t, b, "DELETE", "/v1/profiles/"+profileA, nil, 404)
	var me struct{ Profiles []map[string]any }
	_ = do(t, b, "GET", "/v1/me", nil, 200).JSON(&me)
	if len(me.Profiles) != 0 {
		t.Fatalf("station b sees: %+v", me.Profiles)
	}
	_ = do(t, a, "GET", "/v1/me", nil, 200).JSON(&me)
	if len(me.Profiles) != 2 {
		t.Fatalf("station a sees: %+v", me.Profiles)
	}
	do(t, a, "DELETE", "/v1/profiles/"+profileA, nil, 204)
	do(t, a, "DELETE", "/v1/profiles/"+profileA, nil, 404)
}

func contributionWith(t *testing.T, coef float64) map[string]any {
	c := example(t, "contribution.json")
	click := c["kinds"].(map[string]any)["click"].(map[string]any)
	cs := make([]any, 11)
	for i := range cs {
		cs[i] = coef
	}
	click["coef"] = cs
	delete(c["kinds"].(map[string]any), "tick")
	return c
}

func TestModelsAndReports(t *testing.T) {
	e := newEnv(t, nil)
	stations := []*client.Station{e.active(), e.active(), e.active()}
	for i, s := range stations {
		do(t, s, "PUT", "/v1/profiles/"+profileA, contributionWith(t, 1+0.01*float64(i)), 200)
	}
	if _, err := e.hub.Combine(e.now, false); err != nil {
		t.Fatal(err)
	}
	s := stations[0]
	q := "/v1/models?feature_version=1&detector_version=1&chain_class=turntable_interface.mc"
	r := do(t, s, "GET", q, nil, 200)
	var ms struct{ Published, Candidate map[string]any }
	_ = r.JSON(&ms)
	if ms.Published != nil || ms.Candidate == nil || ms.Candidate["chain_class"] != spec.ClassAll {
		t.Fatalf("models: %s", r.Body)
	}
	payload, err := s.VerifyModel(ms.Candidate["jws"].(string))
	if err != nil {
		t.Fatal(err)
	}
	var gm spec.GlobalModel
	if _, err := spec.Validate(spec.DocGlobalModel, payload, &gm); err != nil || gm.Kinds["click"].Profiles != 3 {
		t.Fatalf("model payload: %v %s", err, payload)
	}
	tag := r.Header.Get("ETag")
	if r2, _ := s.Do("GET", q, nil, http.Header{"If-None-Match": {tag}}); r2.Status != http.StatusNotModified {
		t.Fatalf("unchanged models must answer 304: %d", r2.Status)
	}
	do(t, s, "GET", "/v1/models?feature_version=1", nil, 400)

	v := int64(ms.Candidate["version"].(float64))
	report := example(t, "check-report.json")
	delete(report["kinds"].(map[string]any), "tick")
	path := func(profile string) string { return fmt.Sprintf("/v1/profiles/%s/checks/%d", profile, v) }
	do(t, stations[0], "PUT", path(profileA), report, 200)
	do(t, stations[0], "PUT", path("profile-other-aaaaaa"), report, 200)
	r = do(t, stations[1], "PUT", path(profileA), report, 200)
	var res struct {
		Verdict     string
		ModelStatus string `json:"model_status"`
	}
	_ = r.JSON(&res)
	if res.Verdict != store.NotWorse || res.ModelStatus != store.Published {
		t.Fatalf("third report from a second station publishes: %s", r.Body)
	}
	_ = do(t, s, "GET", q, nil, 200).JSON(&ms)
	if ms.Published == nil || ms.Candidate != nil {
		t.Fatalf("after publishing: %+v", ms)
	}

	withTick := example(t, "check-report.json")
	if r = do(t, s, "PUT", path(profileA), withTick, 422); !strings.Contains(string(r.Body), "tick") {
		t.Fatalf("a kind the model lacks: %s", r.Body)
	}
	do(t, s, "PUT", "/v1/profiles/"+profileA+"/checks/999", report, 404)
	if _, err := e.hub.Withdraw(v, "", e.now); err != nil {
		t.Fatal(err)
	}
	do(t, s, "PUT", path(profileA), report, 409)
	var m map[string]any
	_ = do(t, s, "GET", fmt.Sprintf("/v1/models/%d", v), nil, 200).JSON(&m)
	if m["status"] != store.Withdrawn || m["jws"] != nil || m["reason"] != "withdrawn by the operator" {
		t.Fatalf("withdrawn model: %+v", m)
	}
}

func TestLeaveAndRevoke(t *testing.T) {
	e := newEnv(t, nil)
	a, b := e.active(), e.active()
	do(t, a, "PUT", "/v1/profiles/"+profileA, example(t, "contribution.json"), 200)
	do(t, a, "DELETE", "/v1/me", nil, 204)
	do(t, a, "GET", "/v1/me", nil, 401)
	if cs, _ := e.store.ActiveContributions(); len(cs) != 0 {
		t.Fatalf("leaving must delete contributions: %+v", cs)
	}
	if _, err := e.store.SetStatus(b.Fingerprint(), store.Revoked, e.now); err != nil {
		t.Fatal(err)
	}
	if r := do(t, b, "GET", "/v1/me", nil, 403); errorCode(r) != "revoked" {
		t.Fatalf("revoked: %s", r.Body)
	}
	code := "invitation-code"
	if r, _ := b.Join(&code); r.Status != 403 || errorCode(r) != "revoked" {
		t.Fatalf("a revoked key cannot join again: %d %s", r.Status, r.Body)
	}
}

func TestRateLimits(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.StationRequestsPerMin, c.JoinsPerHour = 3, 2 })
	s := e.active()
	for i := 0; i < 3; i++ {
		do(t, s, "GET", "/v1/me", nil, 200)
	}
	if r := do(t, s, "GET", "/v1/me", nil, 429); r.Header.Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
	e.now = e.now.Add(time.Minute)
	do(t, s, "GET", "/v1/me", nil, 200)
	e.station().Join(nil)
	if r, _ := e.station().Join(nil); r.Status != 429 {
		t.Fatalf("third join from one address within an hour: %d", r.Status)
	}
}

func TestAdmin(t *testing.T) {
	e := newEnv(t, nil)
	if resp, _ := http.Get(e.http.URL + "/admin"); resp.StatusCode != 404 {
		t.Fatalf("without a token the overview does not exist: %d", resp.StatusCode)
	}
	e = newEnv(t, func(c *config.Config) { c.AdminToken = "secret-token-1234567890-abcdefghijkl" })
	s := e.active()
	get := func(set func(*http.Request)) (int, string) {
		req, _ := http.NewRequest("GET", e.http.URL+"/admin", nil)
		set(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := get(func(r *http.Request) { r.SetBasicAuth("x", "wrong") }); code != 401 {
		t.Fatalf("wrong token: %d", code)
	}
	code, page := get(func(r *http.Request) { r.SetBasicAuth("operator", "secret-token-1234567890-abcdefghijkl") })
	// The template writes "+" of a base64 fingerprint as &#43;, which a browser shows as "+"
	if code != 200 || !strings.Contains(html.UnescapeString(page), s.Fingerprint()[:19]) {
		t.Fatalf("overview: %d", code)
	}
	if code, _ = get(func(r *http.Request) { r.Header.Set("Authorization", "Bearer secret-token-1234567890-abcdefghijkl") }); code != 200 {
		t.Fatalf("bearer: %d", code)
	}
}

func TestClientAddressBehindProxy(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.TrustedProxies = []string{"127.0.0.1"} })
	req, _ := http.NewRequest("GET", e.http.URL+"/v1/server", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.7, 203.0.113.9")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	log, _ := e.store.AccessLog(1)
	if log[0].Address != "203.0.113.9" {
		t.Fatalf("rightmost untrusted hop expected, got %q", log[0].Address)
	}
	e2 := newEnv(t, nil)
	req, _ = http.NewRequest("GET", e2.http.URL+"/v1/server", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if log, _ = e2.store.AccessLog(1); log[0].Address != "127.0.0.1" {
		t.Fatalf("an untrusted peer's X-Forwarded-For must be ignored, got %q", log[0].Address)
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newEnv(t, nil)
	for _, path := range []string{"/v1/server", "/healthz", "/nowhere"} {
		resp, err := http.Get(e.http.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		h := resp.Header
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" ||
			!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
			h.Get("Access-Control-Allow-Origin") != "" || h.Get("Server") != "" {
			t.Errorf("%s: headers %v", path, h)
		}
	}
}

func TestReportsCannotInventProfiles(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.MaxProfilesPerStation = 2 })
	stations := []*client.Station{e.active(), e.active(), e.active()}
	for i, s := range stations {
		do(t, s, "PUT", "/v1/profiles/"+profileA, contributionWith(t, 1+0.01*float64(i)), 200)
	}
	if _, err := e.hub.Combine(e.now, false); err != nil {
		t.Fatal(err)
	}
	var ms struct{ Candidate map[string]any }
	_ = do(t, stations[0], "GET", "/v1/models?feature_version=1&detector_version=1", nil, 200).JSON(&ms)
	v := int64(ms.Candidate["version"].(float64))
	report := example(t, "check-report.json")
	delete(report["kinds"].(map[string]any), "tick")
	path := func(profile string) string { return fmt.Sprintf("/v1/profiles/%s/checks/%d", profile, v) }
	do(t, stations[0], "PUT", path(profileA), report, 200)
	do(t, stations[0], "PUT", path("profile-invented-0001"), report, 200)
	if r := do(t, stations[0], "PUT", path("profile-invented-0002"), report, 409); errorCode(r) != "too_many_profiles" {
		t.Fatalf("a third profile: %s", r.Body)
	}
}

func TestAdminTokenGuessing(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.AdminToken = strings.Repeat("x", 40) })
	codes := map[int]int{}
	for i := 0; i < 12; i++ {
		req, _ := http.NewRequest("GET", e.http.URL+"/admin", nil)
		req.SetBasicAuth("x", fmt.Sprint("guess-", i))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		codes[resp.StatusCode]++
	}
	if codes[401] != 10 || codes[429] != 2 {
		t.Fatalf("after ten wrong tokens the address is held back: %v", codes)
	}
}
