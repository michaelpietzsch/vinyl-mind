package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"

	"github.com/michaelpietzsch/vinyl-mind/internal/auth"
	"github.com/michaelpietzsch/vinyl-mind/internal/hub"
	"github.com/michaelpietzsch/vinyl-mind/internal/keys"
	"github.com/michaelpietzsch/vinyl-mind/internal/store"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

var (
	profileRE = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
	tokenRE   = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,31}$`)
	classRE   = regexp.MustCompile(`^(all|(usb_turntable|turntable_interface)\.(mm|mc|unknown))$`)
)

func profileID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("profile")
	if !profileRE.MatchString(id) {
		writeError(w, http.StatusBadRequest, "bad_profile_id",
			"profile ID must be 16 to 64 characters of A-Z, a-z, 0-9, _ and -")
		return "", false
	}
	return id, true
}

// ─── Contributions ─────────────────────────────────────────────────────────

func (s *Server) putContribution(w http.ResponseWriter, r *http.Request, st *store.Station, body []byte) {
	id, ok := profileID(w, r)
	if !ok {
		return
	}
	var c spec.Contribution
	v, err := spec.Validate(spec.DocContribution, body, &c)
	if err != nil {
		if !specError(w, err) {
			s.internal(w, err)
		}
		return
	}
	if c.Client.Name+"/"+c.Client.Version != r.Header.Get(auth.HClient) {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "client: does not match the Vinyl-Mind-Client header")
		return
	}
	if !s.cfg.Clients.AcceptsDetector(c.DetectorVersion) {
		writeError(w, http.StatusUnprocessableEntity, "detector_version_not_accepted",
			"this server does not accept detector version "+c.DetectorVersion)
		return
	}
	if !slices.Contains(s.cfg.FeatureVersions, v.Number) {
		writeError(w, http.StatusUnprocessableEntity, "feature_version_not_accepted",
			"this server does not accept feature version "+strconv.Itoa(v.Number))
		return
	}
	now := s.now()
	changed, err := s.store.PutContribution(st.ID, id, &c, s.cfg.MaxProfilesPerStation, now)
	if errors.Is(err, store.ErrTooManyProfiles) {
		writeError(w, http.StatusConflict, "too_many_profiles", "this station has the most profiles the server accepts")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile_id": id, "changed": changed, "received_at": now.Unix()})
}

func (s *Server) deleteContribution(w http.ResponseWriter, r *http.Request, st *store.Station, _ []byte) {
	id, ok := profileID(w, r)
	if !ok {
		return
	}
	found, err := s.store.DeleteContribution(st.ID, id)
	if err != nil {
		s.internal(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "no contribution for this profile")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Models ────────────────────────────────────────────────────────────────

// entry is what a station sees of a model. Besides the signed model it carries the hub's signed statement about
// the model's status (state_jws), so that a status cannot be changed on the way without the server key.
func (s *Server) entry(m *store.Model) map[string]any {
	if m == nil {
		return nil
	}
	state, _ := json.Marshal(map[string]any{
		"spec": "vinyl-mind/model-state", "server": s.fp, "version": m.Version, "status": m.Status,
		"published_at": unix(m.PublishedAt), "withdrawn_at": unix(m.WithdrawnAt), "at": s.now().Unix(),
	})
	e := map[string]any{
		"state_jws": keys.JWSSignTyped(s.key, keys.StateType, state),
		"version":   m.Version, "status": m.Status, "feature_version": m.Group.FeatureVersion,
		"detector_version": m.Group.DetectorVersion, "chain_class": m.Group.ChainClass,
		"created_at": m.CreatedAt.Unix(), "published_at": unix(m.PublishedAt), "withdrawn_at": unix(m.WithdrawnAt),
	}
	if m.Reason != "" {
		e["reason"] = m.Reason
	}
	if m.Status != store.Withdrawn {
		e["jws"] = m.JWS
	}
	return e
}

// pick returns the model with the status for the station's chain class, or for all classes if its class has
// none: the newest candidate, or the model published last, as long as it is still published (a withdrawal does
// not hand the place back to an older model).
func (s *Server) pick(g store.Group, status string) (*store.Model, error) {
	for _, class := range []string{g.ChainClass, spec.ClassAll} {
		g.ChainClass = class
		var m *store.Model
		var err error
		if status == store.Published {
			m, err = s.store.Published(g)
		} else {
			m, err = s.store.Latest(g, status)
		}
		if err != nil || m != nil {
			return m, err
		}
	}
	return nil, nil
}

// models answers what a station polls for: the newest published model and the current candidate to check. The
// ETag changes with either, so polling usually costs a 304 and no payload.
func (s *Server) models(w http.ResponseWriter, r *http.Request, _ *store.Station, _ []byte) {
	q := r.URL.Query()
	fv, err := strconv.Atoi(q.Get("feature_version"))
	det, class := q.Get("detector_version"), q.Get("chain_class")
	if class == "" {
		class = spec.ClassAll
	}
	if err != nil || !tokenRE.MatchString(det) || !classRE.MatchString(class) {
		writeError(w, http.StatusBadRequest, "bad_query",
			"need feature_version, detector_version and optionally chain_class")
		return
	}
	g := store.Group{FeatureVersion: fv, DetectorVersion: det, ChainClass: class}
	pub, err := s.pick(g, store.Published)
	if err != nil {
		s.internal(w, err)
		return
	}
	cand, err := s.pick(g, store.Candidate)
	if err != nil {
		s.internal(w, err)
		return
	}
	tag := `"m`
	for _, m := range []*store.Model{pub, cand} {
		if m == nil {
			tag += "-0"
		} else {
			tag += "-" + strconv.FormatInt(m.Version, 10)
		}
	}
	tag += `"`
	w.Header().Set("ETag", tag)
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"published": s.entry(pub), "candidate": s.entry(cand)})
}

