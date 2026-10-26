// Package binding checks the signed binding with which an account service ties an invitation to one station key
// (docs/connect.md). Sign is the reference for account services and serves the tests.
package binding

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/keys"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

// Type is the "typ" of a binding.
const Type = "vinyl-mind-binding+json"

// MaxLifetime is the longest a binding may be valid; it only has to outlast a join refused for a while.
const MaxLifetime = 7 * 24 * time.Hour

// ErrInvalid is any binding that does not pass; the message says why, for the operator's log.
var ErrInvalid = errors.New("binding invalid")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

var (
	accountRE     = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
	fingerprintRE = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
)

// Payload is what a binding says.
type Payload struct {
	V            int    `json:"v"`
	InvitationID int64  `json:"invitation_id"`
	Station      string `json:"station"`
	Account      string `json:"account"`
	Iat          int64  `json:"iat"`
	Exp          int64  `json:"exp"`
	// Aud names the hub the binding is for (its canonical URL); optional. A hub with an audience configured
	// refuses a binding meant for another hub.
	Aud string `json:"aud,omitempty"`
}

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// Sign creates a binding, as an account service does.
func Sign(priv ed25519.PrivateKey, p Payload) (string, error) {
	h, _ := json.Marshal(header{Alg: "EdDSA", Typ: Type, Kid: keys.Fingerprint(priv.Public().(ed25519.PublicKey))})
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	input := keys.Encode(h) + "." + keys.Encode(b)
	return input + "." + keys.Encode(ed25519.Sign(priv, []byte(input))), nil
}

// Verify checks a binding against the accepted keys (by fingerprint), the hub's audience (its canonical URL, empty
// if none is configured) and the clock. It checks form, header, key and signature before it looks at the content,
// and refuses anything it does not expect, so that nothing in an unverified binding is ever used.
func Verify(token string, accepted map[string]ed25519.PublicKey, audience string, now time.Time,
	skew time.Duration) (*Payload, error) {
	if len(token) > 2048 {
		return nil, invalid("too long")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, invalid("not a JWS in compact serialisation")
	}
	rawHeader, err := keys.Decode(parts[0])
	if err != nil {
		return nil, invalid("header is not base64url")
	}
	var h header
	if err := spec.StrictJSON(rawHeader, &h); err != nil || h.Alg != "EdDSA" || h.Typ != Type {
		return nil, invalid("unexpected header")
	}
	pub, ok := accepted[h.Kid]
	if !ok {
		return nil, invalid("signed with a key this server does not accept")
	}
	sig, err := keys.Decode(parts[2])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, invalid("signature is not base64url")
	}
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, invalid("signature does not verify")
	}
	raw, err := keys.Decode(parts[1])
	if err != nil {
		return nil, invalid("payload is not base64url")
	}
	var p Payload
	if err := spec.StrictJSON(raw, &p); err != nil {
		return nil, invalid("payload: %v", err)
	}
	switch {
	case p.V != 1:
		return nil, invalid("version %d", p.V)
	case p.InvitationID < 1:
		return nil, invalid("no invitation")
	case !fingerprintRE.MatchString(p.Station):
		return nil, invalid("station is not a fingerprint")
	case !accountRE.MatchString(p.Account):
		return nil, invalid("account is not 16 to 64 characters of base64url")
	case time.Unix(p.Iat, 0).After(now.Add(skew)):
		return nil, invalid("issued in the future")
	case !time.Unix(p.Exp, 0).After(now):
		return nil, invalid("expired")
	case p.Exp-p.Iat > int64(MaxLifetime/time.Second) || p.Exp <= p.Iat:
		return nil, invalid("lifetime longer than %s", MaxLifetime)
	case p.Aud != "" && p.Aud != audience:
		return nil, invalid("meant for another hub")
	}
	return &p, nil
}
