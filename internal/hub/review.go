package hub

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/store"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

// Verdict judges one check report: the global model counts as worse for the profile when its estimate error,
// averaged over the reported kinds, exceeds the local model's by more than the tolerance.
func Verdict(r spec.CheckReport, tolerance float64) string {
	diff := 0.0
	for _, k := range r.Kinds {
		diff += k.GlobalErr - k.LocalErr
	}
	if diff/float64(len(r.Kinds)) > tolerance {
		return store.Worse
	}
	return store.NotWorse
}

// Tally counts the reports of voting stations on one model. Each station has one vote, the majority of its
// profiles' verdicts (a tie is no vote), so that a station cannot outvote the others by reporting many profiles.
// Only active stations that joined at least MinVoteAgeDays ago vote. What decides is the anchors' weight: the
// stations the operator trusts. Everyone else's vote is counted and shown, but a report is only a claim, and
// claims from stations nobody vouches for cannot publish a model or take one away. A station whose contribution
// is in the model, or that shares an account with one, cannot vote for it; it can vote against it. An anchor
// that the last combination found far from the other anchors, or that was reported far within the last day
// (anchor_far, anchor_demoted), decides nothing: its word is in doubt until the others agree with it again.
type Tally struct {
	Reports        int     // reporting profiles of voting stations
	Stations       int     // voting stations
	Worse          int     // stations that found the model worse
	NotWorse       int     // stations that found it not worse
	WorseWeight    float64 // their trust, summed
	NotWorseWeight float64
	AnchorWorse    float64 // the anchors' trust among them: what decides
	AnchorNotWorse float64
}

// Contributors is what a model records about the stations behind it (column contributors).
type Contributors struct {
	Stations []int64  `json:"stations"`
	Accounts []string `json:"accounts,omitempty"`
}

