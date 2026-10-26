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
	Account   string
	Trust     float64
	// Anchor marks a station the operator trusts at the full level. The anchors are the reference: without one
	// there is no combination, their coefficients define the band everything else is held in, and only their
	// check reports publish a model. Automatic trust never makes an anchor.
	Anchor bool
	Kind   spec.KindContribution
}

// Used is an input that went into the combination, with its weight.
type Used struct {
	Input
	Coef     []float64 // the coefficients as combined: the station's own, pulled into the band around the reference
	Evidence float64   // weighted examples: manual judgements count ManualWeight times
	Quality  float64   // 1 at a perfect self-check, 0 at the limit MaxEstError
	Share    float64   // final share in the combination; the shares add up to 1
	Far      bool      // outside the band on at least farCount of the coefficients the profile learned
	Outside  int       // learned coefficients outside the band
	Clipped  int       // coefficients pulled into the band
	MaxDist  float64   // the largest distance to the reference, in bands
	Demoted  bool      // an anchor far from the other anchors: held like a newcomer in this combination
}

// farCount is how many learned coefficients outside the band make a contribution far from the reference: one
// outlier is pulled into the band and forgiven, two are a pattern.
const farCount = 2

// Excluded is an input that was left out, and why.
type Excluded struct {
	StationID int64
	ProfileID string
	Reason    string
}

// Result is the combination of one kind. Coef is nil if there were not enough usable inputs or no anchor.
type Result struct {
	Coef      []float64
	Spread    []float64
	Reference []float64 // the anchors' coefficients, median per coefficient
	Band      []float64 // half-width of the band around the reference, per coefficient
	Basis     []int     // per coefficient, the stations it was combined from; 0: kept at the reference (see Combine)
	Profiles  int
	Stations  int
	Anchors   int
	Examples  float64
	Used      []Used
	Excluded  []Excluded
	Reason    string // why there is no combination
	Disagree  int    // coefficients on which two anchors differ by more than the band (then there is no model)
}

