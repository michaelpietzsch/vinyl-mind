package api

import (
	"errors"
	"net/http"
	"regexp"
	"sort"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/auth"
	"github.com/michaelpietzsch/vinyl-mind/internal/store"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

var invitationRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

type joinRequest struct {
	PublicKey  string  `json:"public_key"`
	Invitation *string `json:"invitation,omitempty"`
}

// join registers a station. The request is signed with the new key itself, which proves that the station holds the
// private key to the public key it registers.
func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	if !s.joins.Allow(info(r).addr.String(), now) {
		w.Header().Set("Retry-After", "3600")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many joins from this address")
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req joinRequest
	if err := spec.StrictJSON(body, &req); err != nil {
		specError(w, err)
		return
	}
	if req.Invitation != nil && !invitationRE.MatchString(*req.Invitation) {
		writeError(w, http.StatusForbidden, "invitation_invalid", "invitation code is not valid")
		return
	}
	sig, err := auth.Parse(r.Header, now, s.cfg.ClockSkew())
	if err != nil {
		authError(w, err)
		return
	}
	if err := sig.Verify(req.PublicKey, s.fp, r.Method, r.RequestURI, body); err != nil {
		authError(w, err)
		return
	}
	info(r).keyFP = sig.KeyFP
	if !s.clientAccepted(w, r) {
		return
	}
	if fresh, err := s.store.UseNonce(sig.KeyFP, sig.Nonce, sig.Time.Add(s.cfg.ClockSkew()+time.Minute)); err != nil {
		s.internal(w, err)
		return
	} else if !fresh {
		writeError(w, http.StatusUnauthorized, "replayed", "nonce already used")
		return
	}
	existing, err := s.store.Station(sig.KeyFP)
	if err != nil {
		s.internal(w, err)
		return
	}
	if existing == nil {
		// A ceiling for the whole server, whatever hands out the invitations: a flood of new stations waits
		n, err := s.store.JoinsSince(now.Add(-24 * time.Hour))
		if err != nil {
			s.internal(w, err)
			return
		}
		if n >= s.cfg.JoinsPerDay {
			w.Header().Set("Retry-After", "3600")
			writeError(w, http.StatusTooManyRequests, "join_limit", "the server admits no more new stations today")
			return
		}
	}
	st, err := s.store.Join(sig.KeyFP, req.PublicKey, req.Invitation, s.cfg.JoinMode == "approval",
		s.cfg.Aggregation.TrustStart, now)
	switch {
	case errors.Is(err, store.ErrInvitationInvalid):
		writeError(w, http.StatusForbidden, "invitation_invalid", "invitation code is not valid")
		return
	case errors.Is(err, store.ErrInvitationRequired):
		writeError(w, http.StatusForbidden, "invitation_required", "this server only admits stations with an invitation")
		return
	case err != nil:
		s.internal(w, err)
		return
	}
	if st.Status == store.Revoked {
		writeError(w, http.StatusForbidden, "revoked", "this station's key has been revoked")
		return
	}
	status := http.StatusCreated
	if existing != nil {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"station": st.KeyFP, "status": st.Status, "server": s.fp})
}

// me reports the station's status and what the server holds from it: everything it may see, nothing about others.
func (s *Server) me(w http.ResponseWriter, _ *http.Request, st *store.Station, _ []byte) {
	cs, err := s.store.StationContributions(st.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	reports, err := s.store.Reports(0, st.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	profiles := []map[string]any{}
	for _, c := range cs {
		kinds := make([]string, 0, len(c.Kinds))
		for k := range c.Kinds {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		profiles = append(profiles, map[string]any{
			"profile_id": c.ProfileID, "feature_version": c.FeatureVersion, "detector_version": c.DetectorVersion,
			"chain_class": c.ChainClass, "kinds": kinds, "received_at": c.ReceivedAt.Unix(),
		})
	}
	rs := []map[string]any{}
	for _, r := range reports {
		rs = append(rs, map[string]any{"profile_id": r.ProfileID, "model_version": r.ModelVersion,
			"verdict": r.Verdict, "adopted": r.Adopted, "received_at": r.ReceivedAt.Unix()})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"station": st.KeyFP, "status": st.Status, "joined_at": st.JoinedAt.Unix(), "approved_at": unix(st.ApprovedAt),
		"profiles": profiles, "reports": rs,
	})
}

// leave deletes the station and everything it sent. Global models already built stay: they are combinations and
// hold nothing that belongs to one station alone.
func (s *Server) leave(w http.ResponseWriter, _ *http.Request, st *store.Station, _ []byte) {
	if err := s.store.Leave(st); err != nil {
		s.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
