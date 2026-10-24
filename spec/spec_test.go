package spec

import (
	"bytes"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("v1", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFeatureVersion1(t *testing.T) {
	v, err := Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(v.Kinds, ",") != "click,tick,pop" || len(v.Features) != 10 || v.NCoef() != 11 {
		t.Fatalf("unexpected feature version 1: %+v", v)
	}
	for _, f := range v.Features {
		if f.Scale <= 0 {
			t.Errorf("%s: scale must be positive", f.Name)
		}
	}
	if _, err := Get(99); err == nil {
		t.Error("feature version 99 must be unknown")
	}
}

func TestValidExamples(t *testing.T) {
	var c Contribution
	if _, err := Validate(DocContribution, read(t, "contribution.json"), &c); err != nil {
		t.Fatal(err)
	}
	if c.Chain.Class() != "turntable_interface.mc" || len(c.Kinds["click"].Coef) != 11 {
		t.Errorf("decoded wrongly: %+v", c)
	}
	var r CheckReport
	if _, err := Validate(DocCheckReport, read(t, "check-report.json"), &r); err != nil {
		t.Fatal(err)
	}
	if !r.Adopted || r.Kinds["tick"].GlobalErr != 0.09 {
		t.Errorf("decoded wrongly: %+v", r)
	}
}

func TestInvalidExamplesAreRejected(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join("v1", "examples", "invalid", "*.json"))
	readme, _ := os.ReadFile(filepath.Join("v1", "examples", "invalid", "README.md"))
	if len(paths) == 0 {
		t.Fatal("no invalid examples")
	}
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			body, _ := os.ReadFile(p)
			var c Contribution
			_, err := Validate(DocContribution, body, &c)
			var se *Error
			if !errors.As(err, &se) {
				t.Fatalf("want a spec error, got %v", err)
			}
			if !bytes.Contains(readme, []byte(filepath.Base(p))) {
				t.Error("not documented in invalid/README.md")
			}
		})
	}
}

func TestErrorsNameThePlace(t *testing.T) {
	body := bytes.Replace(read(t, "contribution.json"), []byte(`"cartridge": "mc"`), []byte(`"cartridge": "xx"`), 1)
	_, err := Validate(DocContribution, body, &Contribution{})
	if err == nil || !strings.HasPrefix(err.Error(), "chain/cartridge:") {
		t.Fatalf("want an error at chain/cartridge, got %v", err)
	}
}

func TestStrictSyntax(t *testing.T) {
	cases := map[string]string{
		`{"feature_version": 1, "a": NaN}`:              "not valid JSON",
		`{"feature_version": 1, "a": 1, "a": 2}`:        "duplicate key",
		`{"feature_version": 1, "k": {"x": 1, "x": 1}}`: "k: duplicate key",
		"\xff\xfe":                     "not UTF-8",
		`{`:                            "not valid JSON",
		`{"feature_version": 1} {}`:    "data after the document",
		`[1, 2]`:                       "not a JSON object",
		`{"feature_version": true}`:    "feature_version",
		`{"feature_version": 1.5}`:     "feature_version",
		strings.Repeat(" ", MaxBody+1): "larger than",
	}
	for body, want := range cases {
		_, err := Validate(DocContribution, []byte(body), &Contribution{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%.40q: want %q, got %v", body, want, err)
		}
	}
}

func TestToSharedGivesTheSameProbabilities(t *testing.T) {
	v, _ := Get(1)
	rng := rand.New(rand.NewPCG(1, 2))
	center, scale := v.Center(), v.Scale()
	mu, sd, b := make([]float64, 10), make([]float64, 10), make([]float64, 11)
	for i := range mu {
		mu[i] = center[i] + rng.NormFloat64()*0.3
		sd[i] = scale[i] * (0.5 + 1.5*rng.Float64())
	}
	for i := range b {
		b[i] = rng.NormFloat64()
	}
	coef := ToShared(v, mu, sd, b)
	for n := 0; n < 200; n++ {
		local, shared := b[0], coef[0]
		for i := range mu {
			x := center[i] + rng.NormFloat64()*scale[i]
			local += b[i+1] * (x - mu[i]) / sd[i]
			shared += coef[i+1] * (x - center[i]) / scale[i]
		}
		if math.Abs(local-shared) > 1e-9 {
			t.Fatalf("logit differs: %g vs %g", local, shared)
		}
	}
}
