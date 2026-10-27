package hub

// Shared material for the scenario tests: a true model, honest profiles around it with a given noise, attackers
// claiming the maximum of everything, and groups of stations combined in one call. The scenario tests are the
// regression suite of the security review: every rule in aggregate.go was changed at least once to close one
// case and found to open a neighbouring one, so the neighbours are all here, with bounds.

import (
	"fmt"
	"math"
	"math/rand/v2"

	"github.com/michaelpietzsch/vinyl-mind/internal/config"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

// truth is the model the honest stations learn, more or less.
var truth = []float64{-0.6, 1.4, 0.9, -0.3, 1.0, -0.4, -0.5, 0.3, -0.6, -0.3, 0.3}

func rng(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, 4242)) }

// honestKind is a profile around the truth, off by noise per coefficient, with realistic counts and self-check.
func honestKind(r *rand.Rand, noise float64) spec.KindContribution {
	coef := make([]float64, 11)
	for i := range coef {
		coef[i] = truth[i] + r.NormFloat64()*noise
	}
	return spec.KindContribution{Coef: coef, NReal: 900, NFalse: 900, NManual: 180, NMaster: 1620,
		MasterRecordings: 6, CVErrEst: 0.12, CVErrRaw: 1.0}
}

// attackKind is an attacker's profile: the given coefficients and the maximum every field may claim.
func attackKind(coef []float64) spec.KindContribution {
	return spec.KindContribution{Coef: append([]float64(nil), coef...), NReal: 5_000_000, NFalse: 5_000_000,
		NManual: 5_000_000, NMaster: 5_000_000, MasterRecordings: 1000, CVErrEst: 0, CVErrRaw: 100}
}

func constCoef(v float64) []float64 {
	c := make([]float64, 11)
	for i := range c {
		c[i] = v
	}
	return c
}

func scaledTruth(f float64) []float64 {
	c := make([]float64, 11)
	for i := range c {
		c[i] = f * truth[i]
	}
	return c
}

func shiftedTruth(d float64) []float64 {
	c := make([]float64, 11)
	for i := range c {
		c[i] = truth[i] + d
	}
	return c
}

// group is a set of stations that share a role: honest anchors, honest newcomers, colluders.
type group struct {
	name     string
	stations int
	profiles int
	trust    float64
	anchor   bool
	kind     func(s, p int) spec.KindContribution
}

// groupsResult is a combination of groups, summed up per group.
type groupsResult struct {
	*Result
	share           map[string]float64 // share per group
	far, demoted    map[string]int     // stations per group
	clipped         map[string]int     // coefficients pulled into the band, per group
	stationsOfGroup map[string]int
}

func combineGroups(cfg config.Aggregation, published []float64, groups ...group) groupsResult {
	var in []Input
	owner := map[int64]string{}
	id := int64(0)
	o := groupsResult{share: map[string]float64{}, far: map[string]int{}, demoted: map[string]int{},
		clipped: map[string]int{}, stationsOfGroup: map[string]int{}}
	for _, g := range groups {
		o.stationsOfGroup[g.name] += g.stations
		for s := 0; s < g.stations; s++ {
			id++
			owner[id] = g.name
			for p := 0; p < g.profiles; p++ {
				in = append(in, Input{StationID: id, ProfileID: fmt.Sprintf("%s-%d-%d", g.name, s, p), Trust: g.trust,
					Anchor: g.anchor, Account: g.name, Kind: g.kind(s, p)})
			}
		}
	}
	o.Result = CombineWith(in, cfg, 11, published)
	if o.Coef == nil {
		return o
	}
	farStation, demotedStation := map[int64]bool{}, map[int64]bool{}
	for _, u := range o.Used {
		o.share[owner[u.StationID]] += u.Share
		o.clipped[owner[u.StationID]] += u.Clipped
		farStation[u.StationID] = farStation[u.StationID] || u.Far
		demotedStation[u.StationID] = demotedStation[u.StationID] || u.Demoted
	}
	for id, g := range owner {
		if farStation[id] {
			o.far[g]++
		}
		if demotedStation[id] {
			o.demoted[g]++
		}
	}
	return o
}

// maxShift is the largest difference between two coefficient vectors, and where.
func maxShift(a, b []float64) (float64, int) {
	m, at := 0.0, -1
	for i := range a {
		if d := math.Abs(a[i] - b[i]); d > m {
			m, at = d, i
		}
	}
	return m, at
}

// honestSet draws n stations with profiles each around the truth, reproducibly.
func honestSet(n, profiles int, seed uint64, noise float64) [][]spec.KindContribution {
	r := rand.New(rand.NewPCG(seed, 99))
	out := make([][]spec.KindContribution, n)
	for s := range out {
		out[s] = make([]spec.KindContribution, profiles)
		for p := range out[s] {
			out[s][p] = honestKind(r, noise)
		}
	}
	return out
}

func honestGroup(name string, n, profiles int, trust float64, anchor bool, set [][]spec.KindContribution) group {
	return group{name, n, profiles, trust, anchor, func(s, p int) spec.KindContribution { return set[s][p] }}
}

func attackGroup(name string, n, profiles int, trust float64, anchor bool, coef []float64) group {
	return group{name, n, profiles, trust, anchor, func(s, p int) spec.KindContribution { return attackKind(coef) }}
}
