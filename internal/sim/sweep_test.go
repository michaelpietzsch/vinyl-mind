package sim

// The simulation's claims as bounds, over seeds: what attackers and turned anchors can and cannot do to the
// model, with and without a second opinion among most anchors, and the feedback loop of stations that adopt
// the global model as their prior. These take a minute and are skipped in short mode; the CI runs them.

import (
	"fmt"
	"math"
	"math/rand/v2"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/michaelpietzsch/vinyl-mind/internal/config"
	"github.com/michaelpietzsch/vinyl-mind/internal/hub"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

func median(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n == 0 {
		return math.NaN()
	}
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// seedRow is what one seed of the simulation tells.
type seedRow struct {
	ok                      bool
	honest, attacked, naive float64
	errLocal, errPrior      float64
	honestFar, honestN      int
	demotedBad, demotedGood int
	attackerShare           float64
}

// overSeeds runs the simulation with the given changes over the seeds, in parallel.
func overSeeds(t *testing.T, seeds int, mod func(*Params)) []seedRow {
	t.Helper()
	rows := make([]seedRow, seeds)
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for i := 0; i < seeds; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			p := Defaults()
			p.Seed = uint64(i + 1)
			mod(&p)
			r, err := Run(p)
			if err != nil {
				return
			}
			row := seedRow{ok: true, honest: r.DistHonest, attacked: r.DistAttacked, naive: r.DistNaive,
				errLocal: r.ErrLocal, errPrior: r.ErrPrior}
			for _, u := range r.Used {
				switch {
				case strings.HasPrefix(u.ProfileID, "honest"):
					row.honestN++
					if u.Far {
						row.honestFar++
					}
					if u.Demoted {
						row.demotedGood++
					}
				case strings.HasPrefix(u.ProfileID, "bad") && u.Demoted:
					row.demotedBad++
				case strings.HasPrefix(u.ProfileID, "attacker"):
					row.attackerShare += u.Share
				}
			}
			rows[i] = row
		}(i)
	}
	wg.Wait()
	return rows
}

type summary struct {
	models                          int
	honest, attacked, naive         float64 // medians over the seeds with a model
	attackerShare                   float64
	honestFar, honestN              int
	demotedBad, demotedGood, better int
}

func summarize(rows []seedRow) summary {
	var s summary
	var h, a, n, as []float64
	for _, r := range rows {
		if !r.ok {
			continue
		}
		s.models++
		h, a, n, as = append(h, r.honest), append(a, r.attacked), append(n, r.naive), append(as, r.attackerShare)
		s.honestFar += r.honestFar
		s.honestN += r.honestN
		s.demotedBad += r.demotedBad
		s.demotedGood += r.demotedGood
		if r.errPrior < r.errLocal {
			s.better++
		}
	}
	s.honest, s.attacked, s.naive, s.attackerShare = median(h), median(a), median(n), median(as)
	return s
}

// TestSimulationSeeds holds the numbers of docs/aggregation.md: attackers, patient or fresh, two or forty, move
// the model by less than 0.01, get at most a fifth together, and the new profile does better with the global
// model as its prior than on its own in most seeds; and two anchors at twice the assumed heterogeneity still
// form a model in most seeds (without anything published, a disputed coefficient means no model).
func TestSimulationSeeds(t *testing.T) {
	if testing.Short() {
		t.Skip("takes a while; the CI runs it")
	}
	const seeds = 20
	cases := []struct {
		name      string
		mod       func(*Params)
		maxDist   float64 // attacked, median
		minModels int
		minBetter int // seeds in which the new profile does better with the global model as its prior
		maxFar    int // honest stations found far, in percent
	}{
		{"defaults: 2 anchors, 2 attackers", func(p *Params) {}, 0.215, 20, 15, 2},
		{"20 attackers", func(p *Params) { p.Attackers = 20 }, 0.22, 20, 15, 2},
		{"40 attackers", func(p *Params) { p.Attackers = 40 }, 0.22, 20, 15, 2},
		{"2 fresh attackers", func(p *Params) { p.AttackerTrust = 0.25 }, 0.215, 20, 15, 2},
		{"1 anchor", func(p *Params) { p.Anchors = 1 }, 0.25, 20, 15, 2},
		{"4 anchors", func(p *Params) { p.Anchors = 4 }, 0.19, 20, 15, 2},
		{"8 anchors, half with a second opinion", func(p *Params) { p.Anchors = 8 }, 0.2, 20, 15, 2},
		{"8 anchors, six with a second opinion", func(p *Params) { p.Anchors, p.NoGC = 8, 0.25 }, 0.09, 20, 15, 2},
		{"16 honest stations", func(p *Params) { p.Stations = 16 }, 0.215, 20, 15, 2},
		// Twice the assumed spread: two anchors dispute a coefficient now and then (no model while nothing is
		// published), a fifth of the honest stations is far, and the prior helps the new profile less. Not a
		// security bound: what the floor of the band would have to grow to if stations really differ that much.
		{"heterogeneity 0.30", func(p *Params) { p.Heterogeneity = 0.3 }, 0.29, 16, 5, 25},
	}
	for _, c := range cases {
		s := summarize(overSeeds(t, seeds, c.mod))
		t.Logf("%-40s models %d/%d honest %.3f attacked %.3f naive %.3f attackers' share %.3f honest far %d/%d "+
			"prior better in %d", c.name, s.models, seeds, s.honest, s.attacked, s.naive, s.attackerShare, s.honestFar,
			s.honestN, s.better)
		switch {
		case s.models < c.minModels:
			t.Errorf("%s: a model in %d of %d seeds, want %d", c.name, s.models, seeds, c.minModels)
		case s.attacked > c.maxDist || s.attacked > s.honest+0.025:
			t.Errorf("%s: attacked %.3f, honest %.3f, limit %.3f", c.name, s.attacked, s.honest, c.maxDist)
		case s.attackerShare > 0.2 || s.naive < 1 || s.better < c.minBetter || 100*s.honestFar > c.maxFar*s.honestN:
			t.Errorf("%s: attackers' share %.3f, naive %.3f, prior better in %d, honest far %d/%d", c.name,
				s.attackerShare, s.naive, s.better, s.honestFar, s.honestN)
		}
	}
}

