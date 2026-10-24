package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFingerprint(t *testing.T) {
	// Public key of test 1 in RFC 8032; the same vector is in spec/signing.md for station implementers
	raw, _ := hex.DecodeString("d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a")
	fp := Fingerprint(ed25519.PublicKey(raw))
	if fp != "SHA256:If4x36FUomFia/hUBG/SJxt77UtqvkWqWId+9H+XIbk" {
		t.Fatalf("fingerprint %s", fp)
	}
	if Encode(raw) != "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo" {
		t.Fatalf("base64url %s", Encode(raw))
	}
	pub, err := ParsePublic(Encode(raw))
	if err != nil || Fingerprint(pub) != fp {
		t.Fatalf("round trip through base64url: %v", err)
	}
	for _, bad := range []string{Encode(raw[:31]), Encode(raw) + "=", strings.ReplaceAll(Encode(raw), "_", "/"), "!"} {
		if _, err := ParsePublic(bad); !errors.Is(err, ErrSignature) {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestLoadOrCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "server-key.pem")
	k1, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v, want 0600", info.Mode().Perm())
	}
	k2, err := LoadOrCreate(path)
	if err != nil || !k1.Equal(k2) {
		t.Fatalf("second start must load the same key: %v", err)
	}
	_ = os.WriteFile(path, []byte("not a key"), 0o600)
	if _, err := LoadOrCreate(path); err == nil {
		t.Fatal("a broken key file must be an error, not a new key")
	}
}

func TestJWS(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	payload := []byte(`{"server":"` + Fingerprint(pub) + `","version":3}`)
	token := JWSSign(priv, payload)
	got, err := JWSVerify(pub, token)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("verify: %v", err)
	}
	parts := strings.Split(token, ".")
	tampered := parts[0] + "." + Encode([]byte(strings.Replace(string(payload), "3", "4", 1))) + "." + parts[2]
	cases := map[string]struct {
		pub   ed25519.PublicKey
		token string
	}{
		"tampered payload":             {pub, tampered},
		"other server key":             {otherPub, token},
		"not three parts":              {pub, parts[0] + "." + parts[1]},
		"payload names another server": {otherPub, JWSSign(otherPriv, payload)},
		"wrong algorithm": {pub, Encode([]byte(`{"alg":"none","typ":"`+JWSType+`","kid":"`+Fingerprint(pub)+
			`"}`)) + "." + parts[1] + "." + parts[2]},
	}
	for name, c := range cases {
		if _, err := JWSVerify(c.pub, c.token); !errors.Is(err, ErrSignature) {
			t.Errorf("%s: want a signature error, got %v", name, err)
		}
	}
}
