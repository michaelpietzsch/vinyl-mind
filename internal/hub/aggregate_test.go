package hub

import (
	"math"
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
	vals, ws := []float64{1, 2, 3, 100}, []float64{0.25, 0.25, 0.25, 0.25}
	if m := trimmedMean(vals, ws, 0); !near(m, 26.5) {
		t.Errorf("untrimmed mean %g", m)
	}
	if m := trimmedMean(vals, ws, 0.25); !near(m, 2.5) {
		t.Errorf("trimmed mean %g", m)
	}
	// Partial trimming at the boundary: 10 % off each end of four equal weights
	if m := trimmedMean(vals, ws, 0.1); !near(m, (0.15*1+0.25*2+0.25*3+0.15*100)/0.8) {
		t.Errorf("partially trimmed mean %g", m)
	}
	if q := quantile(vals, ws, 0.5); !near(q, 2.5) {
		t.Errorf("median of an even count %g", q)
	}
	if q := quantile([]float64{1, 2, 3}, []float64{1, 1, 1}, 0.5); !near(q, 2) {
		t.Errorf("median %g", q)
	}
	if q := quantile([]float64{1, 2, 3}, []float64{0.1, 0.1, 0.8}, 0.5); !near(q, 3) {
		t.Errorf("weighted median %g", q)
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

func TestCombineExclusionsAndMinimum(t *testing.T) {
	cfg := config.Default().Aggregation
	in := []Input{
		{StationID: 1, ProfileID: "a", Trust: 1, Kind: kind(1, 0, 500, 0.05)},
		{StationID: 2, ProfileID: "b", Trust: 1, Kind: kind(1, 0, 500, 0.35)}, // self-check too poor
		{StationID: 3, ProfileID: "c", Trust: 1, Kind: kind(1, 0, 500, 0.6)},  // not better than raw (0.5)
		{StationID: 4, ProfileID: "d", Trust: 1, Kind: spec.KindContribution{Coef: []float64{1}}},
		{StationID: 1, ProfileID: "e", Trust: 1, Kind: kind(1, 0, 500, 0.05)},
	}
	r := Combine(in, cfg, 11)
	if len(r.Excluded) != 3 || r.Coef != nil || r.Profiles != 2 || r.Stations != 1 {
		t.Fatalf("two profiles of one station must not make a model: %+v", r)
	}
	in = append(in, Input{StationID: 5, ProfileID: "f", Trust: 1, Kind: kind(2, 10, 0, 0.15)})
	r = Combine(in, cfg, 11)
	if r.Coef == nil || r.Profiles != 3 || r.Stations != 2 || !near(r.Examples, 1050) {
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

func TestCombineResistsNewAttackers(t *testing.T) {
	cfg := config.Default().Aggregation
	var in []Input
	for i := int64(1); i <= 5; i++ {
		in = append(in, Input{StationID: i, ProfileID: "honest", Trust: 1, Kind: kind(1+0.05*float64(i), 50, 2000, 0.06)})
	}
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
			if u.StationID >= 6 && u.Share > cfg.MaxStationShare*cfg.TrustStart+1e-9 {
				t.Errorf("%s: new station got share %g", method, u.Share)
			}
		}
	}
}

func TestFar(t *testing.T) {
	combined, spread := make([]float64, 11), make([]float64, 11)
	for i := range spread {
		spread[i] = 0.5
	}
	close, distant := kind(0.4, 0, 500, 0.05), kind(2, 0, 500, 0.05)
	distant.Coef[0] = 0
	if far(close, combined, spread, 3) || !far(distant, combined, spread, 3) {
		t.Fatal("far must look at the typical coefficient, not a single one")
	}
	// Weights a profile did not learn are the prior's: being far there says nothing about the profile
	lagging := kind(0.4, 0, 500, 0.05)
	lagging.Informative = make([]bool, 10)
	for j := 1; j <= 7; j++ {
		lagging.Coef[j] = 2
	}
	lagging.Informative[8], lagging.Informative[9] = true, true
	if far(lagging, combined, spread, 3) {
		t.Fatal("far must ignore the weights a profile did not learn")
	}
}

// Most stations have no second opinion: their gc weights are only the prior's and must not drag the weight that
// the few stations with one learned. Below the minimum, the weight is combined from all and so stays near the prior.
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
		in = append(in, Input{StationID: i, ProfileID: "p", Trust: 1, Kind: k})
	}
	r := Combine(in, cfg, 11)
	if r.Coef[4] < 2 || r.Coef[4] > 2.2 || !near(r.Coef[5], -1) || r.Basis[4] != 3 || r.Basis[1] != 8 {
		t.Fatalf("learned weights %v, basis %v", r.Coef, r.Basis)
	}
	// The weights every station learned are combined from all, as before
	if !near(r.Coef[1], 1) {
		t.Fatalf("other weights changed: %v", r.Coef)
	}
	// A mask of the wrong length (the schema refuses it) leaves the profile out instead of breaking the combiner
	in[1].Kind.Informative = []bool{true}
	if r = Combine(in, cfg, 11); len(r.Excluded) != 1 {
		t.Fatalf("short mask: %+v", r.Excluded)
	}
	in[1].Kind.Informative = nil
	in[0].Kind.Informative = []bool{true, true, true, false, false, true, true, true, true, true}
	in[0].Kind.Coef[4], in[0].Kind.Coef[5] = 0, 0
	r = Combine(in, cfg, 11)
	if r.Basis[4] != 0 || math.Abs(r.Coef[4]) > 0.5 {
		t.Fatalf("two stations are below the minimum of three profiles: %v, basis %v", r.Coef[4], r.Basis)
	}
}

func TestManyNewStationsTogetherAreCapped(t *testing.T) {
	cfg := config.Default().Aggregation
	var in []Input
	for i := int64(1); i <= 4; i++ {
		in = append(in, Input{StationID: i, ProfileID: "honest", Trust: 1, Kind: kind(1+0.05*float64(i), 50, 2000, 0.06)})
	}
	// Twenty cheap new stations, each claiming far more examples than the cap allows
	for i := int64(100); i < 120; i++ {
		in = append(in, Input{StationID: i, ProfileID: "sybil", Trust: cfg.TrustStart,
			Kind: kind(-5, 1_000_000, 1_000_000, 0)})
	}
	r := Combine(in, cfg, 11)
	newShare := 0.0
	for _, u := range r.Used {
		if u.StationID >= 100 {
			newShare += u.Share
			if u.Evidence > cfg.MaxEvidence {
				t.Fatalf("evidence above the cap: %g", u.Evidence)
			}
		}
	}
	if !near(newShare, cfg.NewcomerShare) {
		t.Fatalf("new stations together got %g, want %g", newShare, cfg.NewcomerShare)
	}
	if r.Coef[3] < 1.0 || r.Coef[3] > 1.25 {
		t.Fatalf("twenty new stations moved the combination to %g", r.Coef[3])
	}
}

func TestOnlyNewStationsAreNotCappedAsAGroup(t *testing.T) {
	cfg := config.Default().Aggregation
	share := map[int64]float64{1: 0.5, 2: 0.5}
	limitNewcomers(share, map[int64]float64{1: 0.25, 2: 0.25}, cfg)
	if !near(share[1], 0.5) {
		t.Fatal("without established stations the group cap has nobody to protect")
	}
}