// TestTurnedAnchorsSweep: anchors that send the attackers' model, fewer than half of all anchors, are demoted in
// every seed, no honest station is found far, and the model stays within 0.025 of the honest one; half of the
// anchors turned form no model. With and without a second opinion among most anchors.
func TestTurnedAnchorsSweep(t *testing.T) {
	if testing.Short() {
		t.Skip("takes a while; the CI runs it")
	}
	const seeds = 20
	for _, noGC := range []float64{0.5, 0.25} {
		for _, c := range []struct{ stations, anchors, bad int }{{8, 2, 1}, {8, 3, 1}, {8, 5, 2}, {8, 6, 3}, {8, 7, 3},
			{8, 8, 3}, {16, 12, 4}, {16, 13, 4}, {8, 4, 2}, {8, 8, 0}, {8, 5, 0}, {8, 2, 2}} {
			s := summarize(overSeeds(t, seeds, func(p *Params) {
				p.Stations, p.Anchors, p.BadAnchors, p.NoGC = c.stations, c.anchors, c.bad, noGC
			}))
			name := fmt.Sprintf("no-gc %.2f, %d honest anchors + %d turned", noGC, c.anchors, c.bad)
			if c.anchors == c.bad {
				if s.models != 0 {
					t.Errorf("%s: half of the anchors turned, a model in %d seeds", name, s.models)
				}
				continue
			}
			t.Logf("%-42s models %d/%d honest %.3f attacked %.3f honest far %d/%d demoted bad %d good %d", name,
				s.models, seeds, s.honest, s.attacked, s.honestFar, s.honestN, s.demotedBad, s.demotedGood)
			switch {
			case s.models != seeds:
				t.Errorf("%s: a model in %d of %d seeds", name, s.models, seeds)
			case s.attacked > s.honest+0.025:
				t.Errorf("%s: attacked %.3f, honest %.3f", name, s.attacked, s.honest)
			case s.demotedBad != seeds*c.bad || s.demotedGood != 0 || s.honestFar > s.honestN/100:
				t.Errorf("%s: demoted bad %d of %d, good %d, honest far %d/%d", name, s.demotedBad, seeds*c.bad,
					s.demotedGood, s.honestFar, s.honestN)
			}
			// What a second opinion among most anchors is worth, and what half the anchors without one costs
			if c.bad == 0 && c.anchors == 8 {
				if limit := map[float64]float64{0.5: 0.19, 0.25: 0.08}[noGC]; s.honest > limit {
					t.Errorf("%s: honest %.3f, limit %.2f", name, s.honest, limit)
				}
			}
		}
	}
}

// population trains n honest profiles like Run does and returns their contributions and recordings.
func population(fv *spec.Version, p Params, n int, r *rand.Rand) ([]spec.KindContribution, [][][]event) {
	kinds := make([]spec.KindContribution, n)
	recs := make([][][]event, n)
	noGC := int(math.Round(p.NoGC * float64(n)))
	for s := 0; s < n; s++ {
		pr := newProfile(fv, p, s < noGC, r)
		recs[s] = make([][]event, p.Recordings)
		for i := range recs[s] {
			recs[s][i] = pr.recording(fv, p, p.Events)
		}
		kinds[s] = contribution(fv, p, recs[s])
	}
	return kinds, recs
}

func attackerKind(coef []float64) spec.KindContribution {
	return spec.KindContribution{Coef: append([]float64(nil), coef...), NReal: 5_000_000, NFalse: 5_000_000,
		NManual: 5_000_000, NMaster: 5_000_000, MasterRecordings: 1000, CVErrEst: 0, CVErrRaw: 100}
}

