package binding

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/keys"
)

var now = time.Unix(1_790_000_000, 0)

const station = "SHA256:If4x36FUomFia/hUBG/SJxt77UtqvkWqWId+9H+XIbk"

func valid() Payload {
	return Payload{V: 1, InvitationID: 7, Station: station, Account: "AAAAAAAAAAAAAAAAAAAAAA",
		Iat: now.Unix(), Exp: now.Add(72 * time.Hour).Unix()}
}

func setup(t *testing.T) (ed25519.PrivateKey, map[string]ed25519.PublicKey) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	return priv, map[string]ed25519.PublicKey{keys.Fingerprint(pub): pub}
}

func TestVerify(t *testing.T) {
	priv, accepted := setup(t)
	tok, _ := Sign(priv, valid())
	p, err := Verify(tok, accepted, now, 5*time.Minute)
	if err != nil || p.InvitationID != 7 || p.Account != "AAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("valid binding: %+v %v", p, err)
	}
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	foreign, _ := Sign(otherPriv, valid())
	parts := strings.Split(tok, ".")
	tampered := valid()
	tampered.InvitationID = 8
	tb, _ := json.Marshal(tampered)
	withField := strings.TrimSuffix(string(mustJSON(valid())), "}") + `,"admin":true}`
	cases := map[string]string{
		"key not accepted":     foreign,
		"changed payload":      parts[0] + "." + keys.Encode(tb) + "." + parts[2],
		"two parts":            parts[0] + "." + parts[1],
		"unknown field":        sign(t, priv, []byte(withField)),
		"expired":              signP(t, priv, func(p *Payload) { p.Iat, p.Exp = now.Add(-80*time.Hour).Unix(), now.Add(-time.Hour).Unix() }),
		"from the future":      signP(t, priv, func(p *Payload) { p.Iat = now.Add(time.Hour).Unix() }),
		"valid too long":       signP(t, priv, func(p *Payload) { p.Exp = now.Add(8 * 24 * time.Hour).Unix() }),
		"wrong version":        signP(t, priv, func(p *Payload) { p.V = 2 }),
		"no invitation":        signP(t, priv, func(p *Payload) { p.InvitationID = 0 }),
		"station not a fp":     signP(t, priv, func(p *Payload) { p.Station = "station-1" }),
		"account with symbols": signP(t, priv, func(p *Payload) { p.Account = "acc'; DROP TABLE stations;--" }),
		"too long":             strings.Repeat("a", 3000),
		"garbage":              "%%%.@@@.***",
	}
	for name, tok := range cases {
		if _, err := Verify(tok, accepted, now, 5*time.Minute); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func mustJSON(p Payload) []byte {
	b, _ := json.Marshal(p)
	return b
}

func signP(t *testing.T, priv ed25519.PrivateKey, edit func(*Payload)) string {
	p := valid()
	edit(&p)
	tok, err := Sign(priv, p)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// sign signs raw payload bytes, to test payloads Sign would never produce
func sign(t *testing.T, priv ed25519.PrivateKey, payload []byte) string {
	h, _ := json.Marshal(header{Alg: "EdDSA", Typ: Type, Kid: keys.Fingerprint(priv.Public().(ed25519.PublicKey))})
	input := keys.Encode(h) + "." + keys.Encode(payload)
	return input + "." + keys.Encode(ed25519.Sign(priv, []byte(input)))
}

// FuzzVerify feeds arbitrary tokens: Verify must never panic and never accept something that is not a binding
// signed by the accepted key.
func FuzzVerify(f *testing.F) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	accepted := map[string]ed25519.PublicKey{keys.Fingerprint(pub): pub}
	tok, _ := Sign(priv, valid())
	f.Add(tok)
	f.Add("a.b.c")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		p, err := Verify(s, accepted, now, 5*time.Minute)
		if err == nil && s != tok {
			// Ed25519 signatures are not malleable here, so only the original token may pass
			t.Fatalf("accepted a token that was not signed: %q → %+v", s, p)
		}
	})
}
