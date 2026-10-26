// Package keys handles Ed25519 keys, fingerprints and signed models (JWS compact serialisation with EdDSA,
// RFC 7515 / RFC 8037), with the encodings of spec/signing.md.
//
// JWSVerify is what a station does before it looks at a model; it lives here as the reference and for the tests.
package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/edwards25519"
)

// JWSType is the "typ" of a signed model.
const JWSType = "vinyl-mind-model+json"

// ErrSignature is any key, signature or token that cannot be decoded or does not verify.
var ErrSignature = errors.New("signature")

func sigErr(msg string) error { return fmt.Errorf("%w: %s", ErrSignature, msg) }

var b64url = base64.RawURLEncoding.Strict()

// Encode is base64url without padding.
func Encode(b []byte) string { return b64url.EncodeToString(b) }

// Decode is strict base64url without padding, so that one value has exactly one spelling.
func Decode(s string) ([]byte, error) {
	b, err := b64url.DecodeString(s)
	if err != nil {
		return nil, sigErr("not base64url without padding")
	}
	return b, nil
}

// Fingerprint is "SHA256:" and the SHA-256 of the raw public key in standard base64 without padding.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// ParsePublic decodes a public key in base64url.
func ParsePublic(s string) (ed25519.PublicKey, error) {
	b, err := Decode(s)
	if err != nil {
		return nil, err
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, sigErr("an Ed25519 public key has 32 bytes")
	}
	// A key must be a point of the prime-order subgroup: for the eight points of small order one fixed signature
	// verifies every message, so "signed" would prove nothing about who holds the key
	p, err := new(edwards25519.Point).SetBytes(b)
	if err != nil {
		return nil, sigErr("not a point on the curve")
	}
	if new(edwards25519.Point).MultByCofactor(p).Equal(edwards25519.NewIdentityPoint()) == 1 {
		return nil, sigErr("a point of small order is not a key")
	}
	return ed25519.PublicKey(b), nil
}

// LoadOrCreate returns the server's signing key, creating it on first start, readable only by its owner. Losing it
// means every station has to accept a new key; leaking it lets anyone sign models in the server's name. It belongs
// in the backup and nowhere else.
func LoadOrCreate(path string) (ed25519.PrivateKey, error) {
	if raw, err := os.ReadFile(path); err == nil {
		return parsePrivate(path, raw)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		f.Close()
		return nil, err
	}
	return priv, f.Close()
}

func parsePrivate(path string, raw []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM block", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an Ed25519 private key", path)
	}
	return priv, nil
}

type jwsHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// JWSSign signs a model payload with the server key.
func JWSSign(priv ed25519.PrivateKey, payload []byte) string {
	return JWSSignTyped(priv, JWSType, payload)
}

// StateType is the "typ" of the hub's signed statement about a model's status.
const StateType = "vinyl-mind-state+json"

// JWSSignTyped signs a payload under the given "typ": models, and the hub's statements about them.
func JWSSignTyped(priv ed25519.PrivateKey, typ string, payload []byte) string {
	h, _ := json.Marshal(jwsHeader{Alg: "EdDSA", Typ: typ, Kid: Fingerprint(priv.Public().(ed25519.PublicKey))})
	input := Encode(h) + "." + Encode(payload)
	return input + "." + Encode(ed25519.Sign(priv, []byte(input)))
}

// JWSVerify checks header, key ID and signature of a signed model and that the payload names the same server;
// it returns the payload. Nothing in the payload is trusted before this succeeds.
func JWSVerify(pub ed25519.PublicKey, token string) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, sigErr("not a JWS in compact serialisation")
	}
	rawHeader, err := Decode(parts[0])
	if err != nil {
		return nil, err
	}
	var h jwsHeader
	if json.Unmarshal(rawHeader, &h) != nil || h.Alg != "EdDSA" || h.Typ != JWSType {
		return nil, sigErr("unexpected JWS header")
	}
	if h.Kid != Fingerprint(pub) {
		return nil, sigErr("model signed with another key")
	}
	sig, err := Decode(parts[2])
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, sigErr("signature does not verify")
	}
	payload, err := Decode(parts[1])
	if err != nil {
		return nil, err
	}
	var p struct {
		Server string `json:"server"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Server != h.Kid {
		return nil, sigErr("payload names another server")
	}
	return payload, nil
}
