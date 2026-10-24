package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/keys"
)

const serverFP = "SHA256:If4x36FUomFia/hUBG/SJxt77UtqvkWqWId+9H+XIbk"

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestSignAndVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pk := keys.Encode(pub)
	now := time.Unix(1_790_000_000, 0)
	body := []byte(`{"a":1}`)
	h := Sign(priv, serverFP, "put", "/v1/profiles/p?x=1", body, now)

	s, err := Parse(h, now.Add(10*time.Second), 300*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(pk, serverFP, "PUT", "/v1/profiles/p?x=1", body); err != nil {
		t.Fatalf("a correct request must verify: %v", err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	for name, c := range map[string]struct {
		pk, server, method, target string
		body                       []byte
		want                       string
	}{
		"other body":   {pk, serverFP, "PUT", "/v1/profiles/p?x=1", []byte(`{"a":2}`), "bad_signature"},
		"other method": {pk, serverFP, "DELETE", "/v1/profiles/p?x=1", body, "bad_signature"},
		"other path":   {pk, serverFP, "PUT", "/v1/profiles/q?x=1", body, "bad_signature"},
		"other query":  {pk, serverFP, "PUT", "/v1/profiles/p?x=2", body, "bad_signature"},
		"other server": {pk, "SHA256:" + "A" + serverFP[8:], "PUT", "/v1/profiles/p?x=1", body, "bad_signature"},
		"other key":    {keys.Encode(other), serverFP, "PUT", "/v1/profiles/p?x=1", body, "bad_signature"},
		"no key":       {"short", serverFP, "PUT", "/v1/profiles/p?x=1", body, "bad_key"},
	} {
		if err := s.Verify(c.pk, c.server, c.method, c.target, c.body); code(err) != c.want {
			t.Errorf("%s: want %s, got %v", name, c.want, err)
		}
	}
}

func TestParseRejects(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_790_000_000, 0)
	good := Sign(priv, serverFP, "GET", "/v1/me", nil, now)
	edit := func(k, v string) http.Header {
		h := good.Clone()
		if v == "" {
			h.Del(k)
		} else {
			h.Set(k, v)
		}
		return h
	}
	cases := map[string]struct {
		h    http.Header
		at   time.Time
		want string
	}{
		"unsigned":        {http.Header{}, now, "unsigned"},
		"missing nonce":   {edit(HNonce, ""), now, "unsigned"},
		"short nonce":     {edit(HNonce, "abc"), now, "malformed_signature"},
		"bad fingerprint": {edit(HKey, "SHA256:xyz"), now, "malformed_signature"},
		"timestamp text":  {edit(HTimestamp, "12ab"), now, "malformed_signature"},
		"leading zero":    {edit(HTimestamp, "0"+good.Get(HTimestamp)), now, "malformed_signature"},
		"bad signature":   {edit(HSignature, "!!"), now, "malformed_signature"},
		"too old":         {good, now.Add(301 * time.Second), "stale"},
		"from the future": {good, now.Add(-301 * time.Second), "stale"},
	}
	for name, c := range cases {
		if _, err := Parse(c.h, c.at, 300*time.Second); code(err) != c.want {
			t.Errorf("%s: want %s, got %v", name, c.want, err)
		}
	}
}
