package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/auth"
	"github.com/michaelpietzsch/vinyl-mind/internal/binding"
	"github.com/michaelpietzsch/vinyl-mind/internal/store"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

var invitationRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

type joinRequest struct {
	PublicKey  string  `json:"public_key"`
	Invitation *string `json:"invitation,omitempty"`
	Binding    *string `json:"binding,omitempty"` // from an account service, docs/connect.md
}

// errBindingInvalid stands for every failed check of a binding; the reason goes to the log, not to the client.
var errBindingInvalid = errors.New("binding invalid")

// join registers a station. The request is signed with the new key itself, which proves that the station holds the
// private key to the public key it registers.
func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	if !s.joins.Allow(limitKey(info(r).addr), now) {
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
	if req.Binding != nil && req.Invitation == nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "a binding belongs to an invitation")
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
	// Runs inside the join, before the invitation is used up: a refused binding leaves the code as it was
	check := func(invitationID int64, bound bool) (string, error) {
		if req.Binding == nil {
			if bound {
				return "", store.ErrBindingRequired
			}
			return "", nil
		}
		p, err := binding.Verify(*req.Binding, s.cfg.BindingKeys(),
			binding.Audience{URL: s.cfg.Accounts.Audience, Required: s.cfg.Accounts.AudienceRequired}, now,
			s.cfg.ClockSkew())
		switch {
		case err != nil:
			s.log.Warn("binding refused", "station", sig.KeyFP, "reason", err)
			return "", errBindingInvalid
		case p.Station != sig.KeyFP:
			s.log.Warn("binding refused", "station", sig.KeyFP, "reason", "names another station")
			return "", errBindingInvalid
		case p.InvitationID != invitationID:
			s.log.Warn("binding refused", "station", sig.KeyFP, "reason", "names another invitation")
			return "", errBindingInvalid
		}
		return p.Account, nil
	}
	// JoinsPerDay is a ceiling for the whole server, whatever hands out the invitations: a flood of new stations
	// waits. It is counted inside the join, so that parallel joins cannot slip past it together.
	st, err := s.store.Join(sig.KeyFP, req.PublicKey, req.Invitation, s.cfg.JoinMode == "approval",
		s.cfg.Aggregation.TrustStart, check, s.cfg.JoinsPerDay, now)
	switch {
	case errors.Is(err, store.ErrJoinLimit):
		w.Header().Set("Retry-After", "3600")
		writeError(w, http.StatusTooManyRequests, "join_limit", "the server admits no more new stations today")
		return
	case errors.Is(err, store.ErrBindingRequired):
		writeError(w, http.StatusForbidden, "binding_required", "this invitation is valid only with a binding")
		return
	case errors.Is(err, errBindingInvalid):
		writeError(w, http.StatusForbidden, "binding_invalid", "the binding does not fit this station and invitation")
		return
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
	} else {
		detail := "status " + st.Status
		if st.InvitationID != nil {
			detail += fmt.Sprintf(", invitation %d", *st.InvitationID)
		}
		if st.Account != "" {
			detail += ", account " + st.Account
		}
		s.log.Info("station joined", "station", st.KeyFP, "status", st.Status, "address", info(r).addr)
		s.event(now, "join", st.KeyFP, detail)
	}
	writeJSON(w, status, map[string]any{"station": st.KeyFP, "status": st.Status, "server": s.fp})
}

func (s *Server) event(now time.Time, action, subject, detail string) {
	if err := s.store.AddEvent(store.Event{T: now, Actor: "server", Action: action, Subject: subject,
		Detail: detail}); err != nil {
		s.log.Error("event log", "err", err)
	}
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
func (s *Server) leave(w http.ResponseWriter, r *http.Request, st *store.Station, _ []byte) {
	now := s.now()
	if err := s.store.Leave(st, now); err != nil {
		s.internal(w, err)
		return
	}
	s.log.Info("station left", "station", st.KeyFP, "address", info(r).addr)
	s.event(now, "leave", st.KeyFP, fmt.Sprintf("trust %.3f, %d profiles", st.Trust, st.Profiles))
	w.WriteHeader(http.StatusNoContent)
}
