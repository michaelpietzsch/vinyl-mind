package hub

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
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

// Combine builds a new candidate for every group whose contributions changed since its last model. With dryRun,
// nothing is stored and no trust changes.
func (h *Hub) Combine(now time.Time, dryRun bool) ([]Outcome, error) {
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

	var out []Outcome
	farByStation := map[int64]bool{}
	seen := map[int64]bool{}
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
			g := store.Group{FeatureVersion: b.fv, DetectorVersion: b.det, ChainClass: cls}
			o := Outcome{Group: g, Kinds: map[string]*Result{}}
			for _, kind := range v.Kinds {
				var in []Input
				for _, c := range scope {
					if k, ok := c.Kinds[kind]; ok {
						in = append(in, Input{StationID: c.StationID, ProfileID: c.ProfileID, Trust: c.Trust, Kind: k})
					}
				}
				if len(in) == 0 {
					continue
				}
				r := Combine(in, h.cfg.Aggregation, v.NCoef())
				o.Kinds[kind] = r
				// Trust is judged on the model over all classes only, so that a profile is not counted twice
				if r.Coef != nil && cls == spec.ClassAll {
					for _, u := range r.Used {
						seen[u.StationID] = true
						if far(u.Kind.Coef, r.Coef, r.Spread, h.cfg.Aggregation.ConsistencyK) {
							farByStation[u.StationID] = true
						}
					}
				}
			}
			o.Version, o.Skipped, err = h.candidate(v, g, scope, o.Kinds, now, dryRun)
			if err != nil {
				return out, err
			}
			out = append(out, o)
		}
	}
	if !dryRun {
		if err := h.adjustTrust(seen, farByStation, now); err != nil {
			return out, err
		}
	}
	return out, nil
}

// inputsHash identifies the contributions behind a model together with the settings that combine them: if both
// are the same as for the last model, a new one would be the same model again.
func (h *Hub) inputsHash(scope []store.Contribution) string {
	ids := make([]string, len(scope))
	for i, c := range scope {
		ids[i] = strconv.FormatInt(c.ID, 10)
	}
	sort.Strings(ids)
	settings, _ := json.Marshal(h.cfg.Aggregation)
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%v", settings, ids))
	return hex.EncodeToString(sum[:])
}

func (h *Hub) candidate(v *spec.Version, g store.Group, scope []store.Contribution, kinds map[string]*Result,
	now time.Time, dryRun bool) (int64, string, error) {
	model := map[string]spec.KindModel{}
	for kind, r := range kinds {
		if r.Coef == nil {
			continue
		}
		model[kind] = spec.KindModel{Coef: round(r.Coef), Spread: round(r.Spread), Profiles: r.Profiles,
			Stations: r.Stations, Examples: math.Round(r.Examples*100) / 100}
	}
	if len(model) == 0 {
		return 0, "not enough usable contributions", nil
	}
	hash := h.inputsHash(scope)
	last, err := h.store.LatestInputsHash(g)
	if err != nil {
		return 0, "", err
	}
	if last == hash {
		return 0, "unchanged since the last model", nil
	}
	if dryRun {
		return 0, "dry run", nil
	}
	a := h.cfg.Aggregation
	build := func(version int64) ([]byte, string, error) {
		gm := spec.GlobalModel{
			Spec: "vinyl-mind/global-model", Version: version, FeatureVersion: g.FeatureVersion,
			DetectorVersion: g.DetectorVersion, ChainClass: g.ChainClass, CreatedAt: now.Unix(), Server: h.fp,
			Features: v.Names(), Scaling: spec.Scaling{Center: v.Center(), Scale: v.Scale()},
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
	version, err := h.store.AddModel(g, hash, build, now)
	if err != nil {
		return 0, "", err
	}
	h.log.Info("new candidate", "version", version, "feature_version", g.FeatureVersion,
		"detector_version", g.DetectorVersion, "chain_class", g.ChainClass)
	return version, "", nil
}

func round(xs []float64) []float64 {
	out := make([]float64, len(xs))
	for i, x := range xs {
		out[i] = math.Round(x*1e6) / 1e6
	}
	return out
}

// adjustTrust lowers the trust of stations with a contribution far from the consensus at once, and raises the
// trust of consistent stations at most once per day, so that trust cannot be farmed by sending often.
func (h *Hub) adjustTrust(seen, farAway map[int64]bool, now time.Time) error {
	if len(seen) == 0 {
		return nil
	}
	stations, err := h.store.Stations()
	if err != nil {
		return err
	}
	a := h.cfg.Aggregation
	for _, st := range stations {
		if !seen[st.ID] {
			continue
		}
		if farAway[st.ID] {
			t := roundTrust(math.Max(a.TrustMin, st.Trust-a.TrustPenalty))
			if t != st.Trust {
				h.log.Info("trust lowered", "station", st.KeyFP, "trust", t)
				if err := h.store.SetTrust(st.ID, t, nil); err != nil {
					return err
				}
			}
			continue
		}
		if err := h.gainTrust(&st, now); err != nil {
			return err
		}
	}
	return nil
}

func (h *Hub) gainTrust(st *store.Station, now time.Time) error {
	if st.Trust >= 1 || (st.TrustGainedAt != nil && now.Sub(*st.TrustGainedAt) < 24*time.Hour) {
		return nil
	}
	return h.store.SetTrust(st.ID, roundTrust(math.Min(1, st.Trust+h.cfg.Aggregation.TrustStep)), &now)
}

func roundTrust(t float64) float64 { return math.Round(t*1000) / 1000 }
