// Package spec loads the shared specification (feature definitions and JSON schemas per feature version) and checks
// documents against it.
//
// The JSON files next to this source are the contract with the stations. They are embedded into the binary, so the
// server validates against exactly the files in this directory and never against a second description in code.
// What a JSON schema cannot express (counts that must add up, duplicate keys) is checked here.
package spec

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

//go:embed v*/*.json
var files embed.FS

// MaxBody is the largest document the server accepts. A contribution is a few hundred bytes; anything far larger is
// not one.
const MaxBody = 16 << 10

// Document names, also the file names of their schemas (<name>.schema.json).
const (
	DocContribution = "contribution"
	DocGlobalModel  = "global-model"
	DocCheckReport  = "check-report"
)

// Error is a document that does not meet the specification; the message names the place.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func fail(format string, a ...any) error { return &Error{Msg: fmt.Sprintf(format, a...)} }

// Feature is one input of the model with its fixed shared scaling.
type Feature struct {
	Name       string  `json:"name"`
	Definition string  `json:"definition"`
	Center     float64 `json:"center"`
	Scale      float64 `json:"scale"`
}

// Version is one feature version: the features in their order, the kinds and the schemas of its documents.
type Version struct {
	Number    int       `json:"feature_version"`
	Status    string    `json:"status"`
	Kinds     []string  `json:"kinds"`
	CoefBound float64   `json:"coef_bound"`
	Features  []Feature `json:"features"`
	schemas   map[string]*jsonschema.Schema
}

// NCoef is the number of coefficients per kind: the intercept and one weight per feature.
func (v *Version) NCoef() int { return len(v.Features) + 1 }

func (v *Version) Names() []string { return each(v.Features, func(f Feature) string { return f.Name }) }
func (v *Version) Center() []float64 {
	return each(v.Features, func(f Feature) float64 { return f.Center })
}
func (v *Version) Scale() []float64 {
	return each(v.Features, func(f Feature) float64 { return f.Scale })
}

func each[T any](fs []Feature, get func(Feature) T) []T {
	out := make([]T, len(fs))
	for i, f := range fs {
		out[i] = get(f)
	}
	return out
}

