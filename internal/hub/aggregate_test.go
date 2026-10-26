package hub

import (
	"math"
	"strings"
	"testing"

	"github.com/michaelpietzsch/vinyl-mind/internal/config"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestWaterfill(t *testing.T) {
	// Caps leave room: shares follow the weights, the capped station gives its excess to the others
	p := waterfill([]float64{100, 10, 10, 10, 10}, []float64{0.25, 0.25, 0.25, 0.25, 0.25})
	if !near(p[0], 0.25) || !near(p[1], 0.1875) || !near(p[0]+p[1]*4, 1) {
		t.Fatalf("capped shares: %v", p)
	}
	// Caps below 1 in total: shares follow the caps
	p = waterfill([]float64{1000, 1}, []float64{0.25, 0.0625})
	if !near(p[0], 0.8) || !near(p[1], 0.2) {
		t.Fatalf("shares by cap: %v", p)
	}
	// Nobody at the cap: plain proportions
	p = waterfill([]float64{1, 1, 2}, []float64{1, 1, 1})
	if !near(p[2], 0.5) {
		t.Fatalf("proportional shares: %v", p)
	}
}

func TestTrimmedMeanAndQuantile(t *testing.T) {
	vals := []float64{1, 2, 3, 4, 100}
	ws := []float64{1, 1, 1, 1, 1}
	if m := trimmedMean(vals, ws, 0.2); !near(m, 3) {
		t.Fatalf("trimmed mean %g", m)
	}
	if m := trimmedMean(vals, ws, 0); !near(m, 22) {
		t.Fatalf("untrimmed mean %g", m)
	}
	if q := quantile(vals, ws, 0.5); !near(q, 3) {
		t.Fatalf("median %g", q)
	}
	if q := quantile([]float64{1, 3}, []float64{1, 1}, 0.5); !near(q, 2) {
		t.Fatalf("median of two %g", q)
	}
	// A quarter of the weight at an extreme survives the cut in part ((0.25 − 0.2) / 0.6 of it): the residual the
	// band around the reference exists for
	if m := trimmedMean([]float64{1, 100}, []float64{3, 1}, 0.2); !near(m, 9.25) {
		t.Fatalf("weighted trimming %g", m)
	}
}

func kind(coef0 float64, nManual, nMaster int, errEst float64) spec.KindContribution {
	coef := make([]float64, 11)
	for i := range coef {
		coef[i] = coef0
	}
	return spec.KindContribution{Coef: coef, NReal: (nManual + nMaster) / 2, NFalse: (nManual + nMaster + 1) / 2,
		NManual: nManual, NMaster: nMaster, MasterRecordings: 4, CVErrEst: errEst, CVErrRaw: 0.5}
}

// anchors returns n honest stations the operator trusts, at coef0 + 0.05 i.
func anchors(n int, coef0 float64) []Input {
	var in []Input
	for i := int64(1); i <= int64(n); i++ {
		in = append(in, Input{StationID: i, ProfileID: "honest", Trust: 1, Anchor: true,
			Kind: kind(coef0+0.05*float64(i), 50, 2000, 0.06)})
	}
	return in
}

func TestCombineExclusionsAndMinimum(t *testing.T) {
	cfg := config.Default().Aggregation
	in := []Input{
		{StationID: 1, ProfileID: "a", Trust: 1, Anchor: true, Kind: kind(1, 0, 500, 0.05)},
		{StationID: 2, ProfileID: "b", Trust: 1, Kind: kind(1, 0, 500, 0.35)}, // self-check too poor
		{StationID: 3, ProfileID: "c", Trust: 1, Kind: kind(1, 0, 500, 0.6)},  // not better than raw (0.5)
		{StationID: 4, ProfileID: "d", Trust: 1, Kind: spec.KindContribution{Coef: []float64{1}}},
		{StationID: 1, ProfileID: "e", Trust: 1, Anchor: true, Kind: kind(1, 0, 500, 0.05)},
	}
	r := Combine(in, cfg, 11)
	if len(r.Excluded) != 3 || r.Coef != nil || r.Profiles != 2 || r.Stations != 1 {
		t.Fatalf("two profiles of one station must not make a model: %+v", r)
	}
	in = append(in, Input{StationID: 5, ProfileID: "f", Trust: 0.9, Kind: kind(2, 10, 0, 0.15)})
	r = Combine(in, cfg, 11)
	if r.Coef == nil || r.Profiles != 3 || r.Stations != 2 || r.Anchors != 1 || !near(r.Examples, 1050) {
		t.Fatalf("three profiles from two stations: %+v", r)
	}
	sum := 0.0
	for _, u := range r.Used {
		sum += u.Share
	}
	if !near(sum, 1) {
		t.Fatalf("shares add up to %g", sum)
	}
}

func TestNoAnchorNoModel(t *testing.T) {
	cfg := config.Default().Aggregation
	var in []Input
	for i := int64(1); i <= 8; i++ {
		in = append(in, Input{StationID: i, ProfileID: "p", Trust: cfg.TrustAutoMax, Kind: kind(1, 50, 2000, 0.06)})
	}
	if r := Combine(in, cfg, 11); r.Coef != nil || r.Reason == "" {
		t.Fatalf("eight stations without one the operator trusts must not make a model: %+v", r.Reason)
	}
}

func TestCombineResistsNewAttackers(t *testing.T) {
	cfg := config.Default().Aggregation
	in := anchors(5, 1)
	// Two new stations claim enormous evidence and a perfect self-check for a model pointing the other way
	for i := int64(6); i <= 7; i++ {
		in = append(in, Input{StationID: i, ProfileID: "bad", Trust: cfg.TrustStart,
			Kind: kind(-5, 1_000_000, 1_000_000, 0)})
	}
	for _, method := range []string{"trimmed_mean", "median"} {
		cfg.Method = method
		r := Combine(in, cfg, 11)
		if r.Coef[3] < 1.05 || r.Coef[3] > 1.25 {
			t.Errorf("%s: combination dragged to %g by two new stations", method, r.Coef[3])
		}
		for _, u := range r.Used {
			if u.StationID >= 6 && (u.Share > cfg.NewcomerShare/2+1e-9 || !u.Far || u.Clipped != 11) {
				t.Errorf("%s: new station share %g far %v clipped %d", method, u.Share, u.Far, u.Clipped)
			}
		}
	}
}

// The review's scenarios S6, S8 and S10: stations that earned the most automatic trust and then turn, alone or
// together, on all coefficients or on a few. Held in the band, they move no coefficient by more than a fraction of
// the band, and the ones that turned on several coefficients are far.
func TestTrustedColludersAreHeld(t *testing.T) {
	cfg := config.Default().Aggregation
	honest := anchors(1, 1) // the operator's one station
	for i := int64(2); i <= 9; i++ {
		honest = append(honest, Input{StationID: i, ProfileID: "p", Trust: cfg.TrustAutoMax,
			Kind: kind(1+0.05*float64(i%3), 50, 2000, 0.06)})
	}
	base := Combine(honest, cfg, 11)
	cases := map[string][]Input{}
	for n := 1; n <= 4; n++ {
		var bad []Input
		for i := int64(100); i < int64(100+n); i++ {
			bad = append(bad, Input{StationID: i, ProfileID: "p", Trust: cfg.TrustAutoMax, Kind: kind(50, 1_000_000, 1_000_000, 0)})
		}
		cases[string(rune('0'+n))+" colluders at +50"] = bad
	}
	one := Input{StationID: 100, ProfileID: "p", Trust: cfg.TrustAutoMax, Kind: kind(1, 1_000_000, 1_000_000, 0)}
	one.Kind.Coef = append([]float64(nil), one.Kind.Coef...)
	one.Kind.Coef[1] = 50
	cases["one coefficient at +50"] = []Input{one}
	five := Input{StationID: 100, ProfileID: "p", Trust: cfg.TrustAutoMax, Kind: kind(1, 1_000_000, 1_000_000, 0)}
	five.Kind.Coef = append([]float64(nil), five.Kind.Coef...)
	for j := 1; j <= 5; j++ {
		five.Kind.Coef[j] = 50
	}
	cases["five coefficients at +50"] = []Input{five}
	for name, bad := range cases {
		r := Combine(append(append([]Input(nil), honest...), bad...), cfg, 11)
		for j := range r.Coef {
			if d := math.Abs(r.Coef[j] - base.Coef[j]); d > 0.3 {
				t.Errorf("%s: coefficient %d moved by %.3f", name, j, d)
			}
		}
		for _, u := range r.Used {
			if u.StationID >= 100 && !u.Far && u.Clipped >= farCount {
				t.Errorf("%s: station %d clipped on %d coefficients but not far", name, u.StationID, u.Clipped)
			}
		}
	}
}

// An anchor that turns is held to the other anchors and reported as far; it is never demoted here, that is the
// operator's call, but it cannot carry the model away either.
func TestColludingAnchorIsHeld(t *testing.T) {
	cfg := config.Default().Aggregation
	in := anchors(3, 1)
	base := Combine(in, cfg, 11)
	in = append(in, Input{StationID: 4, ProfileID: "p", Trust: 1, Anchor: true, Kind: kind(50, 1_000_000, 1_000_000, 0)})
	r := Combine(in, cfg, 11)
	for j := range r.Coef {
		if d := math.Abs(r.Coef[j] - base.Coef[j]); d > 0.3 {
			t.Errorf("coefficient %d moved by %.3f", j, d)
		}
	}
	if u := r.Used[3]; !u.Far || u.Clipped != 11 {
		t.Fatalf("the turned anchor: far %v clipped %d", u.Far, u.Clipped)
	}
	if r.Used[0].Far || r.Used[0].Clipped != 0 {
		t.Fatal("an honest anchor is not far from the others")
	}
}

func TestHold(t *testing.T) {
	ref, band := make([]float64, 11), make([]float64, 11)
	for j := range band {
		band[j] = 0.75
	}
	known := make([]bool, 11)
	for j := range known {
		known[j] = true
	}
	u := Used{Input: Input{Kind: kind(0.4, 0, 500, 0.05)}, Coef: make([]float64, 11)}
	copy(u.Coef, u.Kind.Coef)
	hold(&u, ref, band, known)
	if u.Far || u.Clipped != 0 {
		t.Fatal("inside the band nothing happens")
	}
	u.Kind.Coef[2] = 5
	u.Coef[2] = 5
	hold(&u, ref, band, known)
	if u.Far || u.Clipped != 1 || !near(u.Coef[2], 0.75) {
		t.Fatalf("one outlier is pulled in and forgiven: far %v clipped %d coef %g", u.Far, u.Clipped, u.Coef[2])
	}
	u.Kind.Coef[3], u.Coef[3], u.Clipped = -5, -5, 0
	hold(&u, ref, band, known)
	if !u.Far || u.Clipped != 2 || !near(u.Coef[3], -0.75) {
		t.Fatalf("two outliers are a pattern: far %v clipped %d", u.Far, u.Clipped)
	}
	// A coefficient no anchor learned holds nobody
	known[3] = false
	u.Coef[3], u.Clipped = -5, 0
	hold(&u, ref, band, known)
	if u.Far || u.Clipped != 1 {
		t.Fatalf("unknown coefficient: far %v clipped %d", u.Far, u.Clipped)
	}
	known[3] = true
	// Weights a profile did not learn are the prior's: being outside there is pulled in but not held against it
	masked := Used{Input: Input{Kind: kind(0.4, 0, 500, 0.05)}, Coef: make([]float64, 11)}
	masked.Kind.Informative = make([]bool, 10)
	masked.Kind.Informative[8], masked.Kind.Informative[9] = true, true
	for j := 1; j <= 7; j++ {
		masked.Kind.Coef[j] = 5
	}
	copy(masked.Coef, masked.Kind.Coef)
	hold(&masked, ref, band, known)
	if masked.Far || masked.Clipped != 7 {
		t.Fatalf("masked positions: far %v clipped %d", masked.Far, masked.Clipped)
	}
}

// Most stations have no second opinion: their gc weights are only the prior's and must not drag the weight that
// the few stations with one learned. A weight that no anchor learned stays what the anchors sent (review B-7).
func TestUninformativeWeights(t *testing.T) {
	cfg := config.Default().Aggregation
	var in []Input
	for i := int64(1); i <= 8; i++ {
		k := kind(1, 50, 2000, 0.06)
		if i > 3 {
			k.Informative = []bool{true, true, true, false, false, true, true, true, true, true}
			k.Coef[4], k.Coef[5] = 0, 0
		} else {
			k.Coef[4], k.Coef[5] = 2+0.05*float64(i), -1
		}
		in = append(in, Input{StationID: i, ProfileID: "p", Trust: cfg.TrustAutoMax, Kind: k})
	}
	in[0].Anchor, in[0].Trust = true, 1 // two anchors with a second opinion: the majority of the three
	in[1].Anchor, in[1].Trust = true, 1
	in[3].Anchor, in[3].Trust = true, 1 // one without
	r := Combine(in, cfg, 11)
	if r.Coef[4] < 2 || r.Coef[4] > 2.2 || !near(r.Coef[5], -1) || r.Basis[4] != 3 || r.Basis[1] != 8 {
		t.Fatalf("learned weights %v, basis %v", r.Coef, r.Basis)
	}
	if !near(r.Coef[1], 1) {
		t.Fatalf("other weights changed: %v", r.Coef)
	}
	// A mask of the wrong length (the schema refuses it) leaves the profile out instead of breaking the combiner
	in[1].Kind.Informative = []bool{true}
	if r = Combine(in, cfg, 11); len(r.Excluded) != 1 {
		t.Fatalf("short mask: %+v", r.Excluded)
	}
	in[1].Kind.Informative = nil
	// No majority of the anchors learned the weight: one anchor and a newcomer with a second opinion cannot define
	// it, it stays the prior
	in[0].Kind.Informative = []bool{true, true, true, false, false, true, true, true, true, true}
	in[0].Kind.Coef[4], in[0].Kind.Coef[5] = 0, 0
	in[1].Kind.Coef[4], in[2].Kind.Coef[4] = 50, 50
	r = Combine(in, cfg, 11)
	if r.Basis[4] != 0 || !near(r.Coef[4], 0) || r.Disagree != 2 {
		t.Fatalf("a weight no majority of the anchors learned: %v, basis %v, disagree %d", r.Coef[4], r.Basis, r.Disagree)
	}
}

func TestManyNewStationsTogetherAreCapped(t *testing.T) {
	cfg := config.Default().Aggregation
	in := anchors(4, 1)
	// Twenty cheap new stations, each claiming far more examples than the cap allows, with sixteen profiles
	for i := int64(100); i < 120; i++ {
		for p := 0; p < 16; p++ {
			in = append(in, Input{StationID: i, ProfileID: string(rune('a' + p)), Trust: cfg.TrustStart,
				Kind: kind(-5, 1_000_000, 1_000_000, 0)})
		}
	}
	r := Combine(in, cfg, 11)
	newShare := 0.0
	perStation := map[int64]float64{}
	for _, u := range r.Used {
		if u.StationID >= 100 {
			newShare += u.Share
			perStation[u.StationID] += u.Evidence
		}
	}
	if !near(newShare, cfg.NewcomerShare) {
		t.Fatalf("new stations together got %g, want %g", newShare, cfg.NewcomerShare)
	}
	for id, e := range perStation {
		if e > 2*cfg.MaxEvidence+1e-9 {
			t.Fatalf("station %d claims %g examples across its profiles", id, e)
		}
	}
	if r.Coef[3] < 1.0 || r.Coef[3] > 1.25 {
		t.Fatalf("twenty new stations moved the combination to %g", r.Coef[3])
	}
}

func TestNewcomersShareEqually(t *testing.T) {
	cfg := config.Default().Aggregation
	in := anchors(1, 1)
	in = append(in,
		Input{StationID: 10, ProfileID: "p", Trust: 0.25, Kind: kind(1, 0, 20, 0.1)},
		Input{StationID: 11, ProfileID: "p", Trust: 0.9, Kind: kind(1, 1000, 100000, 0)},
		Input{StationID: 12, ProfileID: "p", Trust: 0.5, Kind: kind(1, 10, 500, 0.05)})
	r := Combine(in, cfg, 11)
	for _, u := range r.Used {
		want := 1 - cfg.NewcomerShare
		if !u.Anchor {
			want = cfg.NewcomerShare / 3
		}
		if !near(u.Share, want) {
			t.Fatalf("station %d: share %g, want %g", u.StationID, u.Share, want)
		}
	}
}

// The second review's B2-1: with two anchors there is no majority. Two that agree are a reference; two that
// disagree are no reference at all, and nobody is held to a place between them.
func TestTwoAnchors(t *testing.T) {
	cfg := config.Default().Aggregation
	in := anchors(2, 1)
	for i := int64(10); i < 16; i++ {
		in = append(in, Input{StationID: i, ProfileID: "p", Trust: cfg.TrustAutoMax, Kind: kind(1.1, 50, 2000, 0.06)})
	}
	r := Combine(in, cfg, 11)
	if r.Coef == nil || r.Disagree != 0 {
		t.Fatalf("two agreeing anchors make a model: %+v", r.Reason)
	}
	for _, u := range r.Used {
		if u.Far || u.Clipped != 0 {
			t.Fatalf("station %d held although everyone agrees: far %v clipped %d", u.StationID, u.Far, u.Clipped)
		}
	}
	in[1].Kind = kind(50, 1_000_000, 1_000_000, 0) // one of the two turns
	r = Combine(in, cfg, 11)
	if r.Coef != nil || r.Disagree != 11 || !strings.Contains(r.Reason, "no majority") {
		t.Fatalf("two disagreeing anchors must not make a model: coef %v reason %q disagree %d", r.Coef != nil,
			r.Reason, r.Disagree)
	}
	// With a published model, the disputed coefficients are kept there and the rest is combined
	published := make([]float64, 11)
	for j := range published {
		published[j] = 0.9
	}
	r = CombineWith(in, cfg, 11, published)
	if r.Coef == nil || r.Disagree != 11 || len(r.Kept) != 11 {
		t.Fatalf("kept at the published model: %+v", r)
	}
	for j, c := range r.Coef {
		if !near(c, 0.9) || r.Basis[j] != 0 {
			t.Fatalf("coefficient %d not kept: %v basis %v", j, r.Coef, r.Basis)
		}
	}
	// The third review's B3-3: a dispute on one coefficient does not move it to the midpoint either
	in[1].Kind = kind(1.1, 50, 2000, 0.06)
	in[1].Kind.Coef[1] = 50
	r = Combine(in, cfg, 11)
	if r.Coef != nil || r.Disagree != 1 {
		t.Fatalf("a single disputed coefficient without a published model: coef %v disagree %d", r.Coef != nil,
			r.Disagree)
	}
	r = CombineWith(in, cfg, 11, published)
	if r.Coef == nil || !near(r.Coef[1], 0.9) || r.Basis[1] != 0 || r.Coef[2] < 1 || r.Coef[2] > 1.2 ||
		r.Basis[2] == 0 || len(r.Kept) != 1 || r.Kept[0] != 1 {
		t.Fatalf("coefficient 1 kept, the rest combined: coef %v basis %v kept %v", r.Coef, r.Basis, r.Kept)
	}
	for _, u := range r.Used {
		if u.Anchor && (u.Far || u.Outside > 0) {
			t.Fatalf("an anchor is not far on a coefficient without a reference: %+v", u)
		}
	}
}

// The third review's B3-1: a coefficient that only two anchors learned is a second opinion, not a majority. It
// neither demotes the honest anchor that holds it, nor does the bad anchor define it; and once the bad one is
// demoted, the honest one alone is no majority of the anchors either: the coefficient stays the prior.
func TestSecondOpinionOfTwoAnchorsIsNoMajority(t *testing.T) {
	cfg := config.Default().Aggregation
	for _, n := range []int{3, 4, 5} {
		in := anchors(n-1, 1)
		for i := range in {
			if i == 0 {
				continue // anchor 1 has a second opinion: it learned gc and gc_missing
			}
			in[i].Kind.Informative = []bool{true, true, true, false, false, true, true, true, true, true}
			in[i].Kind.Coef[4], in[i].Kind.Coef[5] = 0, 0
		}
		in[0].Kind.Coef[4], in[0].Kind.Coef[5] = 2, -1
		bad := Input{StationID: 99, ProfileID: "p", Trust: 1, Anchor: true, Kind: kind(50, 1_000_000, 1_000_000, 0)}
		in = append(in, bad)
		for i := int64(10); i < 14; i++ {
			in = append(in, Input{StationID: i, ProfileID: "p", Trust: cfg.TrustAutoMax, Kind: kind(1.1, 50, 2000, 0.06)})
		}
		r := Combine(in, cfg, 11)
		if r.Coef == nil || r.Anchors != n-1 {
			t.Fatalf("%d anchors: %s, %d left", n, r.Reason, r.Anchors)
		}
		for _, u := range r.Used {
			switch {
			case u.StationID == 99 && !u.Demoted:
				t.Fatalf("%d anchors: the bad anchor stayed an anchor", n)
			case u.StationID < 10 && u.StationID != 99 && (u.Demoted || u.Far):
				t.Fatalf("%d anchors: honest anchor %d demoted %v far %v", n, u.StationID, u.Demoted, u.Far)
			}
		}
		// The bad anchor demoted, the honest one is the only anchor that learned gc: one of n−1 is no majority,
		// the coefficient stays the prior the other anchors sent
		if !near(r.Coef[4], 0) || r.Basis[4] != 0 || r.Coef[1] > 1.3 || len(r.Kept) != 2 {
			t.Fatalf("%d anchors: coefficients %v basis %v kept %v", n, r.Coef, r.Basis, r.Kept)
		}
	}
	// A bad anchor that is honest everywhere but on the second opinion is not far, and still defines nothing:
	// the coefficient is kept at the prior of the anchors that did not learn it
	in := anchors(3, 1)
	for i := 1; i < 3; i++ {
		in[i].Kind.Informative = []bool{true, true, true, false, false, true, true, true, true, true}
		in[i].Kind.Coef[4], in[i].Kind.Coef[5] = 0, 0
	}
	in[0].Kind.Coef[4], in[0].Kind.Coef[5] = 2, -1
	subtle := Input{StationID: 99, ProfileID: "p", Trust: 1, Anchor: true, Kind: kind(1.1, 1_000_000, 1_000_000, 0)}
	subtle.Kind.Coef[4], subtle.Kind.Coef[5] = 50, -50
	in = append(in, subtle)
	for i := int64(10); i < 14; i++ {
		in = append(in, Input{StationID: i, ProfileID: "p", Trust: cfg.TrustAutoMax, Kind: kind(1.1, 50, 2000, 0.06)})
	}
	r := Combine(in, cfg, 11)
	if r.Coef == nil || r.Anchors != 4 || r.Disagree != 2 || !near(r.Coef[4], 0) || !near(r.Coef[5], 0) ||
		r.Basis[4] != 0 || len(r.Kept) != 2 {
		t.Fatalf("a dispute between two on the second opinion: anchors %d disagree %d coef %v basis %v kept %v",
			r.Anchors, r.Disagree, r.Coef, r.Basis, r.Kept)
	}
	for _, u := range r.Used {
		if u.Demoted || (u.Anchor && u.Far) {
			t.Fatalf("nobody is far or demoted over a dispute between two: %+v", u)
		}
	}
}

// Two colluding anchors share a second opinion with three honest anchors among seven: the honest three are the
// majority of the five remaining anchors once the colluders are demoted, and their second opinion stands.
func TestColludersOnASecondOpinionDemoteNobody(t *testing.T) {
	cfg := config.Default().Aggregation
	in := anchors(5, 1)
	for i := range in {
		if i < 3 {
			in[i].Kind.Coef[4], in[i].Kind.Coef[5] = 2+0.05*float64(i), -1
			continue
		}
		in[i].Kind.Informative = []bool{true, true, true, false, false, true, true, true, true, true}
		in[i].Kind.Coef[4], in[i].Kind.Coef[5] = 0, 0
	}
	for i := int64(98); i < 100; i++ {
		in = append(in, Input{StationID: i, ProfileID: "p", Trust: 1, Anchor: true, Kind: kind(50, 1_000_000, 1_000_000, 0)})
	}
	for i := int64(10); i < 14; i++ {
		in = append(in, Input{StationID: i, ProfileID: "p", Trust: cfg.TrustAutoMax, Kind: kind(1.1, 50, 2000, 0.06)})
	}
	r := Combine(in, cfg, 11)
	if r.Coef == nil || r.Anchors != 5 {
		t.Fatalf("five honest anchors remain: %s, %d", r.Reason, r.Anchors)
	}
	for _, u := range r.Used {
		if u.StationID < 10 && (u.Demoted || u.Far) {
			t.Fatalf("honest anchor %d demoted %v far %v", u.StationID, u.Demoted, u.Far)
		}
	}
	if r.Coef[4] < 1.9 || r.Coef[4] > 2.2 || r.Coef[1] > 1.3 || r.Disagree != 0 {
		t.Fatalf("coefficients %v disagree %d", r.Coef, r.Disagree)
	}
}

// The fourth review's B4-1: colluders who learned a second opinion that few honest anchors learned. On the
// coefficients everyone learned they are far and demoted; honest anchors are never found far on the second
// opinion, and the colluders define it neither when they are the majority of its learners nor when they are
// honest everywhere else.
func TestMinorityColludersOnASecondOpinion(t *testing.T) {
	cfg := config.Default().Aggregation
	noGC := []bool{true, true, true, false, false, true, true, true, true, true}
	for _, c := range []struct{ n, b int }{{5, 2}, {6, 2}, {7, 3}, {9, 4}} {
		for _, subtle := range []bool{false, true} {
			in := anchors(c.n-c.b, 1)
			for i := range in {
				if i == 0 {
					in[i].Kind.Coef[4], in[i].Kind.Coef[5] = 2, -1 // the one honest second opinion
					continue
				}
				in[i].Kind.Informative = noGC
				in[i].Kind.Coef[4], in[i].Kind.Coef[5] = 0, 0
			}
			for b := 0; b < c.b; b++ {
				k := kind(50, 1_000_000, 1_000_000, 0)
				if subtle {
					k = kind(1.1, 1_000_000, 1_000_000, 0)
					k.Coef[4], k.Coef[5] = 50, -50
				}
				in = append(in, Input{StationID: int64(90 + b), ProfileID: "p", Trust: 1, Anchor: true, Kind: k})
			}
			for i := int64(10); i < 14; i++ {
				in = append(in, Input{StationID: i, ProfileID: "p", Trust: cfg.TrustAutoMax, Kind: kind(1.1, 50, 2000, 0.06)})
			}
			r := Combine(in, cfg, 11)
			if r.Coef == nil {
				t.Fatalf("N=%d B=%d subtle %v: %s", c.n, c.b, subtle, r.Reason)
			}
			demoted := 0
			for _, u := range r.Used {
				switch {
				case u.StationID >= 90 && u.Demoted:
					demoted++
				case u.StationID < 10 && (u.Demoted || u.Far):
					t.Fatalf("N=%d B=%d subtle %v: honest anchor %d demoted %v far %v", c.n, c.b, subtle, u.StationID,
						u.Demoted, u.Far)
				}
			}
			if !subtle && demoted != c.b {
				t.Fatalf("N=%d B=%d: %d of the colluders demoted", c.n, c.b, demoted)
			}
			if !near(r.Coef[4], 0) || !near(r.Coef[5], 0) || r.Basis[4] != 0 || r.Coef[1] > 1.3 {
				t.Fatalf("N=%d B=%d subtle %v: the colluders defined something: coef %v basis %v", c.n, c.b, subtle,
					r.Coef, r.Basis)
			}
		}
	}
}

// The fourth review's B4-6: two equal camps among the anchors, further apart than the band allows, have no
// majority: the coefficient has no reference and nobody is far on it, so the honest anchors keep their word.
func TestEqualCampsHaveNoReference(t *testing.T) {
	cfg := config.Default().Aggregation
	in := anchors(2, 1)
	for i := int64(98); i < 100; i++ {
		in = append(in, Input{StationID: i, ProfileID: "p", Trust: 1, Anchor: true, Kind: kind(1.1, 1_000_000, 1_000_000, 0)})
	}
	in[2].Kind.Coef[1], in[3].Kind.Coef[1] = 50, 50 // the two colluders agree on one coefficient
	for i := int64(10); i < 14; i++ {
		in = append(in, Input{StationID: i, ProfileID: "p", Trust: cfg.TrustAutoMax, Kind: kind(1.1, 50, 2000, 0.06)})
	}
	r := Combine(in, cfg, 11)
	if r.Coef != nil || r.Disagree != 1 {
		t.Fatalf("two camps, nothing published: coef %v disagree %d reason %q", r.Coef != nil, r.Disagree, r.Reason)
	}
	published := make([]float64, 11)
	for j := range published {
		published[j] = 0.9
	}
	r = CombineWith(in, cfg, 11, published)
	if r.Coef == nil || !near(r.Coef[1], 0.9) || r.Basis[1] != 0 || r.Coef[2] > 1.2 {
		t.Fatalf("the disputed coefficient is kept, the rest combined: %v basis %v", r.Coef, r.Basis)
	}
	for _, u := range r.Used {
		if u.Anchor && (u.Far || u.Demoted) {
			t.Fatalf("anchor %d far %v demoted %v in a split nobody can judge", u.StationID, u.Far, u.Demoted)
		}
	}
}

// The third review's B3-2: when the anchors found far are half of all anchors or more, nobody is demoted; two
// identical colluders among four would otherwise demote the two honest anchors, whose noise is the only spread.
// Two camps more than the band apart are no majority either: the coefficients are kept at the published model.
func TestTieAmongAnchorsDemotesNobody(t *testing.T) {
	cfg := config.Default().Aggregation
	in := anchors(2, 1) // 1.05 and 1.10
	for i := int64(98); i < 100; i++ {
		in = append(in, Input{StationID: i, ProfileID: "p", Trust: 1, Anchor: true, Kind: kind(2, 1_000_000, 1_000_000, 0)})
	}
	for i := int64(10); i < 16; i++ {
		in = append(in, Input{StationID: i, ProfileID: "p", Trust: cfg.TrustAutoMax, Kind: kind(1.1, 50, 2000, 0.06)})
	}
	published := make([]float64, 11)
	for j := range published {
		published[j] = 0.9
	}
	r := CombineWith(in, cfg, 11, published)
	if r.Coef == nil || r.Anchors != 4 || r.Disagree != 11 {
		t.Fatalf("a tie: %s, %d anchors, %d disputed", r.Reason, r.Anchors, r.Disagree)
	}
	for _, u := range r.Used {
		if u.Demoted || (u.Anchor && u.Far) {
			t.Fatalf("station %d demoted %v far %v in a tie", u.StationID, u.Demoted, u.Far)
		}
	}
	if !near(r.Coef[1], 0.9) {
		t.Fatalf("a tie keeps the published model: %v", r.Coef)
	}
	// Three against two is no tie: the two are demoted
	in = append(in, anchors(1, 1.15)[0])
	in[len(in)-1].StationID = 3
	r = Combine(in, cfg, 11)
	n := 0
	for _, u := range r.Used {
		if u.Demoted {
			n++
		}
	}
	if r.Anchors != 3 || n != 2 || r.Coef[1] > 1.3 {
		t.Fatalf("three honest against two colluders: anchors %d demoted %d coef %v", r.Anchors, n, r.Coef)
	}
}

// B2-1 with three and four anchors: one bad anchor is held by the median of all three; with four, the good ones
// are held to each other and the bad one to the three good ones, also on a coefficient only four learned.
func TestOneBadAnchorAmongFew(t *testing.T) {
	cfg := config.Default().Aggregation
	for _, n := range []int{3, 4} {
		in := anchors(n-1, 1)
		for i := int64(10); i < 16; i++ {
			in = append(in, Input{StationID: i, ProfileID: "p", Trust: cfg.TrustAutoMax, Kind: kind(1.1, 50, 2000, 0.06)})
		}
		base := Combine(in, cfg, 11)
		in = append(in, Input{StationID: 99, ProfileID: "p", Trust: 1, Anchor: true, Kind: kind(50, 1_000_000, 1_000_000, 0)})
		r := Combine(in, cfg, 11)
		if r.Coef == nil {
			t.Fatalf("%d anchors, one bad: %s", n, r.Reason)
		}
		for j := range r.Coef {
			if d := math.Abs(r.Coef[j] - base.Coef[j]); d > 0.3 {
				t.Errorf("%d anchors: coefficient %d moved by %.3f", n, j, d)
			}
		}
		for _, u := range r.Used {
			switch {
			case u.StationID == 99 && (!u.Far || u.Clipped != 11):
				t.Errorf("%d anchors: the bad anchor is not held: far %v clipped %d", n, u.Far, u.Clipped)
			case u.StationID != 99 && (u.Far || u.Clipped != 0):
				t.Errorf("%d anchors: honest station %d held: far %v clipped %d", n, u.StationID, u.Far, u.Clipped)
			}
		}
	}
	// A coefficient only some anchors learned: the bad one learned it with three good ones
	in := anchors(3, 1)
	for i := range in {
		in[i].Kind.Informative = []bool{true, true, true, true, true, true, true, true, true, true}
	}
	masked := Input{StationID: 20, ProfileID: "p", Trust: 1, Anchor: true, Kind: kind(1.1, 50, 2000, 0.06)}
	masked.Kind.Informative = []bool{true, true, true, false, false, true, true, true, true, true}
	in = append(in, masked, Input{StationID: 99, ProfileID: "p", Trust: 1, Anchor: true, Kind: kind(50, 1_000_000, 1_000_000, 0)})
	r := Combine(in, cfg, 11)
	if r.Coef[4] > 1.5 || r.Coef[5] > 1.5 {
		t.Fatalf("a bad anchor on a coefficient four learned: %v", r.Coef)
	}
}

// B2-10: a non-finite coefficient from the database must not break the combination.
func TestNonFiniteCoefficientsAreExcluded(t *testing.T) {
	cfg := config.Default().Aggregation
	in := anchors(3, 1)
	in[2].Kind.CVErrEst = math.NaN() // the third review's B3-7: not reachable through the API, but stored data is data
	if r := Combine(in, cfg, 11); len(r.Excluded) != 1 || r.Excluded[0].Reason != "not a number" {
		t.Fatalf("a NaN self-check error is excluded: %+v", r.Excluded)
	}
	in = anchors(3, 1)
	in[0].Kind.Coef[2] = math.NaN()
	r := Combine(in, cfg, 11)
	if len(r.Excluded) != 1 || r.Excluded[0].Reason != "not a number" {
		t.Fatalf("excluded: %+v", r.Excluded)
	}
	for _, c := range r.Coef {
		if math.IsNaN(c) || math.IsInf(c, 0) {
			t.Fatal("NaN leaked into the combination")
		}
	}
}

// An anchor that turned and alone learned a coefficient (the others have no second opinion) must not define it:
// far from the others on the shared coefficients, it is no anchor in this combination.
func TestTurnedAnchorDefinesNothing(t *testing.T) {
	cfg := config.Default().Aggregation
	in := anchors(2, 1)
	for i := range in {
		in[i].Kind.Informative = []bool{true, true, true, false, false, true, true, true, true, true}
		in[i].Kind.Coef[4], in[i].Kind.Coef[5] = 0, 0 // the prior's weights
	}
	bad := Input{StationID: 99, ProfileID: "p", Trust: 1, Anchor: true, Kind: kind(50, 1_000_000, 1_000_000, 0)}
	in = append(in, bad)
	for i := int64(10); i < 14; i++ {
		in = append(in, Input{StationID: i, ProfileID: "p", Trust: cfg.TrustAutoMax, Kind: kind(1.1, 50, 2000, 0.06)})
	}
	r := Combine(in, cfg, 11)
	if r.Coef == nil || r.Anchors != 2 {
		t.Fatalf("two good anchors remain: %+v anchors %d", r.Reason, r.Anchors)
	}
	if !near(r.Coef[4], 0) || !near(r.Coef[5], 0) || r.Basis[4] != 0 {
		t.Fatalf("the turned anchor defined the coefficients only it learned: %v basis %v", r.Coef, r.Basis)
	}
	for _, u := range r.Used {
		switch {
		case u.StationID == 99 && (u.Anchor || !u.Demoted || !u.Far):
			t.Fatalf("the turned anchor: anchor %v demoted %v far %v", u.Anchor, u.Demoted, u.Far)
		case u.StationID != 99 && (u.Far || u.Demoted):
			t.Fatalf("station %d: far %v demoted %v", u.StationID, u.Far, u.Demoted)
		}
	}
	if r.Coef[1] > 1.3 {
		t.Fatalf("the model followed the turned anchor: %v", r.Coef)
	}
}
