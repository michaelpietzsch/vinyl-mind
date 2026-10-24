// Package api is the HTTP interface: the station endpoints under /v1 (docs/api.md) and the read-only overview for
// the operator under /admin.
package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/auth"
	"github.com/michaelpietzsch/vinyl-mind/internal/config"
	"github.com/michaelpietzsch/vinyl-mind/internal/hub"
	"github.com/michaelpietzsch/vinyl-mind/internal/keys"
	"github.com/michaelpietzsch/vinyl-mind/internal/ratelimit"
	"github.com/michaelpietzsch/vinyl-mind/internal/store"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

// Server holds what the handlers need.
type Server struct {
	cfg        config.Config
	store      *store.Store
	key        ed25519.PrivateKey
	fp         string
	hub        *hub.Hub
	version    string
	now        func() time.Time
	log        *slog.Logger
	perStation *ratelimit.Limiter
	perAddress *ratelimit.Limiter
	joins      *ratelimit.Limiter
	adminFails *ratelimit.Limiter
}

// New returns the server. now may be nil (the wall clock); tests pass their own.
func New(cfg config.Config, st *store.Store, key ed25519.PrivateKey, h *hub.Hub, version string,
	now func() time.Time, log *slog.Logger) *Server {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		cfg: cfg, store: st, key: key, fp: keys.Fingerprint(key.Public().(ed25519.PublicKey)), hub: h,
		version: version, now: now, log: log,
		perStation: ratelimit.New(cfg.StationRequestsPerMin, time.Minute),
		perAddress: ratelimit.New(cfg.AddressRequestsPerMin, time.Minute),
		joins:      ratelimit.New(cfg.JoinsPerHour, time.Hour),
		adminFails: ratelimit.New(10, 10*time.Minute),
	}
}

// Fingerprint is the fingerprint of the server's signing key.
func (s *Server) Fingerprint() string { return s.fp }

// Handler returns the routes with access log and the per-address limit around them.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /v1/server", s.serverInfo)
	mux.HandleFunc("POST /v1/join", s.join)
	mux.Handle("GET /v1/me", s.signed(s.me, true))
	mux.Handle("DELETE /v1/me", s.signed(s.leave, true))
	mux.Handle("PUT /v1/profiles/{profile}", s.signed(s.putContribution, false))
	mux.Handle("DELETE /v1/profiles/{profile}", s.signed(s.deleteContribution, false))
	mux.Handle("PUT /v1/profiles/{profile}/checks/{version}", s.signed(s.putReport, false))
	mux.Handle("GET /v1/models", s.signed(s.models, false))
	mux.Handle("GET /v1/models/{version}", s.signed(s.model, false))
	mux.HandleFunc("GET /admin", s.admin)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	return s.accessLog(securityHeaders(s.limitAddress(mux)))
}

// securityHeaders sets what every answer carries. The API is not meant for browsers: nothing may be framed,
// sniffed, cached or loaded from elsewhere. HSTS belongs to the TLS-terminating proxy.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// ─── Plumbing ──────────────────────────────────────────────────────────────

type ctxKey struct{}

// reqInfo carries what the access log learns while the request runs.
type reqInfo struct {
	addr  netip.Addr
	keyFP string
}

func info(r *http.Request) *reqInfo {
	if ri, ok := r.Context().Value(ctxKey{}).(*reqInfo); ok {
		return ri
	}
	return &reqInfo{}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ri := &reqInfo{addr: s.clientAddr(r)}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), ctxKey{}, ri)))
		if r.URL.Path == "/healthz" {
			return // the proxy's health checks would drown everything else
		}
		addr := ""
		if ri.addr.IsValid() {
			addr = ri.addr.String()
		}
		// The query is left out: it says nothing about abuse and only adds data
		if err := s.store.LogAccess(store.AccessEntry{T: s.now(), Address: addr, KeyFP: ri.keyFP, Method: r.Method,
			Path: r.URL.Path, Status: sw.status}); err != nil {
			s.log.Error("access log", "err", err)
		}
	})
}