// Combine weighs the inputs of one kind and combines them coefficient by coefficient.
//
// Every claim in a contribution is the station's own word, so nothing in it may buy influence beyond what the
// operator's anchors allow: a contribution is held within a band around the anchors' coefficients, the stations
// the operator does not vouch for share one fifth in equal parts, and a weight that no anchor learned stays
// where the anchors left it.
func Combine(in []Input, cfg config.Aggregation, nCoef int) *Result {
	res := &Result{}
	for _, x := range in {
		k := x.Kind
		switch {
		case len(k.Coef) != nCoef || (k.Informative != nil && len(k.Informative) != nCoef-1):
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
		case !finite(k.Coef):
			// The specification refuses these at the door; a stored one must not break the combination
			res.Excluded = append(res.Excluded, Excluded{x.StationID, x.ProfileID, "not a number"})
			continue
		}
		// Claimed examples are not proof: beyond MaxEvidence, more claims buy no more weight
		u := Used{Input: x, Coef: append([]float64(nil), k.Coef...),
			Evidence: math.Min(cfg.MaxEvidence, cfg.ManualWeight*float64(k.NManual)+float64(k.NMaster)),
			Quality:  1 - k.CVErrEst/cfg.MaxEstError}
		if u.Evidence*u.Quality <= 0 {
			res.Excluded = append(res.Excluded, Excluded{x.StationID, x.ProfileID, "no weight"})
			continue
		}
		res.Used = append(res.Used, u)
	}
	capStationEvidence(res.Used, 2*cfg.MaxEvidence)
	for _, u := range res.Used {
		res.Examples += u.Evidence
	}
	res.Profiles, res.Stations, res.Anchors = len(res.Used), countStations(res.Used), countAnchors(res.Used)
	if !enough(res.Used, cfg) {
		res.Reason = fmt.Sprintf("%d usable profiles from %d stations, need %d from %d", res.Profiles,
			res.Stations, cfg.MinProfiles, cfg.MinStations)
		return res
	}
	if res.Anchors == 0 {
		res.Reason = "no station trusted by the operator among the inputs"
		return res
	}

	// The reference is the anchors' position; every other station is held within the band around it. An anchor
	// is held to the other anchors: to the median of all of them while they are few (a median of three stands one
	// bad anchor), and to the others alone once at least three of them learned the coefficient. Two anchors have
	// no majority: if they disagree, there is no reference and no model.
	var known []bool
	res.Reference, res.Band, known = reference(res.Used, 0, cfg, nCoef)
	// An anchor far from the other anchors, on the coefficients they share, is no reference in this combination:
	// it is held like a newcomer, and a coefficient only it learned stays the prior rather than its word. Only
	// other anchors can bring this about, never newcomers, and it changes no trust: the operator is told.
	if res.Anchors >= 3 {
		if demote(res.Used, cfg, nCoef) {
			res.Anchors = countAnchors(res.Used)
			res.Reference, res.Band, known = reference(res.Used, 0, cfg, nCoef)
		}
	}
	if res.Anchors == 2 {
		if res.Disagree = disagreement(res.Used, res.Band, known); res.Disagree >= farCount {
			res.Reason = fmt.Sprintf("the two anchors disagree on %d coefficients", res.Disagree)
			return res
		}
	}
	for i := range res.Used {
		u := &res.Used[i]
		ref, band, learned := res.Reference, res.Band, known
		if u.Anchor {
			if res.Anchors < 2 {
				continue
			}
			ref, band, learned = reference(res.Used, u.StationID, cfg, nCoef)
		}
		hold(u, ref, band, learned)
	}
	shares := weigh(res.Used, cfg)
	for i := range res.Used {
		res.Used[i].Share = shares[i]
	}

	// A weight is combined only from the profiles that learned it: where a feature never varied, the profile
	// sent the prior's weight, and averaging that in would only drag the combination back. The shares are worked
	// out again among those profiles. Unless an anchor is among them, and enough others, the weight stays what
	// the anchors sent, which is the prior they trained towards: a group of newcomers cannot define it alone.
	res.Coef, res.Spread, res.Basis = make([]float64, nCoef), make([]float64, nCoef), make([]int, nCoef)
	for j := 0; j < nCoef; j++ {
		used, ws := res.Used, shares
		var sub []Used
		for _, u := range res.Used {
			if u.Kind.Informs(j) {
				sub = append(sub, u)
			}
		}
		switch {
		case len(sub) == len(res.Used):
			res.Basis[j] = res.Stations
		case countAnchors(sub) > 0 && enough(sub, cfg):
			used, ws, res.Basis[j] = sub, weigh(sub, cfg), countStations(sub)
		default:
			res.Coef[j] = res.Reference[j]
			continue
		}
		vals := make([]float64, len(used))
		for i, u := range used {
			vals[i] = u.Coef[j]
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

func countStations(used []Used) int {
	ids := map[int64]bool{}
	for _, u := range used {
		ids[u.StationID] = true
	}
	return len(ids)
}

func countAnchors(used []Used) int {
	ids := map[int64]bool{}
	for _, u := range used {
		if u.Anchor {
			ids[u.StationID] = true
		}
	}
	return len(ids)
}

func enough(used []Used, cfg config.Aggregation) bool {
	return len(used) >= cfg.MinProfiles && countStations(used) >= cfg.MinStations
}

// capStationEvidence bounds what one station claims across all its profiles: beyond max, more profiles buy no
// more weight, each profile keeps its share of the station's claim.
func capStationEvidence(used []Used, max float64) {
	sum := map[int64]float64{}
	for _, u := range used {
		sum[u.StationID] += u.Evidence
	}
	for i := range used {
		if s := sum[used[i].StationID]; s > max {
			used[i].Evidence *= max / s
		}
	}
}

// leaveOneOut is how many other anchors must have learned a coefficient before an anchor is held to them alone:
// fewer than three others have no majority of their own, so the anchor is held to the median of all anchors,
// which one bad anchor among three cannot move.
const leaveOneOut = 3

// reference is the anchors' position: per coefficient the median of the means of the anchors that learned it,
// leaving out the station exclude (0: none) where at least leaveOneOut others learned it. The band around it is
// ConsistencyK robust spreads wide. With fewer than three such anchors there is no spread to measure, and the
// floor stands in; with more, the spread is their median absolute deviation, which a minority of them cannot
// inflate. Where no anchor learned a coefficient, the reference is what the anchors sent for it (the prior they
// trained towards) and known is false: nobody is held to it, and the combination keeps it as it is.
func reference(used []Used, exclude int64, cfg config.Aggregation, nCoef int) (ref, band []float64, known []bool) {
	ref, band, known = make([]float64, nCoef), make([]float64, nCoef), make([]bool, nCoef)
	for j := range ref {
		others := func(u Used) bool { return u.Anchor && u.StationID != exclude && u.Kind.Informs(j) }
		all := func(u Used) bool { return u.Anchor && u.Kind.Informs(j) }
		col := stationMeans(used, j, others)
		if exclude != 0 && len(col) < leaveOneOut {
			col = stationMeans(used, j, all)
		}
		known[j] = len(col) > 0
		if !known[j] {
			col = stationMeans(used, j, func(u Used) bool { return u.Anchor })
		}
		ref[j] = median(col)
		spread := 0.0
		if len(col) >= 3 {
			dev := make([]float64, len(col))
			for i, v := range col {
				dev[i] = math.Abs(v - ref[j])
			}
			spread = 1.4826 * median(dev)
		}
		band[j] = cfg.ConsistencyK * math.Max(spread, cfg.ConsistencyFloor)
	}
	return ref, band, known
}

// stationMeans averages coefficient j as each station sent it over its profiles that pass keep, in station order.
func stationMeans(used []Used, j int, keep func(Used) bool) []float64 {
	sums, counts := map[int64]float64{}, map[int64]int{}
	var ids []int64
	for _, u := range used {
		if !keep(u) {
			continue
		}
		if counts[u.StationID] == 0 {
			ids = append(ids, u.StationID)
		}
		sums[u.StationID] += u.Kind.Coef[j]
		counts[u.StationID]++
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]float64, 0, len(ids))
	for _, id := range ids {
		out = append(out, sums[id]/float64(counts[id]))
	}
	return out
}

// demote finds the anchors that are far from the other anchors and takes their anchor status away for this
// combination, unless that would leave none. It reports whether anything changed.
func demote(used []Used, cfg config.Aggregation, nCoef int) bool {
	far := map[int64]bool{}
	for _, u := range used {
		if !u.Anchor || far[u.StationID] {
			continue
		}
		ref, band, known := reference(used, u.StationID, cfg, nCoef)
		probe := Used{Input: u.Input, Coef: append([]float64(nil), u.Kind.Coef...)}
		hold(&probe, ref, band, known)
		if probe.Far {
			far[u.StationID] = true
		}
	}
	if len(far) == 0 || len(far) >= countAnchors(used) {
		return false
	}
	for i := range used {
		if used[i].Anchor && far[used[i].StationID] {
			used[i].Anchor, used[i].Demoted = false, true
		}
	}
	return true
}

// disagreement counts the coefficients on which the two anchors differ by more than the band: with two there is
// no majority, and a reference between two positions that far apart would hold everyone to a place neither of
// them is at. Only coefficients both learned count.
func disagreement(used []Used, band []float64, known []bool) int {
	var a, b int64
	for _, u := range used {
		if !u.Anchor {
			continue
		}
		if a == 0 || u.StationID == a {
			a = u.StationID
		} else {
			b = u.StationID
		}
	}
	n := 0
	for j := range band {
		if !known[j] {
			continue
		}
		ma := stationMeans(used, j, func(u Used) bool { return u.StationID == a && u.Kind.Informs(j) })
		mb := stationMeans(used, j, func(u Used) bool { return u.StationID == b && u.Kind.Informs(j) })
		if len(ma) == 1 && len(mb) == 1 && math.Abs(ma[0]-mb[0]) > band[j] {
			n++
		}
	}
	return n
}

func finite(xs []float64) bool {
	for _, x := range xs {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return false
		}
	}
	return true
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	} else {
		return (s[n/2-1] + s[n/2]) / 2
	}
}

