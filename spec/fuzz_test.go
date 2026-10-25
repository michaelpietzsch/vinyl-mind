package spec

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// FuzzValidate feeds arbitrary bytes as each document: Validate must never panic, and whatever it accepts must keep
// the rules the schema promises, also after a round trip through encoding.
func FuzzValidate(f *testing.F) {
	for _, name := range []string{"contribution.json", "check-report.json"} {
		if b, err := os.ReadFile(filepath.Join("v1", "examples", name)); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte(`{"feature_version":1}`))
	f.Add([]byte(`{"feature_version":1e400}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		var c Contribution
		if v, err := Validate(DocContribution, body, &c); err == nil {
			for kind, k := range c.Kinds {
				if !v.HasKind(kind) || len(k.Coef) != v.NCoef() || k.NManual+k.NMaster != k.NReal+k.NFalse {
					t.Fatalf("accepted a contribution that breaks the rules: %s", body)
				}
				for _, x := range k.Coef {
					if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) > v.CoefBound {
						t.Fatalf("accepted coefficient %v", x)
					}
				}
			}
			again, _ := json.Marshal(c)
			if _, err := Validate(DocContribution, again, &Contribution{}); err != nil {
				t.Fatalf("accepted once, refused after a round trip: %v", err)
			}
		}
		var r CheckReport
		_, _ = Validate(DocCheckReport, body, &r)
		var m GlobalModel
		_, _ = Validate(DocGlobalModel, body, &m)
		var x map[string]any
		_ = StrictJSON(body, &x)
	})
}