func (s *Server) limitAddress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && !s.perAddress.Allow(info(r).addr.String(), s.now()) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests from this address")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientAddr is the peer address, or the client address a trusted reverse proxy reports: the rightmost entry of
// X-Forwarded-For that is not itself a trusted proxy. Entries further left could be written by the client.
func (s *Server) clientAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	peer = peer.Unmap()
	if !s.cfg.TrustedProxy(peer) {
		return peer
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if a = a.Unmap(); !s.cfg.TrustedProxy(a) {
			return a
		}
	}
	return peer
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

func (s *Server) internal(w http.ResponseWriter, err error) {
	s.log.Error("request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}

// readBody reads at most spec.MaxBody bytes; a larger body is refused before it is parsed.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	// A body must announce its length: an oversized one is then refused before it is read, also when the request
	// did not come through a proxy that checks it. Every client sends small documents of known length.
	if r.ContentLength < 0 && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch) {
		writeError(w, http.StatusLengthRequired, "length_required", "a body needs a Content-Length")
		return nil, false
	}
	if r.ContentLength > spec.MaxBody {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "body larger than the server accepts")
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, spec.MaxBody))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "body larger than the server accepts")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", "body could not be read")
		return nil, false
	}
	return body, true
}

type stationHandler func(w http.ResponseWriter, r *http.Request, st *store.Station, body []byte)

// signed checks the request signature (spec/signing.md) before the handler runs. Pending stations may only ask
// for their status and leave.
func (s *Server) signed(h stationHandler, allowPending bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		now := s.now()
		sig, err := auth.Parse(r.Header, now, s.cfg.ClockSkew())
		if err != nil {
			authError(w, err)
			return
		}
		st, err := s.store.Station(sig.KeyFP)
		if err != nil {
			s.internal(w, err)
			return
		}
		if st == nil {
			writeError(w, http.StatusUnauthorized, "unknown_key", "this key has not joined")
			return
		}
		if err := sig.Verify(st.PublicKey, s.fp, r.Method, r.RequestURI, body); err != nil {
			authError(w, err)
			return
		}
		info(r).keyFP = st.KeyFP
		if !s.perStation.Allow(st.KeyFP, now) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests from this station")
			return
		}
		// Kept until the timestamp is stale anyway; after that the age check refuses a replay
		fresh, err := s.store.UseNonce(st.KeyFP, sig.Nonce, sig.Time.Add(s.cfg.ClockSkew()+time.Minute))
		if err != nil {
			s.internal(w, err)
			return
		}
		if !fresh {
			writeError(w, http.StatusUnauthorized, "replayed", "nonce already used")
			return
		}
		switch {
		case st.Status == store.Revoked:
			writeError(w, http.StatusForbidden, "revoked", "this station's key has been revoked")
			return
		case st.Status == store.Pending && !allowPending:
			writeError(w, http.StatusForbidden, "pending", "this station waits for approval by the operator")
			return
		}
		if err := s.store.Touch(st.ID, now); err != nil {
			s.internal(w, err)
			return
		}
		h(w, r, st, body)
	})
}

func authError(w http.ResponseWriter, err error) {
	var ae *auth.Error
	if errors.As(err, &ae) {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeError(w, http.StatusUnauthorized, "bad_signature", err.Error())
}

func specError(w http.ResponseWriter, err error) bool {
	var se *spec.Error
	if errors.As(err, &se) {
		writeError(w, http.StatusUnprocessableEntity, "invalid", se.Msg)
		return true
	}
	return false
}

func unix(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := t.Unix()
	return &v
}

// ─── Public endpoints ──────────────────────────────────────────────────────

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = io.WriteString(w, "ok\n")
}

func (s *Server) serverInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name": "vinyl-mind", "version": s.version,
		"public_key": keys.Encode(s.key.Public().(ed25519.PublicKey)), "fingerprint": s.fp,
		"join": s.cfg.JoinMode, "feature_versions": s.cfg.FeatureVersions,
		// Lets a station notice a wrong clock before its signed requests are refused as stale
		"time":   s.now().Unix(),
		"limits": map[string]int{"max_body": spec.MaxBody, "max_profiles": s.cfg.MaxProfilesPerStation},
	})
}
