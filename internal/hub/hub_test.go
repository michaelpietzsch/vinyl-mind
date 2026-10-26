package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/config"
	"github.com/michaelpietzsch/vinyl-mind/internal/keys"
	"github.com/michaelpietzsch/vinyl-mind/internal/store"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

var t0 = time.Unix(1_790_000_000, 0)

// reviewed is a moment after the review window: a candidate may be published from here on
var reviewed = t0.Add(49 * time.Hour)

type fixture struct {
	t        *testing.T
	st       *store.Store
	hub      *Hub
	pub      ed25519.PublicKey
	cfg      config.Config
	ids      map[string]int64
	trust    float64
	operator bool // new stations get their trust from the operator (anchors)
	now      time.Time
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
	// Stations in these tests report the moment they join; the minimum age gets a test of its own
	cfg.Publication.MinVoteAgeDays = 0
	if _, err := st.AddInvitation("invitation", 1000, nil, false, t0); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, st: st, hub: New(cfg, st, priv, nil), pub: pub, cfg: cfg, ids: map[string]int64{},
		trust: 1, operator: true, now: t0}
}

// station joins (if new); by default as one of the operator's, so that tests of the rules are not about trust
func (f *fixture) station(name string) int64 {
	if id, ok := f.ids[name]; ok {
		return id
	}
	code := "invitation"
	s, err := f.st.Join("fp-"+name, "pk-"+name, &code, true, f.trust, nil, 0, t0)
	if err != nil {
		f.t.Fatal(err)
	}
	if f.operator {
		if err := f.st.SetOperatorTrust(s.ID, f.trust, true); err != nil {
			f.t.Fatal(err)
		}
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
	if _, err := f.st.PutContribution(f.station(station), profile, c, 16, f.now); err != nil {
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

func (f *fixture) trustOf(name string) float64 {
	s, _ := f.st.Station("fp-" + name)
	return s.Trust
}

func outcome(out []Outcome, det, class string) *Outcome {
	for i := range out {
		if out[i].Group.DetectorVersion == det && out[i].Group.ChainClass == class {
			return &out[i]
		}
	}
	return nil
}

// report sends a check report at f.now and reviews the model.
func report(f *fixture, station, profile string, version int64, global float64) {
	f.t.Helper()
	r := spec.CheckReport{Kinds: map[string]spec.KindCheck{"click": {LocalErr: 0.1, GlobalErr: global,
		MasterRecordings: 3}}}
	if err := f.st.PutReport(store.Report{StationID: f.station(station), ProfileID: profile, ModelVersion: version,
		Kinds: r.Kinds, Verdict: Verdict(r, f.cfg.Publication.Tolerance), ReceivedAt: f.now}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.hub.Review(version, f.now); err != nil {
		f.t.Fatal(err)
	}
}

func status(f *fixture, version int64) string {
	m, _ := f.st.Model(version)
	return m.Status
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
	var c Contributors
	if json.Unmarshal([]byte(m.Contributors), &c) != nil || len(c.Stations) != 3 {
		t.Fatalf("contributors: %q", m.Contributors)
	}
	if again := outcome(f.combine(t0), "1", spec.ClassAll); again.Version != 0 {
		t.Fatal("unchanged contributions must not make a new model")
	}
	f.contribute("c", "p1", "1", "usb_turntable.mm", 1.3)
	if early := outcome(f.combine(t0.Add(time.Hour)), "1", spec.ClassAll); early.Version != 0 ||
		!strings.Contains(early.Skipped, "under review") {
		t.Fatalf("a candidate under review is not replaced: %+v", early)
	}
	next := outcome(f.combine(reviewed), "1", spec.ClassAll)
	if next.Version <= o.Version {
		t.Fatalf("after the review window a changed contribution makes a new candidate: %+v", next)
	}
	if old, _ := f.st.Model(o.Version); old.Status != store.Withdrawn || old.Reason != "superseded" {
		t.Fatalf("old candidate: %+v", old)
	}
}

// The review's D9: a changed contribution must change the candidate, whatever row it happens to occupy; and a
// change within tolerance keeps the candidate, so that reports on it keep their meaning (D5b).
func TestResultHashSeesEveryChange(t *testing.T) {
	f := newFixture(t)
	for _, s := range []string{"a", "b", "c"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	v1 := outcome(f.combine(t0), "1", spec.ClassAll).Version
	f.contribute("c", "p1", "1", "usb_turntable.mm", 1.5) // the newest row: SQLite would reuse its id
	v2 := outcome(f.combine(reviewed), "1", spec.ClassAll).Version
	if v2 <= v1 {
		t.Fatal("the changed newest contribution must make a new candidate")
	}
	f.contribute("c", "p1", "1", "usb_turntable.mm", 1.501)
	if o := outcome(f.combine(reviewed.Add(49*time.Hour)), "1", spec.ClassAll); o.Version != 0 ||
		!strings.Contains(o.Skipped, "within tolerance") {
		t.Fatalf("a nudge within tolerance keeps the candidate: %+v", o)
	}
	if status(f, v2) != store.Candidate {
		t.Fatal("the candidate under review stays")
	}
}

// The review's D5b: one station with two profiles, changing one every hour, used to replace every candidate.
func TestChurnMakesOneCandidatePerWindow(t *testing.T) {
	f := newFixture(t)
	for _, s := range []string{"a", "b", "c"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	f.operator = false
	f.trust = 0.25
	versions := map[int64]bool{}
	for h := 0; h < 8; h++ {
		f.now = t0.Add(time.Duration(h) * time.Hour)
		f.contribute("x", fmt.Sprint("p", h%2), "1", "usb_turntable.mm", 1.5+float64(h))
		if o := outcome(f.combine(f.now), "1", spec.ClassAll); o.Version != 0 {
			versions[o.Version] = true
		}
	}
	if len(versions) != 1 {
		t.Fatalf("eight hourly changes made %d candidates, want 1", len(versions))
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

// The review's D8: a class of newcomers alone gets no model of its own either.
func TestClassModelNeedsAnAnchor(t *testing.T) {
	f := newFixture(t)
	f.contribute("anchor", "p1", "1", "turntable_interface.mc", 1)
	f.operator = false
	f.trust = 0.25
	for i := 0; i < 6; i++ {
		f.contribute(fmt.Sprint("s", i), "p1", "1", "usb_turntable.mm", 50)
	}
	out := f.combine(t0)
	if o := outcome(out, "1", "usb_turntable.mm"); o == nil || o.Version != 0 ||
		!strings.Contains(o.Skipped, "operator") {
		t.Fatalf("six newcomers in a class must not get a model: %+v", o)
	}
}

func TestTrust(t *testing.T) {
	f := newFixture(t)
	f.contribute("anchor", "p1", "1", "usb_turntable.mm", 1)
	f.operator, f.trust = false, 0.5
	for _, s := range []string{"a", "b", "c", "d"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	f.contribute("e", "p1", "1", "usb_turntable.mm", 20) // far from the reference
	f.combine(t0)
	if !near(f.trustOf("a"), 0.6) || !near(f.trustOf("e"), 0.35) || !near(f.trustOf("anchor"), 1) {
		t.Fatalf("after one run: a %g, e %g, anchor %g", f.trustOf("a"), f.trustOf("e"), f.trustOf("anchor"))
	}
	// The review's D2: hourly runs without new input used to cost 0.15 each
	for h := 1; h <= 6; h++ {
		f.combine(t0.Add(time.Duration(h) * time.Hour))
	}
	if !near(f.trustOf("e"), 0.35) {
		t.Fatalf("a penalty falls once a day and only for a changed contribution: %g", f.trustOf("e"))
	}
	f.now = t0.Add(time.Hour)
	f.contribute("a", "p1", "1", "usb_turntable.mm", 1.01)
	f.contribute("e", "p1", "1", "usb_turntable.mm", 21)
	f.combine(f.now)
	if !near(f.trustOf("a"), 0.6) || !near(f.trustOf("e"), 0.35) {
		t.Fatalf("trust changes once per day only: a %g, e %g", f.trustOf("a"), f.trustOf("e"))
	}
	f.now = t0.Add(25 * time.Hour)
	f.contribute("a", "p1", "1", "usb_turntable.mm", 1.02)
	f.contribute("e", "p1", "1", "usb_turntable.mm", 22)
	f.combine(f.now)
	if !near(f.trustOf("a"), 0.7) || !near(f.trustOf("e"), 0.2) {
		t.Fatalf("next day: a %g, e %g", f.trustOf("a"), f.trustOf("e"))
	}
	// The review's D1: automatic trust never reaches the full level
	for day := 2; day <= 10; day++ {
		f.now = t0.Add(time.Duration(day)*24*time.Hour + time.Hour)
		f.contribute("a", "p1", "1", "usb_turntable.mm", 1+0.001*float64(day))
		f.combine(f.now)
	}
	if !near(f.trustOf("a"), f.cfg.Aggregation.TrustAutoMax) {
		t.Fatalf("automatic trust stops at %g, got %g", f.cfg.Aggregation.TrustAutoMax, f.trustOf("a"))
	}
	if s, _ := f.st.Station("fp-a"); s.TrustOperator {
		t.Fatal("automatic trust is not the operator's")
	}
}

// The review's D3 and S11: an anchor is never demoted by the others, however many and however far they are.
func TestAnchorIsNeverDemoted(t *testing.T) {
	f := newFixture(t)
	f.contribute("pi", "p1", "1", "usb_turntable.mm", 1)
	f.operator, f.trust = false, f.cfg.Aggregation.TrustAutoMax
	for i := 0; i < 4; i++ {
		for p := 0; p < 16; p++ {
			f.contribute(fmt.Sprint("s", i), fmt.Sprint("p", p), "1", "usb_turntable.mm", 50)
		}
	}
	o := outcome(f.combine(t0), "1", spec.ClassAll)
	if !near(f.trustOf("pi"), 1) {
		t.Fatalf("the anchor lost trust: %g", f.trustOf("pi"))
	}
	if c := o.Kinds["click"].Coef[1]; c > 1.3 {
		t.Fatalf("four stations with sixteen profiles each carried the model to %g", c)
	}
	for i := 0; i < 4; i++ {
		if f.trustOf(fmt.Sprint("s", i)) >= f.cfg.Aggregation.TrustAutoMax {
			t.Fatalf("a station far from the reference kept its trust")
		}
	}
	events, _ := f.st.Events(100)
	lowered := 0
	for _, e := range events {
		if e.Action == "trust_lowered" {
			lowered++
		}
	}
	if lowered != 4 {
		t.Fatalf("%d trust_lowered events, want 4", lowered)
	}
}

func TestPublishAndWithdraw(t *testing.T) {
	f := newFixture(t)
	for _, s := range []string{"a", "b", "c"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	v := outcome(f.combine(t0), "1", spec.ClassAll).Version
	f.now = reviewed
	report(f, "a", "p1", v, 0.09)
	report(f, "a", "p2", v, 0.105) // within the tolerance: not worse
	report(f, "a", "p3", v, 0.08)
	if status(f, v) != store.Candidate {
		t.Fatal("three reports from one station, and a contributor at that, are not enough")
	}
	report(f, "v2", "p1", v, 0.1)
	report(f, "v1", "p1", v, 0.2)
	if status(f, v) != store.Candidate {
		t.Fatal("one for, one against is a tie, which waits")
	}
	report(f, "v3", "p1", v, 0.1)
	if status(f, v) != store.Published {
		t.Fatalf("two of three voting anchors not worse: %s", status(f, v))
	}
	report(f, "d", "p1", v, 0.3)
	report(f, "e", "p1", v, 0.3)
	if m, _ := f.st.Model(v); m.Status != store.Withdrawn || !strings.HasPrefix(m.Reason, "worse for 3 of") {
		t.Fatalf("most anchors worse: %+v", m)
	}
}

// Anchors against and nobody for withdraw a candidate as soon as enough of them have reported; a contributor's
// good word does not count as a report either, so it cannot fill the quorum for its own candidate.
func TestAnchorsAgainstWithdraw(t *testing.T) {
	f := newFixture(t)
	for _, s := range []string{"a", "b", "c"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	v := outcome(f.combine(t0), "1", spec.ClassAll).Version
	f.now = reviewed
	report(f, "a", "p1", v, 0.1)
	report(f, "a", "p2", v, 0.1)
	report(f, "v1", "p1", v, 0.3)
	report(f, "v1", "p2", v, 0.3)
	if status(f, v) != store.Candidate {
		t.Fatalf("one anchor against is below min_report_stations: %s", status(f, v))
	}
	report(f, "v2", "p1", v, 0.3)
	if status(f, v) != store.Withdrawn {
		t.Fatalf("two anchors against, nobody for: %s", status(f, v))
	}
}

// The review's D4: a candidate is published no earlier than the review window, so that the first three reports
// cannot decide it before anyone else has looked; ReviewDue decides it then without a further report.
func TestReviewWindow(t *testing.T) {
	f := newFixture(t)
	for _, s := range []string{"a", "b", "c"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	v := outcome(f.combine(t0), "1", spec.ClassAll).Version
	f.now = t0.Add(time.Minute)
	for _, s := range []string{"v1", "v2", "v3"} {
		report(f, s, "p1", v, 0.1)
	}
	if status(f, v) != store.Candidate {
		t.Fatal("three anchors not worse, but the window has not passed")
	}
	if err := f.hub.ReviewDue(t0.Add(47 * time.Hour)); err != nil || status(f, v) != store.Candidate {
		t.Fatalf("before the window: %v %s", err, status(f, v))
	}
	if err := f.hub.ReviewDue(reviewed); err != nil || status(f, v) != store.Published {
		t.Fatalf("after the window: %v %s", err, status(f, v))
	}
}

// The review's D4: contributors cannot publish their own candidate, nor can stations of their accounts.
func TestContributorsCannotPublishThemselves(t *testing.T) {
	f := newFixture(t)
	for _, s := range []string{"a", "b", "c"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	v := outcome(f.combine(t0), "1", spec.ClassAll).Version
	f.now = reviewed
	report(f, "a", "p1", v, 0.1)
	report(f, "b", "p1", v, 0.1)
	report(f, "c", "p1", v, 0.1)
	tally, _ := f.hub.Tally(v, f.now)
	if status(f, v) != store.Candidate || tally.AnchorNotWorse != 0 || tally.Reports != 0 {
		t.Fatalf("contributors' good word must not count: %s %+v", status(f, v), tally)
	}
	// Contributors can still vote against their own candidate
	report(f, "a", "p2", v, 0.5)
	report(f, "b", "p2", v, 0.5)
	report(f, "c", "p2", v, 0.5)
	tally, _ = f.hub.Tally(v, f.now)
	if tally.AnchorWorse != 3 || status(f, v) != store.Withdrawn {
		t.Fatalf("a contributor's word against counts: %+v %s", tally, status(f, v))
	}
}

// The review's D4: withdrawing the published model does not bring an older one back; that is the operator's.
func TestNoRollbackOnWithdrawal(t *testing.T) {
	f := newFixture(t)
	for _, s := range []string{"a", "b", "c"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	g := store.Group{FeatureVersion: 1, DetectorVersion: "1", ChainClass: spec.ClassAll}
	v1 := outcome(f.combine(t0), "1", spec.ClassAll).Version
	if _, err := f.hub.Publish(v1, t0); err != nil {
		t.Fatal(err)
	}
	f.now = reviewed
	f.contribute("c", "p1", "1", "usb_turntable.mm", 1.5)
	v2 := outcome(f.combine(reviewed), "1", spec.ClassAll).Version
	if _, err := f.hub.Publish(v2, reviewed); err != nil {
		t.Fatal(err)
	}
	if p, _ := f.st.Published(g); p == nil || p.Version != v2 {
		t.Fatalf("published: %+v", p)
	}
	for _, s := range []string{"v1", "v2", "v3"} {
		report(f, s, "p1", v2, 0.5)
	}
	if status(f, v2) != store.Withdrawn {
		t.Fatal("three anchors found it worse")
	}
	if p, _ := f.st.Published(g); p != nil {
		t.Fatalf("withdrawal must not fall back to %d", p.Version)
	}
	if _, err := f.hub.Publish(v1, reviewed); err != nil {
		t.Fatal(err)
	}
	if p, _ := f.st.Published(g); p == nil || p.Version != v1 {
		t.Fatal("the operator can publish the older model again")
	}
}

func TestOneStationOneVote(t *testing.T) {
	f := newFixture(t)
	for _, s := range []string{"a", "b", "c"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	v := outcome(f.combine(t0), "1", spec.ClassAll).Version
	f.now = reviewed
	// One station invents ten profiles that all find the model worse
	for i := 0; i < 10; i++ {
		report(f, "x", fmt.Sprint("fake-", i), v, 0.5)
	}
	report(f, "y", "p1", v, 0.1)
	report(f, "z", "p1", v, 0.1)
	tally, _ := f.hub.Tally(v, f.now)
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
	f.now = reviewed
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
	f.contribute("anchor", "p1", "1", "usb_turntable.mm", 1)
	f.operator, f.trust = false, 0.5
	for _, s := range []string{"a", "b", "c"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	out, err := f.hub.Combine(t0, true)
	if err != nil || outcome(out, "1", spec.ClassAll).Skipped != "dry run" {
		t.Fatalf("dry run: %+v %v", out, err)
	}
	ms, _ := f.st.Models(nil, "")
	if len(ms) != 0 || f.trustOf("a") != 0.5 {
		t.Fatal("a dry run must not store models or change trust")
	}
	b, _ := json.Marshal(out[0].Kinds["click"].Coef)
	if len(b) < 10 {
		t.Fatal("a dry run still shows the combination")
	}
}

func TestVotesNeedAgeAndOnlyAnchorsDecide(t *testing.T) {
	f := newFixture(t)
	for _, s := range []string{"a", "b", "c"} {
		f.contribute(s, "p1", "1", "usb_turntable.mm", 1)
	}
	v := outcome(f.combine(t0), "1", spec.ClassAll).Version
	cfg := config.Default()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	h := New(cfg, f.st, key, nil)
	put := func(station, profile string, global float64) {
		r := spec.CheckReport{Kinds: map[string]spec.KindCheck{"click": {LocalErr: 0.1, GlobalErr: global,
			MasterRecordings: 3}}}
		if err := f.st.PutReport(store.Report{StationID: f.station(station), ProfileID: profile, ModelVersion: v,
			Kinds: r.Kinds, Verdict: Verdict(r, cfg.Publication.Tolerance), ReceivedAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	// One anchor finds the model fine, three new stations say it is worse
	put("v", "p1", 0.09)
	f.operator, f.trust = false, cfg.Aggregation.TrustStart
	for _, s := range []string{"x", "y", "z"} {
		put(s, "p1", 0.4)
	}
	if err := h.Review(v, t0.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if status(f, v) != store.Candidate {
		t.Fatal("stations younger than min_vote_age_days must not decide")
	}
	if err := h.Review(v, t0.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	tally, _ := h.Tally(v, t0.Add(8*24*time.Hour))
	if status(f, v) != store.Published || tally.Worse != 3 || !near(tally.WorseWeight, 0.75) || tally.AnchorWorse != 0 {
		t.Fatalf("three new stations must not outweigh an anchor: %s %+v", status(f, v), tally)
	}
	// Five new stations neither: only anchors decide (the review's D4b)
	for _, s := range []string{"p", "q"} {
		put(s, "p1", 0.4)
	}
	if err := h.Review(v, t0.Add(8*24*time.Hour)); err != nil || status(f, v) != store.Published {
		t.Fatalf("five new stations withdrew the anchors' model: %v %s", err, status(f, v))
	}
}

// The review's D6: leaving and coming back resets nothing.
func TestLeaveAndRejoinKeepsTrust(t *testing.T) {
	f := newFixture(t)
	f.operator, f.trust = false, 0.05
	id := f.station("x")
	st, _ := f.st.Station("fp-x")
	if err := f.st.Leave(st, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	code := "invitation"
	again, err := f.st.Join("fp-x", "pk-x", &code, true, 0.25, nil, 0, t0.Add(2*time.Hour))
	if err != nil || !near(again.Trust, 0.05) || !again.JoinedAt.Equal(t0) {
		t.Fatalf("rejoined (was %d): %+v %v", id, again, err)
	}
	// Another key of the same account comes back with the account's lowest trust
	accounted := func(int64, bool) (string, error) { return "account-xxxxxxxxxxxxxxxxx", nil }
	first, _ := f.st.Join("fp-y", "pk-y", &code, true, 0.25, accounted, 0, t0)
	_ = f.st.PenalizeTrust(first.ID, 0.1, t0)
	first, _ = f.st.Station("fp-y")
	if err := f.st.Leave(first, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	other, err := f.st.Join("fp-z", "pk-z", &code, true, 0.25, accounted, 0, t0.Add(2*time.Hour))
	if err != nil || !near(other.Trust, 0.1) {
		t.Fatalf("the account's trust follows: %+v %v", other, err)
	}
	if n, _ := f.st.JoinsSince(t0.Add(-time.Hour)); n != 3 {
		t.Fatalf("stations that left still count as joins of the day: %d", n)
	}
}
