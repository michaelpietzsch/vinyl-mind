package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/config"
	"github.com/michaelpietzsch/vinyl-mind/internal/keys"
	"github.com/michaelpietzsch/vinyl-mind/internal/store"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

var t0 = time.Unix(1_790_000_000, 0)

type fixture struct {
	t     *testing.T
	st    *store.Store
	hub   *Hub
	pub   ed25519.PublicKey
	cfg   config.Config
	ids   map[string]int64
	trust float64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "vm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	cfg := config.Default()
	if _, err := st.AddInvitation("invitation", 1000, nil, t0); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, st: st, hub: New(cfg, st, priv, nil), pub: pub, cfg: cfg, ids: map[string]int64{},
		trust: 1}
}

// station joins (if new) with full trust, so that tests of the rules are not about trust
func (f *fixture) station(name string) int64 {
	if id, ok := f.ids[name]; ok {
		return id
	}
	code := "invitation"
	s, err := f.st.Join("fp-"+name, "pk-"+name, &code, true, f.trust, t0)
	if err != nil {
		f.t.Fatal(err)
	}
	f.ids[name] = s.ID
	return s.ID
}

func (f *fixture) contribute(station, profile, det, class string, coef0 float64) {
	f.t.Helper()
	c := &spec.Contribution{Spec: "vinyl-mind/contribution", FeatureVersion: 1, DetectorVersion: det,
		Kinds: map[string]spec.KindContribution{"click": kind(coef0, 20, 800, 0.05)}}
	c.Client.Name, c.Client.Version = "vinyl-check", "1.1.0"
	c.Chain.Source, c.Chain.Cartridge = class[:len(class)-3], class[len(class)-2:]
	if _, err := f.st.PutContribution(f.station(station), profile, c, 16, t0); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) combine(at time.Time) []Outcome {
	f.t.Helper()
	out, err := f.hub.Combine(at, false)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func outcome(out []Outcome, det, class string) *Outcome {
	for i := range out {
		if out[i].Group.DetectorVersion == det && out[i].Group.ChainClass == class {
			return &out[i]
		}
	}
	return nil
}

func TestCombineBuildsSignedCandidates(t *testing.T) {
	f := newFixture(t)
	f.contribute("a", "p1", "1", "turntable_interface.mc", 1.0)
	f.contribute("b", "p1", "1", "turntable_interface.mc", 1.1)
	if o := outcome(f.combine(t0), "1", spec.ClassAll); o.Version != 0 || o.Skipped == "" {
		t.Fatalf("two profiles must not make a model: %+v", o)
	}
	f.contribute("c", "p1", "1", "usb_turntable.mm", 1.2)
	f.contribute("d", "p1", "2", "usb_turntable.mm", 9) // other detector: its own group
	out := f.combine(t0)
	o := outcome(out, "1", spec.ClassAll)
	if o == nil || o.Version == 0 {
		t.Fatalf("no candidate: %+v", out)
	}
	if o2 := outcome(out, "2", spec.ClassAll); o2 == nil || o2.Version != 0 {
		t.Fatalf("detector versions must not mix: %+v", o2)
	}
	m, _ := f.st.Model(o.Version)
	payload, err := keys.JWSVerify(f.pub, m.JWS)
	if err != nil || string(payload) != m.Payload {
		t.Fatalf("signature: %v", err)
	}
	var gm spec.GlobalModel
	if _, err := spec.Validate(spec.DocGlobalModel, payload, &gm); err != nil {
		t.Fatal(err)
	}
	if gm.Version != o.Version || gm.Kinds["click"].Profiles != 3 || !near(gm.Kinds["click"].Coef[5], 1.1) {
		t.Fatalf("model content: %+v", gm)
	}
	if again := outcome(f.combine(t0), "1", spec.ClassAll); again.Version != 0 {
		t.Fatal("unchanged contributions must not make a new model")
	}
	f.contribute("c", "p1", "1", "usb_turntable.mm", 1.3)
	next := outcome(f.combine(t0), "1", spec.ClassAll)
	if next.Version <= o.Version {
		t.Fatal("a changed contribution must make a new candidate")
	}
	if old, _ := f.st.Model(o.Version); old.Status != store.Withdrawn || old.Reason != "superseded" {
		t.Fatalf("old candidate: %+v", old)
	}
}

func TestChainClassModels(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 5; i++ {
		f.contribute(fmt.Sprint("s", i), "p1", "1", "turntable_interface.mc", 1)
	}
	f.contribute("u", "p1", "1", "usb_turntable.mm", 2)
	out := f.combine(t0)
	if o := outcome(out, "1", "turntable_interface.mc"); o == nil || o.Version == 0 {
		t.Fatalf("a class with five profiles gets its own model: %+v", out)
	}
	if o := outcome(out, "1", "usb_turntable.mm"); o != nil {
		t.Fatalf("a class with one profile does not: %+v", o)
	}
}

func TestTrust(t *testing.T) {
	f := newFixture(t)
	f.trust = 0.5
	for _, s := range []string{"a", "b", "c", "d"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	f.contribute("e", "p1", "1", "usb_turntable.mm", 20) // far from the others
	f.combine(t0)
	trust := func(name string) float64 {
		s, _ := f.st.Station("fp-" + name)
		return s.Trust
	}
	if !near(trust("a"), 0.6) || !near(trust("e"), 0.35) {
		t.Fatalf("after one run: a %g, e %g", trust("a"), trust("e"))
	}
	f.contribute("a", "p1", "1", "usb_turntable.mm", 1.01)
	f.combine(t0.Add(time.Hour))
	if !near(trust("a"), 0.6) {
		t.Fatalf("trust may grow once per day only: %g", trust("a"))
	}
	f.contribute("a", "p1", "1", "usb_turntable.mm", 1.02)
	f.combine(t0.Add(25 * time.Hour))
	if !near(trust("a"), 0.7) {
		t.Fatalf("next day: %g", trust("a"))
	}
}

func report(f *fixture, station, profile string, version int64, global float64) {
	f.t.Helper()
	r := spec.CheckReport{Kinds: map[string]spec.KindCheck{"click": {LocalErr: 0.1, GlobalErr: global,
		MasterRecordings: 3}}}
	if err := f.st.PutReport(store.Report{StationID: f.station(station), ProfileID: profile, ModelVersion: version,
		Kinds: r.Kinds, Verdict: Verdict(r, f.cfg.Publication.Tolerance), ReceivedAt: t0}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.hub.Review(version, t0); err != nil {
		f.t.Fatal(err)
	}
}

func status(f *fixture, version int64) string {
	m, _ := f.st.Model(version)
	return m.Status
}

func TestPublishAndWithdraw(t *testing.T) {
	f := newFixture(t)
	for _, s := range []string{"a", "b", "c"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	v := outcome(f.combine(t0), "1", spec.ClassAll).Version
	report(f, "a", "p1", v, 0.09)
	report(f, "a", "p2", v, 0.105) // within the tolerance: not worse
	report(f, "a", "p3", v, 0.08)
	if status(f, v) != store.Candidate {
		t.Fatal("three reports from one station are not enough")
	}
	report(f, "b", "p1", v, 0.2)
	if status(f, v) != store.Candidate {
		t.Fatal("one station for, one against is a tie, which waits")
	}
	report(f, "c", "p1", v, 0.1)
	if status(f, v) != store.Published {
		t.Fatalf("two of three stations not worse: %s", status(f, v))
	}
	report(f, "d", "p1", v, 0.3)
	report(f, "e", "p1", v, 0.3)
	if m, _ := f.st.Model(v); m.Status != store.Withdrawn || m.Reason != "worse for 3 of 5 reporting stations" {
		t.Fatalf("most stations worse: %+v", m)
	}
}

func TestOneStationOneVote(t *testing.T) {
	f := newFixture(t)
	for _, s := range []string{"a", "b", "c"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	v := outcome(f.combine(t0), "1", spec.ClassAll).Version
	// One station invents ten profiles that all find the model worse
	for i := 0; i < 10; i++ {
		report(f, "x", fmt.Sprint("fake-", i), v, 0.5)
	}
	report(f, "a", "p1", v, 0.1)
	report(f, "b", "p1", v, 0.1)
	tally, _ := f.hub.Tally(v)
	if status(f, v) != store.Published || tally.Reports != 12 || tally.Worse != 1 || tally.NotWorse != 2 {
		t.Fatalf("ten reports of one station must count once: %s %+v", status(f, v), tally)
	}
}

func TestPendingReportsDoNotCount(t *testing.T) {
	f := newFixture(t)
	for _, s := range []string{"a", "b", "c"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	v := outcome(f.combine(t0), "1", spec.ClassAll).Version
	for i, s := range []string{"x", "y", "z"} {
		f.station(s)
		_, _ = f.st.SetStatus("fp-"+s, store.Pending, t0)
		report(f, s, fmt.Sprint("p", i), v, 0.5)
	}
	if status(f, v) != store.Candidate {
		t.Fatal("reports of stations that are not active must not decide")
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.trust = 0.5
	for _, s := range []string{"a", "b", "c"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	out, err := f.hub.Combine(t0, true)
	if err != nil || outcome(out, "1", spec.ClassAll).Skipped != "dry run" {
		t.Fatalf("dry run: %+v %v", out, err)
	}
	ms, _ := f.st.Models(nil, "")
	s, _ := f.st.Station("fp-a")
	if len(ms) != 0 || s.Trust != 0.5 {
		t.Fatal("a dry run must not store models or change trust")
	}
	b, _ := json.Marshal(out[0].Kinds["click"].Coef)
	if len(b) < 10 {
		t.Fatal("a dry run still shows the combination")
	}
}