// hold pulls a profile's coefficients into the band around the reference and notes whether the profile was far:
// outside the band on farCount or more of the coefficients it learned. What a station claims beyond the band is
// never averaged in, so the influence of one contribution is bounded by the band, not by the coefficient bound of
// the specification. A coefficient no anchor learned (known false) holds nobody.
func hold(u *Used, ref, band []float64, known []bool) {
	u.Outside, u.Clipped, u.MaxDist = 0, 0, 0
	for j := range u.Coef {
		d := u.Kind.Coef[j] - ref[j]
		if !known[j] {
			continue
		}
		if u.Kind.Informs(j) {
			u.MaxDist = math.Max(u.MaxDist, math.Abs(d)/band[j])
		}
		if math.Abs(d) <= band[j] {
			continue
		}
		if u.Kind.Informs(j) {
			u.Outside++
		}
		u.Coef[j] = ref[j] + math.Copysign(band[j], d)
		u.Clipped++
	}
	u.Far = u.Outside >= farCount
}

// weigh gives each input its share in the combination. The anchors share 1 − NewcomerShare among themselves by
// their weight, capped per station; all other stations share NewcomerShare in equal parts, whatever they claim,
// so that neither many stations nor big claims buy a newcomer more than its equal part of a fifth. Within a
// station, the profiles split its share by their weight. The shares add up to 1.
func weigh(used []Used, cfg config.Aggregation) []float64 {
	weight, trust, anchor := map[int64]float64{}, map[int64]float64{}, map[int64]bool{}
	for _, u := range used {
		weight[u.StationID] += u.Evidence * u.Quality
		trust[u.StationID] = u.Trust
		anchor[u.StationID] = u.Anchor
	}
	var anchors, newcomers []int64
	for id := range weight {
		if anchor[id] {
			anchors = append(anchors, id)
		} else {
			newcomers = append(newcomers, id)
		}
	}
	sort.Slice(anchors, func(i, j int) bool { return anchors[i] < anchors[j] })
	sort.Slice(newcomers, func(i, j int) bool { return newcomers[i] < newcomers[j] })
	share := map[int64]float64{}
	newShare := 0.0
	switch {
	case len(anchors) == 0:
		newShare = 1 // Combine refuses this case; the shares still add up to 1
	case len(newcomers) > 0:
		newShare = cfg.NewcomerShare
	}
	for _, id := range newcomers {
		share[id] = newShare / float64(len(newcomers))
	}
	if len(anchors) > 0 {
		S, caps := make([]float64, len(anchors)), make([]float64, len(anchors))
		for i, id := range anchors {
			S[i], caps[i] = weight[id], cfg.MaxStationShare*trust[id]
		}
		for i, p := range waterfill(S, caps) {
			share[anchors[i]] = (1 - newShare) * p
		}
	}
	out := make([]float64, len(used))
	for i, u := range used {
		out[i] = share[u.StationID] * u.Evidence * u.Quality / weight[u.StationID]
	}
	return out
}

// waterfill turns the stations' weights S into shares that add up to 1 and stay below each station's cap: shares
// follow the weights, and whatever a capped station would get beyond its cap goes to the others in proportion.
// If the caps together are below 1 (few stations), every station is at its cap, and the shares follow the caps,
// that is the trust.
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
