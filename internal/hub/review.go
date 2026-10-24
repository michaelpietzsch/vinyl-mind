package hub

import (
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

// Tally counts the reports of active stations on one model. Each station has one vote, the majority of its
// profiles' verdicts (a tie is no vote), so that a station cannot outvote the others by reporting many profiles.
type Tally struct {
	Reports  int // reporting profiles
	Stations int // reporting stations
	Worse    int // stations that found the model worse
	NotWorse int // stations that found it not worse
}

// Review applies the rules of docs/aggregation.md after a new check report. With reports from enough profiles and
// stations, a model that most stations find worse is withdrawn, and a candidate that most find not worse is
// published. Ties wait for the next report.
func (h *Hub) Review(version int64, now time.Time) error {
	if !h.cfg.Publication.Auto {
		return nil
	}
	m, err := h.store.Model(version)
	if err != nil || m == nil || m.Status == store.Withdrawn {
		return err
	}
	t, votes, err := h.tally(version)
	if err != nil || t.Reports < h.cfg.Publication.MinReports || t.Stations < h.cfg.Publication.MinReportStations {
		return err
	}
	decided := ""
	switch {
	case t.Worse > t.NotWorse:
		reason := fmt.Sprintf("worse for %d of %d reporting stations", t.Worse, t.Stations)
		if _, err := h.store.SetModelStatus(version, store.Withdrawn, reason, now); err != nil {
			return err
		}
		h.log.Info("model withdrawn", "version", version, "reason", reason)
		decided = store.Worse
	case m.Status == store.Candidate && t.NotWorse > t.Worse:
		if _, err := h.store.SetModelStatus(version, store.Published, "", now); err != nil {
			return err
		}
		h.log.Info("model published", "version", version)
		decided = store.NotWorse
	}
	if decided == "" {
		return nil
	}
	// Reports that agree with the decision are what a careful station sends; they earn trust like consistent
	// contributions. Disagreeing ones cost nothing, honest stations can differ.
	stations, err := h.store.Stations()
	if err != nil {
		return err
	}
	for _, st := range stations {
		if votes[st.ID] == decided {
			if err := h.gainTrust(&st, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// tally returns the counts and each station's vote.
func (h *Hub) tally(version int64) (Tally, map[int64]string, error) {
	all, err := h.store.Reports(version, 0)
	if err != nil {
		return Tally{}, nil, err
	}
	var t Tally
	worse, notWorse := map[int64]int{}, map[int64]int{}
	for _, r := range all {
		if r.StationStatus != store.Active {
			continue
		}
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
		case notWorse[id] > worse[id]:
			votes[id] = store.NotWorse
			t.NotWorse++
		}
	}
	return t, votes, nil
}

// Tally reports the counts of one model's check reports (for the overview and the command line).
func (h *Hub) Tally(version int64) (Tally, error) {
	t, _, err := h.tally(version)
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
	return h.store.SetModelStatus(version, store.Published, "", now)
}

// Withdraw takes a model back; stations drop it at their next check.
func (h *Hub) Withdraw(version int64, reason string, now time.Time) (bool, error) {
	if reason == "" {
		reason = "withdrawn by the operator"
	}
	return h.store.SetModelStatus(version, store.Withdrawn, reason, now)
}
