package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/auth"
	"github.com/michaelpietzsch/vinyl-mind/internal/config"
	"github.com/michaelpietzsch/vinyl-mind/internal/keys"
	"github.com/michaelpietzsch/vinyl-mind/internal/store"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

func fuzzServer(f *testing.F) (*Server, *store.Store, ed25519.PrivateKey, time.Time) {
	cfg := config.Default()
	cfg.AddressRequestsPerMin, cfg.StationRequestsPerMin, cfg.JoinsPerHour, cfg.JoinsPerDay = 1e9, 1e9, 1e9, 1e9
	if err := cfg.Check(); err != nil {
		f.Fatal(err)
	}
	st, err := store.Open("sqlite://" + filepath.Join(f.TempDir(), "fuzz.sqlite"))
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { st.Close() })
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_790_000_000, 0)
	srv := New(cfg, st, key, nil, "fuzz", func() time.Time { return now }, nil)
	// A joined station, so that signed requests reach the handlers behind the signature check
	_, station, _ := ed25519.GenerateKey(rand.Reader)
	code := "fuzz-invitation"
	_, _ = st.AddInvitation(code, 1, nil, false, now)
	_, _ = st.Join(keys.Fingerprint(station.Public().(ed25519.PublicKey)),
		keys.Encode(station.Public().(ed25519.PublicKey)), &code, false, 0.25, nil, now)
	return srv, st, station, now
}

// FuzzHandler sends arbitrary requests to the whole interface: whatever comes in, the server answers without a
// server error and without crashing.
func FuzzHandler(f *testing.F) {
	srv, _, _, _ := fuzzServer(f)
	h := srv.Handler()
	f.Add("GET", "/v1/server", "", "", "")
	f.Add("PUT", "/v1/profiles/profile-aaaaaaaaaaaa", `{"spec":"vinyl-mind/contribution"}`, "SHA256:x", "1790000000")
	f.Add("POST", "/v1/join", `{"public_key":"x","invitation":"y","binding":"a.b.c"}`, "", "")
	f.Add("GET", "/v1/models?feature_version=1&detector_version=1'%20OR%201=1--", "", "", "")
	f.Fuzz(func(t *testing.T, method, target, body, key, ts string) {
		if !strings.HasPrefix(target, "/") || strings.ContainsAny(target, " \r\n\t") || method == "" ||
			strings.ContainsAny(method, " \r\n\t()<>@,;:\\\"/[]?={}") {
			return // not something net/http would deliver to a handler
		}
		req, err := http.NewRequest(method, "http://hub"+target, strings.NewReader(body))
		if err != nil {
			return
		}
		req.RequestURI = target
		req.RemoteAddr = "192.0.2.1:1234"
		req.Header.Set(auth.HKey, key)
		req.Header.Set(auth.HTimestamp, ts)
		req.Header.Set(auth.HNonce, "AAAAAAAAAAAAAAAAAAAAAA")
		req.Header.Set(auth.HSignature, "AAAA")
		req.Header.Set(auth.HClient, "vinyl-check/1.1.0")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code >= 500 {
			t.Fatalf("%s %s → %d: %s", method, target, rec.Code, rec.Body)
		}
	})
}

// FuzzSignedContribution sends arbitrary contributions with a valid signature, the way into the database: the
// server never fails, and anything it stores keeps the rules of the specification.
func FuzzSignedContribution(f *testing.F) {
	srv, st, station, now := fuzzServer(f)
	h := srv.Handler()
	if b, err := os.ReadFile(filepath.Join("..", "..", "spec", "v1", "examples", "contribution.json")); err == nil {
		f.Add(b, "profile-aaaaaaaaaaaa")
	}
	f.Add([]byte(`{}`), "profile-'; DROP TABLE stations;--")
	f.Fuzz(func(t *testing.T, body []byte, profile string) {
		target := "/v1/profiles/" + profile
		if strings.ContainsAny(profile, "/?# \r\n\t%") || len(profile) == 0 {
			return
		}
		req, err := http.NewRequest("PUT", "http://hub"+target, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.RequestURI = target
		req.RemoteAddr = "192.0.2.1:1234"
		for k, v := range auth.Sign(station, srv.Fingerprint(), "PUT", target, body, now) {
			req.Header[k] = v
		}
		req.Header.Set(auth.HClient, "vinyl-check/1.1.0")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code >= 500 {
			t.Fatalf("PUT %s → %d: %s", target, rec.Code, rec.Body)
		}
		if rec.Code == 200 {
			cs, err := st.ActiveContributions()
			if err != nil {
				t.Fatal(err)
			}
			v, _ := spec.Get(1)
			for _, c := range cs {
				for kind, k := range c.Kinds {
					if !v.HasKind(kind) || len(k.Coef) != v.NCoef() {
						t.Fatalf("stored a contribution that breaks the specification: %+v", c)
					}
				}
			}
		}
	})
}
