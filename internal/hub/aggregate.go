// Package hub combines contributions into global models, publishes and withdraws them by the stations' check
// reports, and keeps the stations' trust. The method and its reasons are in docs/aggregation.md.
package hub

import (
	"fmt"
	"math"
	"sort"

	"github.com/michaelpietzsch/vinyl-mind/internal/config"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

// Input is one profile's model of one kind.
type Input struct {
	StationID int64
	ProfileID string
	Trust     float64
	Kind      spec.KindContribution
}

// Used is an input that went into the combination, with its weight.
type Used struct {
	Input
	Evidence float64 // weighted examples: manual judgements count ManualWeight times
	Quality  float64 // 1 at a perfect self-check, 0 at the limit MaxEstError
	Share    float64 // final share in the combination; the shares add up to 1
}

// Excluded is an input that was left out, and why.
type Excluded struct {
	StationID int64
	ProfileID string
	Reason    string
}

// Result is the combination of one kind. Coef is nil if there were not enough usable inputs.
type Result struct {
	Coef     []float64
	Spread   []float64
	Profiles int
	Stations int
	Examples float64
	Used     []Used
	Excluded []Excluded
	Reason   string // why there is no combination
}

// Combine weighs the inputs of one kind and combines them coefficient by coefficient.
func Combine(in []Input, cfg config.Aggregation, nCoef int) *Result {
	res := &Result{}
	for _, x := range in {
		k := x.Kind
		switch {
		case len(k.Coef) != nCoef:
			res.Excluded = append(res.Excluded, Excluded{x.StationID, x.ProfileID, "wrong number of coefficients"})
			continue
		case k.CVErrEst > cfg.MaxEstError:
			res.Excluded = append(res.Excluded, Excluded{x.StationID, x.ProfileID,
				fmt.Sprintf("self-check error %.2f above %.2f", k.CVErrEst, cfg.MaxEstError)})
			continue
		case k.CVErrEst >= k.CVErrRaw:
			// The station itself would not use such a model: it does not beat counting every detection
			res.Excluded = append(res.Excluded, Excluded{x.StationID, x.ProfileID, "estimate not better than raw count"})
			continue
		}
		// Claimed examples are not proof: beyond MaxEvidence, more claims buy no more weight
		u := Used{Input: x, Evidence: math.Min(cfg.MaxEvidence, cfg.ManualWeight*float64(k.NManual)+float64(k.NMaster)),
			Quality: 1 - k.CVErrEst/cfg.MaxEstError}
		if u.Evidence*u.Quality <= 0 {
			res.Excluded = append(res.Excluded, Excluded{x.StationID, x.ProfileID, "no weight"})
			continue
		}
		res.Used = append(res.Used, u)
	}
	stations := map[int64]float64{} // station → sum of its profiles' weights
	trust := map[int64]float64{}
	for _, u := range res.Used {
		stations[u.StationID] += u.Evidence * u.Quality
		trust[u.StationID] = u.Trust
		res.Examples += u.Evidence
	}
	res.Profiles, res.Stations = len(res.Used), len(stations)
	if res.Profiles < cfg.MinProfiles || res.Stations < cfg.MinStations {
		res.Reason = fmt.Sprintf("%d usable profiles from %d stations, need %d from %d", res.Profiles,
			res.Stations, cfg.MinProfiles, cfg.MinStations)
		return res
	}

	ids := make([]int64, 0, len(stations))
	for id := range stations {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	S, caps := make([]float64, len(ids)), make([]float64, len(ids))
	for i, id := range ids {
		S[i], caps[i] = stations[id], cfg.MaxStationShare*trust[id]
	}
	share := map[int64]float64{}
	for i, p := range waterfill(S, caps) {
		share[ids[i]] = p
	}
	limitNewcomers(share, trust, cfg)
	for i := range res.Used {
		u := &res.Used[i]
		u.Share = share[u.StationID] * u.Evidence * u.Quality / stations[u.StationID]
	}

	res.Coef, res.Spread = make([]float64, nCoef), make([]float64, nCoef)
	vals, ws := make([]float64, len(res.Used)), make([]float64, len(res.Used))
	for j := 0; j < nCoef; j++ {
		for i, u := range res.Used {
			vals[i], ws[i] = u.Kind.Coef[j], u.Share
		}
		if cfg.Method == "median" {
			res.Coef[j] = quantile(vals, ws, 0.5)
		} else {
			res.Coef[j] = trimmedMean(vals, ws, cfg.Trim)
		}
		res.Spread[j] = quantile(vals, ws, 0.75) - quantile(vals, ws, 0.25)
	}
	return res
}

// waterfill turns the stations' weights S into shares that add up to 1 and stay below each station's cap: shares
// follow the weights, and whatever a capped station would get beyond its cap goes to the others in proportion.
// If the caps together are below 1 (few stations, or many new ones), every station is at its cap, and the shares
// follow the caps, that is the trust.
func waterfill(S, caps []float64) []float64 {
	n := len(S)
	p := make([]float64, n)
	sumCaps := 0.0
	for _, c := range caps {
		sumCaps += c
	}
	if sumCaps <= 1 {
		for i := range p {
			p[i] = caps[i] / sumCaps
		}
		return p
	}
	capped := make([]bool, n)
	for {
		rest, free := 1.0, 0.0
		for i := range S {
			if capped[i] {
				rest -= caps[i]
			} else {
				free += S[i]
			}
		}
		changed := false
		for i := range S {
			if capped[i] {
				p[i] = caps[i]
				continue
			}
			p[i] = rest * S[i] / free
			if p[i] > caps[i] {
				capped[i], changed = true, true
			}
		}
		if !changed {
			return p
		}
	}
}

// limitNewcomers caps the share of all new stations together. Each new station is capped on its own already, but
// many cheap stations would together still outweigh the established ones; together they get at most
// NewcomerShare, which the trimming can then cut off. The relations within each group stay as they were. While
// every station is new (at the start), there is nobody to protect, and nothing changes; the operator can mark
// known stations as trusted.
func limitNewcomers(share, trust map[int64]float64, cfg config.Aggregation) {
	g, any := 0.0, false
	for id, p := range share {
		if trust[id] < cfg.NewcomerTrust {
			g += p
		} else {
			any = true
		}
	}
	if !any || g <= cfg.NewcomerShare {
		return
	}
	for id := range share {
		if trust[id] < cfg.NewcomerTrust {
			share[id] *= cfg.NewcomerShare / g
		} else {
			share[id] *= (1 - cfg.NewcomerShare) / (1 - g)
		}
	}
}

type pair struct{ v, w float64 }

func sorted(vals, ws []float64) ([]pair, float64) {
	ps := make([]pair, len(vals))
	total := 0.0
	for i := range vals {
		ps[i] = pair{vals[i], ws[i]}
		total += ws[i]
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].v < ps[j].v })
	return ps, total
}