// Review applies the rules of docs/aggregation.md after a new check report or when a candidate has reached the
// review age. With reports from enough profiles and stations, a model that the anchors find worse is withdrawn,
// and a candidate that the anchors find not worse is published once it is ReviewHours old, so that every station
// that wants to has had time to check it. Ties wait for the next report.
func (h *Hub) Review(version int64, now time.Time) error {
	if !h.cfg.Publication.Auto {
		return nil
	}
	m, err := h.store.Model(version)
	if err != nil || m == nil || m.Status == store.Withdrawn {
		return err
	}
	t, votes, err := h.tally(m, now)
	if err != nil || t.Reports < h.cfg.Publication.MinReports || t.Stations < h.cfg.Publication.MinReportStations {
		return err
	}
	decided := ""
	switch {
	case t.AnchorWorse > t.AnchorNotWorse:
		reason := fmt.Sprintf("worse for %d of %d reporting stations", t.Worse, t.Stations)
		if _, err := h.store.SetModelStatus(version, store.Withdrawn, reason, now); err != nil {
			return err
		}
		h.log.Info("model withdrawn", "version", version, "reason", reason)
		h.event(now, "review", "withdraw", fmt.Sprint(version), reason)
		decided = store.Worse
	case m.Status == store.Candidate && t.AnchorNotWorse > t.AnchorWorse && now.Sub(m.CreatedAt) >= h.reviewWindow():
		if _, err := h.store.SetModelStatus(version, store.Published, "", now); err != nil {
			return err
		}
		h.log.Info("model published", "version", version)
		h.event(now, "review", "publish", fmt.Sprint(version),
			fmt.Sprintf("not worse for %d of %d reporting stations", t.NotWorse, t.Stations))
		decided = store.NotWorse
	}
	if decided == "" {
		return nil
	}
	// Reports that agree with the decision are what a careful station sends; they earn trust like consistent
	// contributions, for stations whose contributions went into the last combination. Disagreeing ones cost
	// nothing, honest stations can differ.
	stations, err := h.store.Stations()
	if err != nil {
		return err
	}
	h.mu.Lock()
	seen := h.seen
	h.mu.Unlock()
	for _, st := range stations {
		if votes[st.ID] == decided && seen[st.ID] {
			if err := h.gainTrust(&st, now, "report agreed with the decision"); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReviewDue reviews every candidate that has reached the review age, so that a decision does not wait for the
// next report.
func (h *Hub) ReviewDue(now time.Time) error {
	cands, err := h.store.Models(nil, store.Candidate)
	if err != nil {
		return err
	}
	for _, c := range cands {
		if now.Sub(c.CreatedAt) >= h.reviewWindow() {
			if err := h.Review(c.Version, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *Hub) reviewWindow() time.Duration {
	return time.Duration(h.cfg.Publication.ReviewHours) * time.Hour
}

// doubted says whether an anchor was reported far from the other anchors within the last day: for an anchor the
// penalty stamp is set only by that report, so it is the clock for the doubt, and it outlives a restart, which
// the last combination's far set does not.
func (h *Hub) doubted(reported *time.Time, now time.Time) bool {
	return reported != nil && now.Sub(*reported) < 24*time.Hour
}

// tally returns the counts and each station's vote.
func (h *Hub) tally(m *store.Model, now time.Time) (Tally, map[int64]string, error) {
	all, err := h.store.Reports(m.Version, 0)
	if err != nil {
		return Tally{}, nil, err
	}
	var own Contributors
	if m.Contributors != "" {
		if err := json.Unmarshal([]byte(m.Contributors), &own); err != nil {
			return Tally{}, nil, err
		}
	}
	ownStation, ownAccount := map[int64]bool{}, map[string]bool{}
	for _, id := range own.Stations {
		ownStation[id] = true
	}
	for _, a := range own.Accounts {
		ownAccount[a] = true
	}
	var t Tally
	worse, notWorse := map[int64]int{}, map[int64]int{}
	trust, anchor := map[int64]float64{}, map[int64]bool{}
	minAge := time.Duration(h.cfg.Publication.MinVoteAgeDays) * 24 * time.Hour
	h.mu.Lock()
	far := h.far
	h.mu.Unlock()
	for _, r := range all {
		if r.StationStatus != store.Active || now.Sub(r.StationJoined) < minAge {
			continue
		}
		own := ownStation[r.StationID] || (r.StationAccount != "" && ownAccount[r.StationAccount])
		if r.Verdict != store.Worse && own {
			continue // a station's good word for a model built from its own contribution counts for nothing
		}
		trust[r.StationID] = r.StationTrust
		anchor[r.StationID] = h.anchor(r.StationTrust, r.StationOperator) && !far[r.StationID] &&
			!h.doubted(r.StationPenalized, now)
		t.Reports++
		if r.Verdict == store.Worse {
			worse[r.StationID]++
		} else {
			notWorse[r.StationID]++
		}
	}
	votes := map[int64]string{}
	for id := range worse {
		votes[id] = ""
	}
	for id := range notWorse {
		votes[id] = ""
	}
	t.Stations = len(votes)
	for id := range votes {
		switch {
		case worse[id] > notWorse[id]:
			votes[id] = store.Worse
			t.Worse++
			t.WorseWeight += trust[id]
			if anchor[id] {
				t.AnchorWorse += trust[id]
			}
		case notWorse[id] > worse[id]:
			votes[id] = store.NotWorse
			t.NotWorse++
			t.NotWorseWeight += trust[id]
			if anchor[id] {
				t.AnchorNotWorse += trust[id]
			}
		}
	}
	return t, votes, nil
}

// Tally reports the counts of one model's check reports (for the overview and the command line).
func (h *Hub) Tally(version int64, now time.Time) (Tally, error) {
	m, err := h.store.Model(version)
	if err != nil || m == nil {
		return Tally{}, err
	}
	t, _, err := h.tally(m, now)
	return t, err
}

// Publish and Withdraw are the operator's decisions, independent of the reports.
func (h *Hub) Publish(version int64, now time.Time) (bool, error) {
	m, err := h.store.Model(version)
	if err != nil || m == nil {
		return false, err
	}
	if m.Status == store.Withdrawn && m.Reason == "superseded" {
		return false, fmt.Errorf("model %d was superseded by a newer candidate", version)
	}
	ok, err := h.store.SetModelStatus(version, store.Published, "", now)
	if ok && err == nil {
		h.event(now, "cli", "publish", fmt.Sprint(version), "published by the operator")
	}
	return ok, err
}

// Withdraw takes a model back; stations drop it at their next check.
func (h *Hub) Withdraw(version int64, reason string, now time.Time) (bool, error) {
	if reason == "" {
		reason = "withdrawn by the operator"
	}
	ok, err := h.store.SetModelStatus(version, store.Withdrawn, reason, now)
	if ok && err == nil {
		h.event(now, "cli", "withdraw", fmt.Sprint(version), reason)
	}
	return ok, err
}

func (h *Hub) event(now time.Time, actor, action, subject, detail string) {
	if err := h.store.AddEvent(store.Event{T: now, Actor: actor, Action: action, Subject: subject,
		Detail: detail}); err != nil {
		h.log.Error("event log", "err", err)
	}
}
