package auth

import (
	"net/http"
	"testing"
	"time"
)

// FuzzParse feeds arbitrary signature headers: Parse must never panic, and Verify on whatever it accepts must not
// either.
func FuzzParse(f *testing.F) {
	f.Add("SHA256:If4x36FUomFia/hUBG/SJxt77UtqvkWqWId+9H+XIbk", "1790000000", "AAAAAAAAAAAAAAAAAAAAAA", "sig", "body")
	f.Fuzz(func(t *testing.T, fp, ts, nonce, sig, body string) {
		h := http.Header{}
		h.Set(HKey, fp)
		h.Set(HTimestamp, ts)
		h.Set(HNonce, nonce)
		h.Set(HSignature, sig)
		s, err := Parse(h, time.Unix(1_790_000_000, 0), 300*time.Second)
		if err == nil {
			_ = s.Verify("11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo", serverFP, "PUT", "/v1/x", []byte(body))
		}
	})
}