// HasKind reports whether the feature version defines the kind.
func (v *Version) HasKind(kind string) bool {
	for _, k := range v.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

var (
	loadOnce sync.Once
	versions map[int]*Version
	loadErr  error
)

// Versions returns all feature versions of the specification, keyed by number.
func Versions() (map[int]*Version, error) {
	loadOnce.Do(func() { versions, loadErr = load(files) })
	return versions, loadErr
}

// Numbers returns the known feature versions in ascending order.
func Numbers() []int {
	vs, _ := Versions()
	out := make([]int, 0, len(vs))
	for n := range vs {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// Get returns one feature version.
func Get(n int) (*Version, error) {
	vs, err := Versions()
	if err != nil {
		return nil, err
	}
	v, ok := vs[n]
	if !ok {
		return nil, fail("unknown feature_version %d", n)
	}
	return v, nil
}

func load(fsys fs.FS) (map[int]*Version, error) {
	dirs, err := fs.Glob(fsys, "v*")
	if err != nil {
		return nil, err
	}
	out := map[int]*Version{}
	for _, dir := range dirs {
		raw, err := fs.ReadFile(fsys, path.Join(dir, "features.json"))
		if err != nil {
			return nil, err
		}
		v := &Version{schemas: map[string]*jsonschema.Schema{}}
		if err := json.Unmarshal(raw, v); err != nil {
			return nil, fmt.Errorf("%s/features.json: %w", dir, err)
		}
		if dir != "v"+strconv.Itoa(v.Number) {
			return nil, fmt.Errorf("%s: directory does not match feature_version %d", dir, v.Number)
		}
		c := jsonschema.NewCompiler()
		c.DefaultDraft(jsonschema.Draft2020)
		for _, name := range []string{DocContribution, DocGlobalModel, DocCheckReport} {
			file := path.Join(dir, name+".schema.json")
			raw, err := fs.ReadFile(fsys, file)
			if err != nil {
				return nil, err
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", file, err)
			}
			// Compiled under a local name: the $id URLs point to the repository and must never be fetched
			url := "mem:///" + file
			if err := c.AddResource(url, doc); err != nil {
				return nil, fmt.Errorf("%s: %w", file, err)
			}
			if v.schemas[name], err = c.Compile(url); err != nil {
				return nil, fmt.Errorf("%s: %w", file, err)
			}
		}
		out[v.Number] = v
	}
	if len(out) == 0 {
		return nil, errors.New("no feature version in the specification")
	}
	return out, nil
}

// checkSyntax rejects what encoding/json would accept silently: invalid UTF-8 and duplicate keys. Two parsers may
// keep different copies of a duplicated key; refusing them removes the ambiguity.
func checkSyntax(body []byte) error {
	if len(body) > MaxBody {
		return fail("body larger than %d bytes", MaxBody)
	}
	if !utf8.Valid(body) {
		return fail("not valid JSON: not UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := walk(dec, ""); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fail("not valid JSON: data after the document")
	}
	return nil
}

func walk(dec *json.Decoder, at string) error {
	tok, err := dec.Token()
	if err != nil {
		return fail("not valid JSON: %v", err)
	}
	switch tok {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return fail("not valid JSON: %v", err)
			}
			key := k.(string)
			if seen[key] {
				return fail("%s: duplicate key %q", where(at), key)
			}
			seen[key] = true
			if err := walk(dec, at+"/"+key); err != nil {
				return err
			}
		}
		_, err = dec.Token()
	case json.Delim('['):
		for i := 0; dec.More(); i++ {
			if err := walk(dec, at+"/"+strconv.Itoa(i)); err != nil {
				return err
			}
		}
		_, err = dec.Token()
	}
	if err != nil {
		return fail("not valid JSON: %v", err)
	}
	return nil
}

func where(at string) string {
	if at == "" {
		return "document"
	}
	return strings.TrimPrefix(at, "/")
}

var printer = message.NewPrinter(language.English)

func schemaError(err error) error {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return fail("%v", err)
	}
	// The first leaf is the most specific complaint; the full tree would repeat it for every enclosing keyword
	for len(ve.Causes) > 0 {
		ve = ve.Causes[0]
	}
	return fail("%s: %s", where(strings.Join(append([]string{""}, ve.InstanceLocation...), "/")),
		ve.ErrorKind.LocalizedString(printer))
}

// Validate checks one document against the schema of its feature version, decodes it into out and applies the
// rules beyond the schema. It returns the feature version the document belongs to.
func Validate(document string, body []byte, out any) (*Version, error) {
	if err := checkSyntax(body); err != nil {
		return nil, err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return nil, fail("not valid JSON: %v", err)
	}
	obj, ok := inst.(map[string]any)
	if !ok {
		return nil, fail("document is not a JSON object")
	}
	num, ok := obj["feature_version"].(json.Number)
	if !ok {
		return nil, fail("feature_version missing or not an integer")
	}
	n, err := strconv.Atoi(num.String())
	if err != nil {
		return nil, fail("feature_version missing or not an integer")
	}
	v, err := Get(n)
	if err != nil {
		return nil, err
	}
	sch, ok := v.schemas[document]
	if !ok {
		return nil, fmt.Errorf("unknown document %q", document)
	}
	if err := sch.Validate(inst); err != nil {
		return nil, schemaError(err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, fail("%v", err)
	}
	if c, ok := out.(interface{ check(*Version) error }); ok {
		if err := c.check(v); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// ToShared converts a model standardised with its own mean and spread (as a station trains it) into the fixed
// shared scaling. Exact: both give the same probability for every event (see README.md). b holds the intercept and
// one weight per feature.
func ToShared(v *Version, mu, sd, b []float64) []float64 {
	center, scale := v.Center(), v.Scale()
	out := make([]float64, v.NCoef())
	out[0] = b[0]
	for i := range v.Features {
		w := b[i+1] / sd[i]
		out[0] += w * (center[i] - mu[i])
		out[i+1] = w * scale[i]
	}
	return out
}