// TestPriorFeedback: the slow poisoning the review asked about. Honest stations adopt the global model as the
// prior of their training when it is not worse on their own recordings, and an attacker nudges one coefficient
// every round from wherever the model is. Over eight rounds, attackers without the operator's trust move
// nothing, and one turned anchor among two, three or four moves the coefficient by less than the band; among
// two, the big step and the jump find no majority and no model.
func TestPriorFeedback(t *testing.T) {
	if testing.Short() {
		t.Skip("takes a while; the CI runs it")
	}
	fv, _ := spec.Get(1)
	p := Defaults()
	center, scale := fv.Center(), fv.Scale()
	const target = 1
	cases := []struct {
		name          string
		honestAnchors int
		attackers     int
		attackAnchor  bool
		trust         float64
		delta         float64
		maxDrift      float64 // of the target coefficient after eight rounds, against the baseline
		noModel       bool
	}{
		{"no attacker, 2 anchors", 2, 0, false, 0, 0, 0, false},
		{"1 attacker 0.9, coef1 at +50 every round", 2, 1, false, 0.9, 50, 0.02, false},
		{"1 attacker 0.9, nudge +0.3 per round", 2, 1, false, 0.9, 0.3, 0.02, false},
		{"4 attackers 0.9, nudge +0.3 per round", 2, 4, false, 0.9, 0.3, 0.02, false},
		{"1 turned anchor of 2, nudge +0.3 per round", 1, 1, true, 1, 0.3, 0.2, false},
		{"1 turned anchor of 2, nudge +1.0 per round", 1, 1, true, 1, 1.0, 0, true},
		{"1 turned anchor of 2, coef1 at +50", 1, 1, true, 1, 50, 0, true},
		{"1 turned anchor of 3, nudge +0.3 per round", 2, 1, true, 1, 0.3, 0.2, false},
		{"1 turned anchor of 3, coef1 at +50", 2, 1, true, 1, 50, 0.2, false},
		{"1 turned anchor of 4, nudge +0.3 per round", 3, 1, true, 1, 0.3, 0.2, false},
	}
	baseline := math.NaN()
	for _, c := range cases {
		cfg := config.Default().Aggregation
		r := rand.New(rand.NewPCG(7, 11))
		const n = 8
		kinds, recs := population(fv, p, n, r)
		local := make([]model, n)
		for s := 0; s < n; s++ {
			local[s] = fit(flatten(recs[s]), p.L2, nil, nil, nil)
		}
		var g []float64
		for round := 0; round < 8; round++ {
			var in []hub.Input
			for s := 0; s < n; s++ {
				x := hub.Input{StationID: int64(s + 1), ProfileID: "h", Trust: cfg.TrustAutoMax, Kind: kinds[s]}
				if s < c.honestAnchors {
					x.Trust, x.Anchor = 1, true
				}
				in = append(in, x)
			}
			if c.attackers > 0 {
				atk := append([]float64(nil), Truth...)
				if g != nil {
					atk = append([]float64(nil), g...)
				}
				atk[target] = math.Max(-50, math.Min(50, atk[target]+c.delta))
				for a := 0; a < c.attackers; a++ {
					in = append(in, hub.Input{StationID: int64(100 + a), ProfileID: "a", Trust: c.trust,
						Anchor: c.attackAnchor, Kind: attackerKind(atk)})
				}
			}
			res := hub.Combine(in, cfg, fv.NCoef())
			if res.Coef == nil {
				if !c.noModel {
					t.Errorf("%s round %d: no model: %s", c.name, round, res.Reason)
				}
				g = nil
				break
			}
			g = res.Coef
			shared := model{mu: center, sd: scale, b: g}.predict
			for s := 0; s < n; s++ {
				eg, _ := estimateError(recs[s], shared)
				el, _ := estimateError(recs[s], local[s].predict)
				if eg <= el+0.01 {
					m := fit(flatten(recs[s]), p.PriorStrength, center, scale, g)
					kinds[s].Coef = m.b
					kinds[s].Informative = informative(flatten(recs[s]))
				}
			}
		}
		if c.noModel {
			if g != nil {
				t.Errorf("%s: a model was built", c.name)
			}
			continue
		}
		if g == nil {
			continue
		}
		if math.IsNaN(baseline) {
			baseline = g[target]
		}
		drift := math.Abs(g[target] - baseline)
		t.Logf("%-44s coefficient %d after eight rounds %.3f (baseline %.3f, truth %.2f), distance %.3f", c.name,
			target, g[target], baseline, Truth[target], distance(g, Truth))
		if drift > c.maxDrift || distance(g, Truth) > 0.25 {
			t.Errorf("%s: drift %.3f (limit %.2f), distance %.3f", c.name, drift, c.maxDrift, distance(g, Truth))
		}
	}
}
