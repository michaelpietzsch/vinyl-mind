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

// Tally counts the reports of active stations on one model.
type Tally struct {
	Reports, Stations, Worse int
}

// Review applies the rules of docs/aggregation.md after a new check report: a model that is worse for most
// reporting profiles is withdrawn; a candidate that is not worse for most of them, with reports from enough
// stations, is published. Ties wait for the next report.
func (h *Hub) Review(version int64, now time.Time) error {
	if !h.cfg.Publication.Auto {
		return nil
	}
	m, err := h.store.Model(version)
	if err != nil || m == nil || m.Status == store.Withdrawn {
		return err
	}
	t, reports, err := h.tally(version)
	if err != nil || t.Reports < h.cfg.Publication.MinReports {
		return err
	}
	decided := ""
	switch {
	case 2*t.Worse > t.Reports:
		reason := fmt.Sprintf("worse on %d of %d reporting profiles", t.Worse, t.Reports)
		if _, err := h.store.SetModelStatus(version, store.Withdrawn, reason, now); err != nil {
			return err
		}
		h.log.Info("model withdrawn", "version", version, "reason", reason)
		decided = store.Worse
	case m.Status == store.Candidate && 2*t.Worse < t.Reports && t.Stations >= h.cfg.Publication.MinReportStations:
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
	agree := map[int64]bool{}
	for _, r := range reports {
		if r.Verdict == decided {
			agree[r.StationID] = true
		}
	}
	for _, st := range stations {
		if agree[st.ID] {
			if err := h.gainTrust(&st, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *Hub) tally(version int64) (Tally, []store.Report, error) {
	all, err := h.store.Reports(version, 0)
	if err != nil {
		return Tally{}, nil, err
	}
	var t Tally
	var reports []store.Report
	stations := map[int64]bool{}
	for _, r := range all {
		if r.StationStatus != store.Active {
			continue
		}
		reports = append(reports, r)
		stations[r.StationID] = true
		t.Reports++
		if r.Verdict == store.Worse {
			t.Worse++
		}
	}
	t.Stations = len(stations)
	return t, reports, nil
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