// trimmedMean is the weighted mean after cutting off the share trim of the weight at each end. Weights are cut
// partially at the boundaries, so the result moves smoothly when a weight changes.
func trimmedMean(vals, ws []float64, trim float64) float64 {
	ps, total := sorted(vals, ws)
	lo, hi := trim*total, (1-trim)*total
	sum, kept, cum := 0.0, 0.0, 0.0
	for _, p := range ps {
		k := math.Min(cum+p.w, hi) - math.Max(cum, lo)
		if k > 0 {
			sum += k * p.v
			kept += k
		}
		cum += p.w
	}
	return sum / kept
}

// quantile is the weighted quantile: the value at which the cumulative weight reaches q, halfway between two
// values when it lands exactly on a boundary (so the median of two equal weights is their mean).
func quantile(vals, ws []float64, q float64) float64 {
	ps, total := sorted(vals, ws)
	target, cum := q*total, 0.0
	const eps = 1e-12
	for i, p := range ps {
		cum += p.w
		if math.Abs(cum-target) <= eps*total && i+1 < len(ps) {
			return (p.v + ps[i+1].v) / 2
		}
		if cum > target {
			return p.v
		}
	}
	return ps[len(ps)-1].v
}

// far reports whether a contribution lies far from the combination: its median distance per coefficient, measured
// in spreads, is above k. The floor on the spread keeps a few nearly identical profiles from calling everything
// else far.
func far(coef, combined, spread []float64, k float64) bool {
	const minSpread = 0.1
	d := make([]float64, len(coef))
	for j := range coef {
		d[j] = math.Abs(coef[j]-combined[j]) / math.Max(spread[j], minSpread)
	}
	sort.Float64s(d)
	return d[len(d)/2] > k
}
