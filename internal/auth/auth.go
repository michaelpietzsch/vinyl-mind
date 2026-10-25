// Package auth implements signed requests (spec/signing.md): the message both sides sign, signing on the station
// and checking on the server.
package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/keys"
)

const (
	Prefix     = "vinyl-mind-request-v1"
	HKey       = "Vinyl-Mind-Key"
	HTimestamp = "Vinyl-Mind-Timestamp"
	HNonce     = "Vinyl-Mind-Nonce"
	HSignature = "Vinyl-Mind-Signature"
	// HClient names the client software as "name/version". It is not signed: it serves compatibility, not trust.
	HClient = "Vinyl-Mind-Client"
)

var (
	fingerprintRE = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
	nonceRE       = regexp.MustCompile(`^[A-Za-z0-9_-]{22,64}$`)
)

// ValidFingerprint reports whether s has the form of a key fingerprint.
func ValidFingerprint(s string) bool { return fingerprintRE.MatchString(s) }

// Error is a rejected request: HTTP status, a stable code for clients and a message for people.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func unauthorized(code, msg string) *Error {
	return &Error{Status: http.StatusUnauthorized, Code: code, Message: msg}
}

// Message is what a station signs and the server verifies.
func Message(serverFP, method, target string, body []byte, timestamp, nonce, keyFP string) []byte {
	sum := sha256.Sum256(body)
	return []byte(strings.Join([]string{Prefix, serverFP, strings.ToUpper(method), target, hex.EncodeToString(sum[:]),
		timestamp, nonce, keyFP}, "\n"))
}

// Sign returns the headers for one request, as a station creates them.
func Sign(priv ed25519.PrivateKey, serverFP, method, target string, body []byte, now time.Time) http.Header {
	fp := keys.Fingerprint(priv.Public().(ed25519.PublicKey))
	ts := strconv.FormatInt(now.Unix(), 10)
	n := make([]byte, 16)
	_, _ = rand.Read(n)
	nonce := keys.Encode(n)
	h := http.Header{}
	h.Set(HKey, fp)
	h.Set(HTimestamp, ts)
	h.Set(HNonce, nonce)
	h.Set(HSignature, keys.Encode(ed25519.Sign(priv, Message(serverFP, method, target, body, ts, nonce, fp))))
	return h
}

// Signed holds the signature headers of one request, checked for form and age but not yet verified.
type Signed struct {
	KeyFP, Timestamp, Nonce string
	signature               []byte
	Time                    time.Time
}

// Parse checks that the signature headers are present, well-formed and recent.
func Parse(h http.Header, now time.Time, skew time.Duration) (*Signed, error) {
	fp, ts, nonce, sig := h.Get(HKey), h.Get(HTimestamp), h.Get(HNonce), h.Get(HSignature)
	if fp == "" || ts == "" || nonce == "" || sig == "" {
		return nil, unauthorized("unsigned", "request is not signed")
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if !fingerprintRE.MatchString(fp) || !nonceRE.MatchString(nonce) || err != nil || len(ts) > 12 ||
		strconv.FormatInt(sec, 10) != ts {
		return nil, unauthorized("malformed_signature", "signature headers are malformed")
	}
	t := time.Unix(sec, 0)
	if d := now.Sub(t); d > skew || d < -skew {
		// Also the answer to a replay after the server has forgotten the nonce
		return nil, unauthorized("stale", "timestamp too far off the server clock")
	}
	raw, err := keys.Decode(sig)
	if err != nil || len(raw) != ed25519.SignatureSize {
		return nil, unauthorized("malformed_signature", "signature is not a base64url Ed25519 signature")
	}
	return &Signed{KeyFP: fp, Timestamp: ts, Nonce: nonce, signature: raw, Time: t}, nil
}

// Verify checks the signature with the station's public key (base64url).
func (s *Signed) Verify(publicKey, serverFP, method, target string, body []byte) error {
	pub, err := keys.ParsePublic(publicKey)
	if err != nil {
		return &Error{Status: http.StatusBadRequest, Code: "bad_key", Message: "public key is not an Ed25519 key"}
	}
	if keys.Fingerprint(pub) != s.KeyFP {
		return unauthorized("bad_signature", "key header does not match the key")
	}
	if !ed25519.Verify(pub, Message(serverFP, method, target, body, s.Timestamp, s.Nonce, s.KeyFP), s.signature) {
		return unauthorized("bad_signature", "signature does not verify")
	}
	return nil
}
