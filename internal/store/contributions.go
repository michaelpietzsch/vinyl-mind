package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/spec"
)

// ErrTooManyProfiles is returned when a station would exceed its number of profiles.
var ErrTooManyProfiles = errors.New("too many profiles")

// Contribution is the stored state of one profile, with the station's key, status and trust.
type Contribution struct {
	ID              int64
	StationID       int64
	StationFP       string
	StationStatus   string
	Account         string
	Trust           float64
	TrustOperator   bool
	ProfileID       string
	FeatureVersion  int
	DetectorVersion string
	ClientName      string
	ClientVersion   string
	ChainClass      string
	TrainedAt       *int64
	ReceivedAt      time.Time
	Kinds           map[string]spec.KindContribution
}

func (c *Contribution) same(d *spec.Contribution) bool {
	tr := func(p *int64) any {
		if p == nil {
			return nil
		}
		return *p
	}
	return c.FeatureVersion == d.FeatureVersion && c.DetectorVersion == d.DetectorVersion &&
		c.ClientName == d.Client.Name && c.ClientVersion == d.Client.Version && c.ChainClass == d.Chain.Class() &&
		tr(c.TrainedAt) == tr(d.TrainedAt) && reflect.DeepEqual(c.Kinds, d.Kinds)
}

// PutContribution replaces a profile's contribution. It returns true if something changed: an unchanged
// contribution keeps its ID, so the combiner sees the same inputs and does not build a new model for nothing.
func (s *Store) PutContribution(stationID int64, profileID string, d *spec.Contribution, maxProfiles int,
	now time.Time) (bool, error) {
	changed := true
	err := s.tx(func(tx *sql.Tx) error {
		old, err := s.contributions(tx, "c.station_id = ? AND c.profile_id = ?", stationID, profileID)
		if err != nil {
			return err
		}
		if len(old) == 1 && old[0].same(d) {
			changed = false
			if _, err := tx.Exec(s.q("UPDATE contributions SET received_at = ? WHERE id = ?"), ms(now),
				old[0].ID); err != nil {
				return err
			}
			_, err := tx.Exec(s.q("UPDATE stations SET last_contribution_at = ? WHERE id = ?"), ms(now), stationID)
			return err
		}
		if len(old) == 0 {
			// Profiles that only reported count too: the limit is on the profiles a station uses at all
			var n, known int
			if err := tx.QueryRow(s.q(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN profile_id = ? THEN 1 ELSE 0 END), 0)
				FROM (SELECT profile_id FROM contributions WHERE station_id = ?
				      UNION SELECT profile_id FROM check_reports WHERE station_id = ?) p`), profileID, stationID,
				stationID).Scan(&n, &known); err != nil {
				return err
			}
			if known == 0 && n >= maxProfiles {
				return ErrTooManyProfiles
			}
		}
		if _, err := tx.Exec(s.q("DELETE FROM contributions WHERE station_id = ? AND profile_id = ?"), stationID,
			profileID); err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRow(s.q(`INSERT INTO contributions (station_id, profile_id, feature_version,
			detector_version, client_name, client_version, chain_class, trained_at, received_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`), stationID, profileID, d.FeatureVersion,
			d.DetectorVersion, d.Client.Name, d.Client.Version, d.Chain.Class(), d.TrainedAt, ms(now)).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(s.q(`UPDATE stations SET contributions_total = contributions_total + 1,
			last_contribution_at = ? WHERE id = ?`), ms(now), stationID); err != nil {
			return err
		}
		for kind, k := range d.Kinds {
			coef, _ := json.Marshal(k.Coef)
			var informative any
			if k.Informative != nil {
				b, _ := json.Marshal(k.Informative)
				informative = string(b)
			}
			if _, err := tx.Exec(s.q(`INSERT INTO contribution_kinds (contribution_id, kind, coef, n_real, n_false,
				n_manual, n_master, master_recordings, cv_err_est, cv_err_raw, auc, informative)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), id, kind, string(coef), k.NReal, k.NFalse, k.NManual,
				k.NMaster, k.MasterRecordings, k.CVErrEst, k.CVErrRaw, k.AUC, informative); err != nil {
				return err
			}
		}
		return nil
	})
	return changed, err
}

// DeleteContribution removes a profile's contribution.
func (s *Store) DeleteContribution(stationID int64, profileID string) (bool, error) {
	r, err := s.db.Exec(s.q("DELETE FROM contributions WHERE station_id = ? AND profile_id = ?"), stationID,
		profileID)
	return affected(r, err)
}

// StationContributions lists one station's contributions.
func (s *Store) StationContributions(stationID int64) ([]Contribution, error) {
	return s.contributions(s.db, "c.station_id = ?", stationID)
}

// ActiveContributions lists the contributions of all active stations, the input of the combiner.
func (s *Store) ActiveContributions() ([]Contribution, error) {
	return s.contributions(s.db, "st.status = ?", Active)
}

type rowsQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
}

func (s *Store) contributions(q rowsQuerier, where string, args ...any) ([]Contribution, error) {
	rows, err := q.Query(s.q(`SELECT c.id, c.station_id, st.key_fp, st.status, st.account, st.trust,
		st.trust_operator, c.profile_id, c.feature_version, c.detector_version, c.client_name, c.client_version,
		c.chain_class, c.trained_at, c.received_at FROM contributions c JOIN stations st ON st.id = c.station_id
		WHERE `+where+` ORDER BY c.id`), args...)
	if err != nil {
		return nil, err
	}
	var out []Contribution
	byID := map[int64]int{}
	for rows.Next() {
		var c Contribution
		var trained sql.NullInt64
		var received int64
		var account sql.NullString
		if err := rows.Scan(&c.ID, &c.StationID, &c.StationFP, &c.StationStatus, &account, &c.Trust,
			&c.TrustOperator, &c.ProfileID, &c.FeatureVersion, &c.DetectorVersion, &c.ClientName, &c.ClientVersion,
			&c.ChainClass, &trained, &received); err != nil {
			rows.Close()
			return nil, err
		}
		c.Account = account.String
		if trained.Valid {
			c.TrainedAt = &trained.Int64
		}
		c.ReceivedAt, c.Kinds = time.UnixMilli(received), map[string]spec.KindContribution{}
		byID[c.ID] = len(out)
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	ids := make([]any, 0, len(out))
	for _, c := range out {
		ids = append(ids, c.ID)
	}
	krows, err := q.Query(s.q(`SELECT contribution_id, kind, coef, n_real, n_false, n_manual, n_master,
		master_recordings, cv_err_est, cv_err_raw, auc, informative FROM contribution_kinds
		WHERE contribution_id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`), ids...)
	if err != nil {
		return nil, err
	}
	defer krows.Close()
	for krows.Next() {
		var id int64
		var kind, coef string
		var k spec.KindContribution
		var auc sql.NullFloat64
		var informative sql.NullString
		if err := krows.Scan(&id, &kind, &coef, &k.NReal, &k.NFalse, &k.NManual, &k.NMaster, &k.MasterRecordings,
			&k.CVErrEst, &k.CVErrRaw, &auc, &informative); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(coef), &k.Coef); err != nil {
			return nil, err
		}
		if informative.Valid {
			if err := json.Unmarshal([]byte(informative.String), &k.Informative); err != nil {
				return nil, err
			}
		}
		if auc.Valid {
			k.AUC = &auc.Float64
		}
		out[byID[id]].Kinds[kind] = k
	}
	return out, krows.Err()
}