func (s *Server) model(w http.ResponseWriter, r *http.Request, _ *store.Station, _ []byte) {
	version, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "bad_version", "version must be a positive integer")
		return
	}
	m, err := s.store.Model(version)
	if err != nil {
		s.internal(w, err)
		return
	}
	if m == nil {
		writeError(w, http.StatusNotFound, "not_found", "no such model")
		return
	}
	writeJSON(w, http.StatusOK, s.entry(m))
}

// ─── Check reports ─────────────────────────────────────────────────────────

func (s *Server) putReport(w http.ResponseWriter, r *http.Request, st *store.Station, body []byte) {
	id, ok := profileID(w, r)
	if !ok {
		return
	}
	version, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "bad_version", "version must be a positive integer")
		return
	}
	m, err := s.store.Model(version)
	if err != nil {
		s.internal(w, err)
		return
	}
	if m == nil {
		writeError(w, http.StatusNotFound, "not_found", "no such model")
		return
	}
	if m.Status == store.Withdrawn {
		writeError(w, http.StatusConflict, "withdrawn", "this model has been withdrawn")
		return
	}
	var rep spec.CheckReport
	v, err := spec.Validate(spec.DocCheckReport, body, &rep)
	if err != nil {
		if !specError(w, err) {
			s.internal(w, err)
		}
		return
	}
	var gm spec.GlobalModel
	if err := json.Unmarshal([]byte(m.Payload), &gm); err != nil {
		s.internal(w, err)
		return
	}
	if v.Number != m.Group.FeatureVersion {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "feature_version differs from the model's")
		return
	}
	for kind := range rep.Kinds {
		if _, ok := gm.Kinds[kind]; !ok {
			writeError(w, http.StatusUnprocessableEntity, "invalid", "kinds/"+kind+": not in this model")
			return
		}
	}
	known, err := s.store.StationProfiles(st.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	if !known[id] && len(known) >= s.cfg.MaxProfilesPerStation {
		writeError(w, http.StatusConflict, "too_many_profiles", "this station has the most profiles the server accepts")
		return
	}
	now := s.now()
	verdict := hub.Verdict(rep, s.cfg.Publication.Tolerance)
	if err := s.store.PutReport(store.Report{StationID: st.ID, ProfileID: id, ModelVersion: version,
		Adopted: rep.Adopted, Kinds: rep.Kinds, Verdict: verdict, ReceivedAt: now}); err != nil {
		s.internal(w, err)
		return
	}
	if err := s.hub.Review(version, now); err != nil {
		s.log.Error("review", "version", version, "err", err)
	}
	status := m.Status
	if after, err := s.store.Model(version); err == nil && after != nil {
		status = after.Status
	}
	writeJSON(w, http.StatusOK, map[string]any{"verdict": verdict, "model_status": status})
}
