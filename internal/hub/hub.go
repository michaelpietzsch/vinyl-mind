package hub

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/config"
	"github.com/michaelpietzsch/vinyl-mind/internal/keys"
	"github.com/michaelpietzsch/vinyl-mind/internal/store"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

// Hub combines, publishes and withdraws models.
type Hub struct {
	cfg   config.Config
	store *store.Store
	key   ed25519.PrivateKey
	fp    string
	log   *slog.Logger
}

// New returns a hub.
func New(cfg config.Config, st *store.Store, key ed25519.PrivateKey, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{cfg: cfg, store: st, key: key, fp: keys.Fingerprint(key.Public().(ed25519.PublicKey)), log: log}
}

// Outcome is what one combine run did for one group.
type Outcome struct {
	Group   store.Group
	Version int64 // new candidate, 0 if none
	Skipped string
	Kinds   map[string]*Result
}

// pass is one combination of everything: the results per group and what they say about the stations.
type pass struct {
	groups []groupResult
	seen   map[int64]bool // stations used in a combination over all classes
	far    map[int64]bool // stations with a contribution far from the reference
}

type groupResult struct {
	v     *spec.Version
	g     store.Group
	kinds map[string]*Result
}

// Combine builds a new candidate for every group whose combination changed since its last model. It combines
// twice: the first pass judges every contribution against the reference and settles the trust, the second builds
// the candidates with the settled trust, so that a station caught far from the reference does not shape the model
// it was caught by. With dryRun, nothing is stored and no trust changes.
func (h *Hub) Combine(now time.Time, dryRun bool) ([]Outcome, error) {
	p, err := h.combineAll()
	if err != nil {
		return nil, err
	}
	if !dryRun {
		if err := h.adjustTrust(p, now); err != nil {
			return nil, err
		}
		if p, err = h.combineAll(); err != nil {
			return nil, err
		}
	}
	var out []Outcome
	for _, gr := range p.groups {
		o := Outcome{Group: gr.g, Kinds: gr.kinds}
		o.Version, o.Skipped, err = h.candidate(gr, now, dryRun)
		if err != nil {
			return out, err
		}
		out = append(out, o)
	}
	if !dryRun {
		if err := h.store.SetMeta("last_combine_at", strconv.FormatInt(now.Unix(), 10)); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (h *Hub) combineAll() (*pass, error) {
	cs, err := h.store.ActiveContributions()
	if err != nil {
		return nil, err
	}
	type base struct {
		fv  int
		det string
	}
	bases := map[base][]store.Contribution{}
	for _, c := range cs {
		if slices.Contains(h.cfg.FeatureVersions, c.FeatureVersion) {
			b := base{c.FeatureVersion, c.DetectorVersion}
			bases[b] = append(bases[b], c)
		}
	}
	keysSorted := make([]base, 0, len(bases))
	for b := range bases {
		keysSorted = append(keysSorted, b)
	}
	sort.Slice(keysSorted, func(i, j int) bool {
		if keysSorted[i].fv != keysSorted[j].fv {
			return keysSorted[i].fv < keysSorted[j].fv
		}
		return keysSorted[i].det < keysSorted[j].det
	})
	p := &pass{seen: map[int64]bool{}, far: map[int64]bool{}}
	for _, b := range keysSorted {
		v, err := spec.Get(b.fv)
		if err != nil {
			return nil, err
		}
		all := bases[b]
		perClass := map[string][]store.Contribution{}
		for _, c := range all {
			perClass[c.ChainClass] = append(perClass[c.ChainClass], c)
		}
		classes := []string{spec.ClassAll}
		for cls, list := range perClass {
			if len(list) >= h.cfg.Aggregation.MinClassProfiles {
				classes = append(classes, cls)
			}
		}
		sort.Strings(classes[1:])
		for _, cls := range classes {
			scope := all
			if cls != spec.ClassAll {
				scope = perClass[cls]
			}
			gr := groupResult{v: v, g: store.Group{FeatureVersion: b.fv, DetectorVersion: b.det, ChainClass: cls},
				kinds: map[string]*Result{}}
			for _, kind := range v.Kinds {
				var in []Input
				for _, c := range scope {
					if k, ok := c.Kinds[kind]; ok {
						in = append(in, Input{StationID: c.StationID, ProfileID: c.ProfileID, Account: c.Account,
							Trust: c.Trust, Anchor: h.anchor(c.Trust, c.TrustOperator), Kind: k})
					}
				}
				if len(in) == 0 {
					continue
				}
				r := Combine(in, h.cfg.Aggregation, v.NCoef())
				gr.kinds[kind] = r
				// Trust is judged on the model over all classes only, so that a profile is not counted twice
				if r.Coef != nil && cls == spec.ClassAll {
					for _, u := range r.Used {
						p.seen[u.StationID] = true
						if u.Far {
							p.far[u.StationID] = true
						}
					}
				}
			}
			p.groups = append(p.groups, gr)
		}
	}
	return p, nil
}

// anchor says whether a station is one of the operator's: trust given by the operator, at the full level.
func (h *Hub) anchor(trust float64, operator bool) bool {
	return operator && trust >= h.cfg.Aggregation.NewcomerTrust
}

// resultHash identifies a combination by what it produces: the rounded coefficients, spreads and counts per kind,
// with the settings that produced them. Two combinations with the same hash would be the same model, so the second
// is not stored; a changed contribution that changes nothing (left out, or pulled to the same place) does not
// churn out candidates either.
func (h *Hub) resultHash(kinds map[string]*Result) string {
	type summary struct {
		Coef, Spread []float64
		Basis        []int
		Profiles     int
		Stations     int
		Examples     float64
	}
	sums := map[string]summary{}
	for kind, r := range kinds {
		if r.Coef != nil {
			sums[kind] = summary{round(r.Coef), round(r.Spread), r.Basis, r.Profiles, r.Stations, math.Round(r.Examples)}
		}
	}
	body, _ := json.Marshal(sums)
	settings, _ := json.Marshal(h.cfg.Aggregation)
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%s", settings, body))
	return hex.EncodeToString(sum[:])
}

// candidateTolerance is how far a new combination may differ from the candidate under review and still count as
// the same one: the reports already given keep their meaning, and nobody pushes a candidate out by nudging a
// contribution every hour.
const candidateTolerance = 0.02

func (h *Hub) candidate(gr groupResult, now time.Time, dryRun bool) (int64, string, error) {
	model := map[string]spec.KindModel{}
	for kind, r := range gr.kinds {
		if r.Coef == nil {
			continue
		}
		model[kind] = spec.KindModel{Coef: round(r.Coef), Spread: round(r.Spread), Profiles: r.Profiles,
			Stations: r.Stations, Examples: math.Round(r.Examples*100) / 100}
	}
	if len(model) == 0 {
		reason := "not enough usable contributions"
		for _, kind := range gr.v.Kinds {
			if r := gr.kinds[kind]; r != nil && r.Reason != "" {
				reason = r.Reason
				break
			}
		}
		return 0, reason, nil
	}
	hash := h.resultHash(gr.kinds)
	last, err := h.store.LatestResultHash(gr.g)
	if err != nil {
		return 0, "", err
	}
	if last == hash {
		return 0, "unchanged since the last model", nil
	}
	current, err := h.store.Latest(gr.g, store.Candidate)
	if err != nil {
		return 0, "", err
	}
	if current != nil {
		if age := now.Sub(current.CreatedAt); age < h.reviewWindow() {
			return 0, fmt.Sprintf("candidate %d under review for another %s", current.Version,
				(h.reviewWindow() - age).Round(time.Minute)), nil
		}
		close, err := within(current.Payload, model, candidateTolerance)
		if err != nil {
			return 0, "", err
		}
		if close {
			return 0, fmt.Sprintf("differs from candidate %d only within tolerance", current.Version), nil
		}
	}
	if dryRun {
		return 0, "dry run", nil
	}
	a := h.cfg.Aggregation
	build := func(version int64) ([]byte, string, error) {
		gm := spec.GlobalModel{
			Spec: "vinyl-mind/global-model", Version: version, FeatureVersion: gr.g.FeatureVersion,
			DetectorVersion: gr.g.DetectorVersion, ChainClass: gr.g.ChainClass, CreatedAt: now.Unix(), Server: h.fp,
			Features: gr.v.Names(), Scaling: spec.Scaling{Center: gr.v.Center(), Scale: gr.v.Scale()},
			Aggregation: spec.AggregationInfo{Method: a.Method, Trim: a.Trim, ManualWeight: a.ManualWeight,
				MaxEstError: a.MaxEstError, MaxStationShare: a.MaxStationShare},
			Kinds: model,
		}
		payload, err := json.Marshal(gm)
		if err != nil {
			return nil, "", err
		}
		// What leaves the server must pass the same checks a station applies
		if _, err := spec.Validate(spec.DocGlobalModel, payload, &spec.GlobalModel{}); err != nil {
			return nil, "", fmt.Errorf("built model does not meet the specification: %w", err)
		}
		return payload, keys.JWSSign(h.key, payload), nil
	}
	version, err := h.store.AddModel(gr.g, hash, contributorsOf(gr.kinds), build, now)
	if err != nil {
		return 0, "", err
	}
	h.log.Info("new candidate", "version", version, "feature_version", gr.g.FeatureVersion,
		"detector_version", gr.g.DetectorVersion, "chain_class", gr.g.ChainClass)
	h.event(now, "server", "candidate", fmt.Sprint(version), fmt.Sprintf("feature %d, detector %s, class %s",
		gr.g.FeatureVersion, gr.g.DetectorVersion, gr.g.ChainClass))
	return version, "", nil
}

// within reports whether a stored model's coefficients differ from the new ones by at most tol everywhere.
func within(payload string, model map[string]spec.KindModel, tol float64) (bool, error) {
	var old spec.GlobalModel
	if err := json.Unmarshal([]byte(payload), &old); err != nil {
		return false, err
	}
	if len(old.Kinds) != len(model) {
		return false, nil
	}
	for kind, m := range model {
		o, ok := old.Kinds[kind]
		if !ok || len(o.Coef) != len(m.Coef) {
			return false, nil
		}
		for j := range m.Coef {
			if math.Abs(o.Coef[j]-m.Coef[j]) > tol {
				return false, nil
			}
		}
	}
	return true, nil
}

// contributorsOf records the stations and accounts behind a model, so that they cannot vote for it.
func contributorsOf(kinds map[string]*Result) string {
	stations, accounts := map[int64]bool{}, map[string]bool{}
	for _, r := range kinds {
		for _, u := range r.Used {
			stations[u.StationID] = true
			if u.Account != "" {
				accounts[u.Account] = true
			}
		}
	}
	c := Contributors{Stations: slices.Sorted(maps.Keys(stations)), Accounts: slices.Sorted(maps.Keys(accounts))}
	b, _ := json.Marshal(c)
	return string(b)
}

func round(xs []float64) []float64 {
	out := make([]float64, len(xs))
	for i, x := range xs {
		out[i] = math.Round(x*1e6) / 1e6
	}
	return out
}

// adjustTrust applies what a pass found. Trust the operator gave never changes here: an anchor far from the other
// anchors is reported, not demoted, so that no group of stations can push the reference aside. Automatic trust
// falls by TrustPenalty for a contribution far from the reference, at most once a day and only for a contribution
// that changed since the last penalty, and rises by TrustStep for a consistent one, at most once a day and never
// beyond TrustAutoMax: the full level stays the operator's to give.
func (h *Hub) adjustTrust(p *pass, now time.Time) error {
	if len(p.seen) == 0 {
		return nil
	}
	stations, err := h.store.Stations()
	if err != nil {
		return err
	}
	a := h.cfg.Aggregation
	for _, st := range stations {
		if !p.seen[st.ID] {
			continue
		}
		if st.TrustOperator {
			if p.far[st.ID] {
				h.log.Warn("anchor far from the other anchors", "station", st.KeyFP)
				h.event(now, "review", "anchor_far", st.KeyFP,
					"contribution far from the other operator-trusted stations; trust unchanged")
			}
			continue
		}
		if p.far[st.ID] {
			if st.TrustPenalty != nil && (now.Sub(*st.TrustPenalty) < 24*time.Hour || st.LastContrib == nil ||
				!st.LastContrib.After(*st.TrustPenalty)) {
				continue
			}
			t := roundTrust(math.Max(a.TrustMin, st.Trust-a.TrustPenalty))
			if t != st.Trust {
				if err := h.store.PenalizeTrust(st.ID, t, now); err != nil {
					return err
				}
				h.log.Info("trust lowered", "station", st.KeyFP, "trust", t)
				h.event(now, "review", "trust_lowered", st.KeyFP,
					fmt.Sprintf("%.3f to %.3f: contribution far from the reference", st.Trust, t))
			}
			continue
		}
		if err := h.gainTrust(&st, now, "consistent contribution"); err != nil {
			return err
		}
	}
	return nil
}

// gainTrust raises automatic trust by one step, at most once a day and up to TrustAutoMax.
func (h *Hub) gainTrust(st *store.Station, now time.Time, why string) error {
	a := h.cfg.Aggregation
	if st.TrustOperator || st.Trust >= a.TrustAutoMax ||
		(st.TrustGainedAt != nil && now.Sub(*st.TrustGainedAt) < 24*time.Hour) {
		return nil
	}
	t := roundTrust(math.Min(a.TrustAutoMax, st.Trust+a.TrustStep))
	if err := h.store.SetTrust(st.ID, t, now); err != nil {
		return err
	}
	h.log.Info("trust raised", "station", st.KeyFP, "trust", t, "why", why)
	h.event(now, "review", "trust_raised", st.KeyFP, fmt.Sprintf("%.3f to %.3f: %s", st.Trust, t, why))
	return nil
}

func roundTrust(t float64) float64 { return math.Round(t*1000) / 1000 }
