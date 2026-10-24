// Package client is a minimal station: it joins, signs its requests and verifies models as spec/signing.md
// describes. It serves the tests and shows a station implementer the whole exchange in one place.
package client

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/auth"
	"github.com/michaelpietzsch/vinyl-mind/internal/keys"
)

// Station talks to one server with one key.
type Station struct {
	Base      string // e.g. https://vinyl-mind.example.org
	Key       ed25519.PrivateKey
	ServerKey ed25519.PublicKey // accepted at the first connection; models are verified against it
	HTTP      *http.Client
	Now       func() time.Time
}

// ServerInfo is the answer of GET /v1/server.
type ServerInfo struct {
	PublicKey       string `json:"public_key"`
	Fingerprint     string `json:"fingerprint"`
	Join            string `json:"join"`
	FeatureVersions []int  `json:"feature_versions"`
	Time            int64  `json:"time"`
}

func (s *Station) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Station) http() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return http.DefaultClient
}

// Fingerprint is the station's own key fingerprint.
func (s *Station) Fingerprint() string { return keys.Fingerprint(s.Key.Public().(ed25519.PublicKey)) }

// Connect fetches the server key. A real station shows its fingerprint to the user and stores it; it only ever
// accepts a different key when the user says so.
func (s *Station) Connect() (*ServerInfo, error) {
	resp, err := s.http().Get(s.Base + "/v1/server")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var info ServerInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	pub, err := keys.ParsePublic(info.PublicKey)
	if err != nil {
		return nil, err
	}
	if keys.Fingerprint(pub) != info.Fingerprint {
		return nil, fmt.Errorf("server key does not match its fingerprint")
	}
	if s.ServerKey != nil && !s.ServerKey.Equal(pub) {
		return nil, fmt.Errorf("server key changed: %s", info.Fingerprint)
	}
	s.ServerKey = pub
	return &info, nil
}

// Response is a decoded answer.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON decodes the body.
func (r *Response) JSON(v any) error { return json.Unmarshal(r.Body, v) }

// Do sends a signed request. body may be nil, raw bytes or a value to encode as JSON.
func (s *Station) Do(method, target string, body any, header http.Header) (*Response, error) {
	var raw []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		raw = b
	default:
		var err error
		if raw, err = json.Marshal(b); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest(method, s.Base+target, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range auth.Sign(s.Key, keys.Fingerprint(s.ServerKey), method, target, raw, s.now()) {
		req.Header[k] = vs
	}
	resp, err := s.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: out}, err
}

// Join registers the station's key, with an invitation code or without (then it waits for approval).
func (s *Station) Join(invitation *string) (*Response, error) {
	body := map[string]any{"public_key": keys.Encode(s.Key.Public().(ed25519.PublicKey))}
	if invitation != nil {
		body["invitation"] = *invitation
	}
	return s.Do(http.MethodPost, "/v1/join", body, nil)
}

// VerifyModel checks a model's JWS against the accepted server key and returns its payload.
func (s *Station) VerifyModel(jws string) ([]byte, error) {
	return keys.JWSVerify(s.ServerKey, jws)
}
