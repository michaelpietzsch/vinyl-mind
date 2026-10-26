// Package sim simulates stations that learn together, to show what combining does before real stations take part:
// that the global model helps a new profile with few judgements, and that bad contributions do not drag it.
//
// A station is simulated as vinyl-check works today (learning.py): a ridge-penalised logistic regression per kind
// on features standardised with the station's own mean and spread, trained with Newton's method, judgements by hand
// counting five times, cross-checked by leaving out one recording with a master at a time. The code is rebuilt
// here, not imported, so that the hub stays independent of the station.
package sim

import (
	"fmt"
	"math"
	"math/rand/v2"

	"github.com/michaelpietzsch/vinyl-mind/internal/config"
	"github.com/michaelpietzsch/vinyl-mind/internal/hub"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

// Params describe one simulation.
type Params struct {
	Seed           uint64
	Stations       int     // honest stations, one profile each
	Recordings     int     // recordings with a master per honest profile
	Events         int     // detections per recording
	ManualShare    float64 // share of the events judged by hand (they count ManualWeight times)
	Heterogeneity  float64 // spread of each profile's true coefficients around the common truth
	ChainShift     float64 // spread of each chain's feature offsets (what the shared scaling must absorb)
	RecordShift    float64 // spread of each record's feature offsets: records differ, so does what is a click
	Anchors        int     // honest stations the operator trusts; the other honest ones are newcomers at the automatic maximum
	Attackers      int     // stations sending a model that points the other way, with inflated claims
	AttackerTrust  float64 // the attackers' trust: the automatic maximum is what patience buys them
	NoGC           float64 // share of the honest stations without a second opinion: gc and gc_missing are constant
	GCOutage       float64 // share of the events without a second opinion on the stations that have one
	NewRecordings  int     // labelled recordings of the new profile that has little data of its own
	NewEvents      int     // judged detections per recording of the new profile
	TestRecordings int     // held-out recordings of the new profile to measure on
	PriorStrength  float64 // ridge penalty towards the global model when it is the prior
	L2             float64 // the station's ridge penalty towards zero (learning.L2)
	ManualWeight   float64
}

// Defaults are the parameters of `go run ./tools/simulate`.
func Defaults() Params {
	return Params{Seed: 1, Stations: 8, Recordings: 6, Events: 300, ManualShare: 0.1, Heterogeneity: 0.15,
		ChainShift: 0.4, RecordShift: 0.8, Anchors: 2, Attackers: 2, AttackerTrust: 0.9, NoGC: 0.5, GCOutage: 0.05,
		NewRecordings: 2, NewEvents: 40,
		TestRecordings: 20,
		PriorStrength:  100, L2: 1, ManualWeight: 5}
}

// Truth is the common model behind all profiles, in the shared scaling: what a click looks like.
var Truth = []float64{-0.6, 1.4, 0.9, -0.3, 1.0, -0.4, -0.5, 0.3, -0.6, -0.3, 0.3}

// Report is the outcome of one simulation.
type Report struct {
	Params Params

	// Distance of the combined coefficients to the truth (mean absolute difference); DistUnmasked combines the
	// honest contributions as if every weight had been learned everywhere
	DistHonest, DistAttacked, DistNaive, DistUnmasked float64

	// Mean relative estimate error per recording on the new profile's held-out recordings
	ErrRaw, ErrLocal, ErrPrior, ErrGlobal, ErrPriorNaive float64

	Excluded []hub.Excluded
	Used     []hub.Used
}

type event struct {
	x []float64 // features in the station's units
	y float64   // 1 real, 0 music
	w float64   // example weight
}

type profile struct {
	truth  []float64 // true coefficients of this profile in the shared scaling
	offset []float64 // chain offset per feature, in units of the shared scale
	noGC   bool      // no second opinion: gc is always 0.5 and gc_missing always 1
	rng    *rand.Rand
}

func newProfile(fv *spec.Version, p Params, noGC bool, rng *rand.Rand) *profile {
	pr := &profile{truth: make([]float64, len(Truth)), offset: make([]float64, len(fv.Features)), noGC: noGC,
		rng: rng}
	for i := range Truth {
		pr.truth[i] = Truth[i] + rng.NormFloat64()*p.Heterogeneity
	}
	for i, f := range fv.Features {
		if !binary(f.Name) {
			pr.offset[i] = rng.NormFloat64() * p.ChainShift
		}
	}
	return pr
}

func binary(name string) bool { return name == "gc_missing" || name == "music" || name == "both_ch" }

// Indexes of the second-opinion features in feature version 1.
const (
	gcIndex        = 3
	gcMissingIndex = 4
)

// recording draws the detections of one recording. Features are drawn around the shared center, shifted by the
// chain and by the record (each record has its own mix of music, crackle and loud passages); labels follow the
// profile's true model in the shared scaling. A model only generalises to other records if its weights are right,
// which is what a station with few judgements cannot learn alone.
func (pr *profile) recording(fv *spec.Version, p Params, n int) []event {
	center, scale := fv.Center(), fv.Scale()
	shift, share := make([]float64, len(center)), make([]float64, len(center))
	for i := range shift {
		shift[i], share[i] = pr.rng.NormFloat64()*p.RecordShift, 0.15+0.7*pr.rng.Float64()
	}
	evs := make([]event, n)
	for k := range evs {
		x := make([]float64, len(center))
		logit := pr.truth[0]
		missing := pr.noGC || pr.rng.Float64() < p.GCOutage
		for i, f := range fv.Features {
			switch {
			case i == gcMissingIndex:
				x[i] = 0
				if missing {
					x[i] = 1
				}
			case i == gcIndex && missing:
				x[i] = 0.5
			case binary(f.Name):
				x[i] = 0
				if pr.rng.Float64() < share[i] {
					x[i] = 1
				}
			default:
				x[i] = center[i] + (pr.rng.NormFloat64()+pr.offset[i]+shift[i])*scale[i]
			}
			logit += pr.truth[i+1] * (x[i] - center[i]) / scale[i]
		}
		y := 0.0
		if pr.rng.Float64() < sigmoid(logit) {
			y = 1
		}
		w := 1.0
		if pr.rng.Float64() < p.ManualShare {
			w = p.ManualWeight
		}
		evs[k] = event{x: x, y: y, w: w}
	}
	return evs
}

func sigmoid(z float64) float64 { return 1 / (1 + math.Exp(-math.Max(-30, math.Min(30, z)))) }

// model is a logistic regression on standardised features: z = (x - mu) / sd, p = sigmoid(b0 + b·z).
type model struct{ mu, sd, b []float64 }

func (m model) predict(x []float64) float64 {
	t := m.b[0]
	for i := range x {
		t += m.b[i+1] * (x[i] - m.mu[i]) / m.sd[i]
	}
	return sigmoid(t)
}

// fit is learning._fit: weighted logistic regression with a ridge penalty, Newton's method, no penalty on the
// intercept. With a prior, the penalty pulls towards the prior's coefficients instead of towards zero, intercept
// included; mu and sd are then fixed (the shared scaling) rather than the data's own.
func fit(evs []event, l2 float64, mu, sd, prior []float64) model {
	d := len(evs[0].x)
	if mu == nil {
		mu, sd = make([]float64, d), make([]float64, d)
		tw := 0.0
		for _, e := range evs {
			for i, v := range e.x {
				mu[i] += v
			}
			tw++
		}
		for i := range mu {
			mu[i] /= tw
		}
		for _, e := range evs {
			for i, v := range e.x {
				sd[i] += (v - mu[i]) * (v - mu[i])
			}
		}
		for i := range sd {
			sd[i] = math.Sqrt(sd[i] / tw)
			if sd[i] < 1e-9 {
				sd[i] = 1
			}
		}
	}
	target := make([]float64, d+1)
	if prior != nil {
		copy(target, prior)
	}
	b := append([]float64(nil), target...)
	z := make([]float64, d+1)
	for iter := 0; iter < 60; iter++ {
		g := make([]float64, d+1)
		H := make([][]float64, d+1)
		for i := range H {
			H[i] = make([]float64, d+1)
		}
		for _, e := range evs {
			z[0] = 1
			for i, v := range e.x {
				z[i+1] = (v - mu[i]) / sd[i]
			}
			t := 0.0
			for i := range z {
				t += b[i] * z[i]
			}
			p := sigmoid(t)
			r, h := e.w*(e.y-p), e.w*p*(1-p)+1e-9
			for i := range z {
				g[i] += r * z[i]
				for j := range z {
					H[i][j] += h * z[i] * z[j]
				}
			}
		}
		// Without a prior the intercept is free, as in learning.py. With one it is pulled towards the global
		// intercept as well: left free, it absorbs the peculiarities of the few records a new profile has seen.
		first := 1
		if prior != nil {
			first = 0
		}
		for i := first; i <= d; i++ {
			g[i] -= l2 * (b[i] - target[i])
			H[i][i] += l2
		}
		step := solve(H, g)
		maxStep := 0.0
		for i := range b {
			b[i] += step[i]
			maxStep = math.Max(maxStep, math.Abs(step[i]))
		}
		if maxStep < 1e-7 {
			break
		}
	}
	return model{mu: mu, sd: sd, b: b}
}

// solve is Gaussian elimination with partial pivoting; the systems here have eleven unknowns.
func solve(A [][]float64, rhs []float64) []float64 {
	n := len(rhs)
	M := make([][]float64, n)
	for i := range M {
		M[i] = append(append([]float64(nil), A[i]...), rhs[i])
	}
	for c := 0; c < n; c++ {
		piv := c
		for r := c + 1; r < n; r++ {
			if math.Abs(M[r][c]) > math.Abs(M[piv][c]) {
				piv = r
			}
		}
		M[c], M[piv] = M[piv], M[c]
		for r := c + 1; r < n; r++ {
			f := M[r][c] / M[c][c]
			for k := c; k <= n; k++ {
				M[r][k] -= f * M[c][k]
			}
		}
	}
	x := make([]float64, n)
	for r := n - 1; r >= 0; r-- {
		s := M[r][n]
		for k := r + 1; k < n; k++ {
			s -= M[r][k] * x[k]
		}
		x[r] = s / M[r][r]
	}
	return x
}

// estimateError is the station's cross-check measure: per recording, the relative difference between the
// estimated number of real clicks (the sum of probabilities) and the true number, averaged. Recordings with fewer
// than three real clicks are skipped, as learning.py does.
func estimateError(recs [][]event, predict func([]float64) float64) (est, raw float64) {
	n := 0
	for _, rec := range recs {
		truth, sum := 0.0, 0.0
		for _, e := range rec {
			truth += e.y
			sum += predict(e.x)
		}
		if truth < 3 {
			continue
		}
		est += math.Abs(sum-truth) / truth
		raw += math.Abs(float64(len(rec))-truth) / truth
		n++
	}
	return est / float64(n), raw / float64(n)
}

func flatten(recs [][]event) []event {
	var out []event
	for _, r := range recs {
		out = append(out, r...)
	}
	return out
}

// contribution trains a profile's model as the station does, cross-checks it and converts it into the shared
// scaling: what the station would send.
func contribution(fv *spec.Version, p Params, recs [][]event) spec.KindContribution {
	all := flatten(recs)
	m := fit(all, p.L2, nil, nil, nil)
	var errEst, errRaw float64
	n := 0
	for i := range recs {
		var train []event
		for j, r := range recs {
			if j != i {
				train = append(train, r...)
			}
		}
		cm := fit(train, p.L2, nil, nil, nil)
		e, r := estimateError(recs[i:i+1], cm.predict)
		if !math.IsNaN(e) {
			errEst += e
			errRaw += r
			n++
		}
	}
	k := spec.KindContribution{Coef: spec.ToShared(fv, m.mu, m.sd, m.b), MasterRecordings: len(recs),
		CVErrEst: errEst / float64(n), CVErrRaw: errRaw / float64(n), Informative: informative(all)}
	for _, e := range all {
		if e.y == 1 {
			k.NReal++
		} else {
			k.NFalse++
		}
		if e.w > 1 {
			k.NManual++
		} else {
			k.NMaster++
		}
	}
	return k
}

// informative is what the station reports: a feature is informative if it took more than one value. Without a
// prior, the weight of a constant feature is already 0 (its standardised value is always 0), as the rule asks.
func informative(evs []event) []bool {
	out, all := make([]bool, len(evs[0].x)), true
	for i := range out {
		for _, e := range evs {
			if e.x[i] != evs[0].x[i] {
				out[i] = true
				break
			}
		}
		all = all && out[i]
	}
	if all {
		return nil
	}
	return out
}

func distance(a, b []float64) float64 {
	d := 0.0
	for i := range a {
		d += math.Abs(a[i] - b[i])
	}
	return d / float64(len(a))
}

// naive is the plain weighted mean by claimed examples, without caps, trimming or self-check filter: what
// federated averaging does when it trusts every contribution.
func naive(in []hub.Input, manualWeight float64) []float64 {
	out := make([]float64, len(in[0].Kind.Coef))
	total := 0.0
	for _, x := range in {
		w := manualWeight*float64(x.Kind.NManual) + float64(x.Kind.NMaster)
		total += w
		for j, c := range x.Kind.Coef {
			out[j] += w * c
		}
	}
	for j := range out {
		out[j] /= total
	}
	return out
}

// Run simulates honest stations, attackers and one new profile, and measures.
func Run(p Params) (*Report, error) {
	fv, err := spec.Get(1)
	if err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewPCG(p.Seed, 0x9e3779b97f4a7c15))
	cfg := config.Default().Aggregation
	cfg.ManualWeight = p.ManualWeight

	var honest []hub.Input
	noGC := int(math.Round(p.NoGC * float64(p.Stations)))
	for s := 0; s < p.Stations; s++ {
		pr := newProfile(fv, p, s < noGC, rng)
		recs := make([][]event, p.Recordings)
		for i := range recs {
			recs[i] = pr.recording(fv, p, p.Events)
		}
		in := hub.Input{StationID: int64(s + 1), ProfileID: fmt.Sprint("honest-", s), Trust: cfg.TrustAutoMax,
			Kind: contribution(fv, p, recs)}
		if s < p.Anchors {
			in.Trust, in.Anchor = 1, true
		}
		honest = append(honest, in)
	}
	attacked := append([]hub.Input(nil), honest...)
	for a := 0; a < p.Attackers; a++ {
		bad := make([]float64, len(Truth))
		for i := range bad {
			bad[i] = -3 * Truth[i]
		}
		attacked = append(attacked, hub.Input{StationID: int64(1000 + a), ProfileID: fmt.Sprint("attacker-", a),
			Trust: p.AttackerTrust, Kind: spec.KindContribution{Coef: bad, NReal: 5_000_000, NFalse: 5_000_000,
				NManual: 5_000_000, NMaster: 5_000_000, MasterRecordings: 1000, CVErrEst: 0, CVErrRaw: 1}})
	}

	unmasked := make([]hub.Input, len(honest))
	for i, x := range honest {
		x.Kind.Informative = nil
		unmasked[i] = x
	}
	rh := hub.Combine(honest, cfg, fv.NCoef())
	ra := hub.Combine(attacked, cfg, fv.NCoef())
	ru := hub.Combine(unmasked, cfg, fv.NCoef())
	if rh.Coef == nil || ra.Coef == nil || ru.Coef == nil {
		return nil, fmt.Errorf("no combination: %s %s", rh.Reason, ra.Reason)
	}
	naiveCoef := naive(attacked, p.ManualWeight)
	rep := &Report{Params: p, DistHonest: distance(rh.Coef, Truth), DistAttacked: distance(ra.Coef, Truth),
		DistNaive: distance(naiveCoef, Truth), DistUnmasked: distance(ru.Coef, Truth), Excluded: ra.Excluded,
		Used: ra.Used}

	// The new profile: little data of its own, judged on many held-out recordings, and no second opinion, as most
	pr := newProfile(fv, p, true, rng)
	own := make([][]event, p.NewRecordings)
	for i := range own {
		own[i] = pr.recording(fv, p, p.NewEvents)
	}
	test := make([][]event, p.TestRecordings)
	for i := range test {
		test[i] = pr.recording(fv, p, p.Events)
	}
	center, scale := fv.Center(), fv.Scale()
	shared := func(coef []float64) func([]float64) float64 {
		return model{mu: center, sd: scale, b: coef}.predict
	}
	data := flatten(own)
	local := fit(data, p.L2, nil, nil, nil)
	prior := fit(data, p.PriorStrength, center, scale, ra.Coef)
	priorNaive := fit(data, p.PriorStrength, center, scale, naiveCoef)
	rep.ErrLocal, rep.ErrRaw = estimateError(test, local.predict)
	rep.ErrPrior, _ = estimateError(test, prior.predict)
	rep.ErrGlobal, _ = estimateError(test, shared(ra.Coef))
	rep.ErrPriorNaive, _ = estimateError(test, priorNaive.predict)
	return rep, nil
}
