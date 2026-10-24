package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/spec"
)

// Model status values.
const (
	Candidate = "candidate"
	Published = "published"
	Withdrawn = "withdrawn"
)

// Group is what a model is built for: contributions of different groups are never combined.
type Group struct {
	FeatureVersion  int
	DetectorVersion string
	ChainClass      string
}

// Model is a global model with its signed form and status.
type Model struct {
	Version     int64
	Group       Group
	Status      string
	InputsHash  string
	Payload     string
	JWS         string
	CreatedAt   time.Time
	PublishedAt *time.Time
	WithdrawnAt *time.Time
	Reason      string
}

const modelCols = `version, feature_version, detector_version, chain_class, status, inputs_hash, payload, jws,
	created_at, published_at, withdrawn_at, reason`

func scanModel(r scanner) (*Model, error) {
	var m Model
	var created int64
	var pub, wd sql.NullInt64
	var reason sql.NullString
	if err := r.Scan(&m.Version, &m.Group.FeatureVersion, &m.Group.DetectorVersion, &m.Group.ChainClass, &m.Status,
		&m.InputsHash, &m.Payload, &m.JWS, &created, &pub, &wd, &reason); err != nil {
		return nil, err
	}
	m.CreatedAt, m.PublishedAt, m.WithdrawnAt, m.Reason = time.UnixMilli(created), fromMS(pub), fromMS(wd), reason.String
	return &m, nil
}

// AddModel stores a new candidate. build receives the version number, which is part of the signed payload, and
// returns payload and JWS. An older candidate of the same group is superseded.
func (s *Store) AddModel(g Group, inputsHash string, build func(version int64) ([]byte, string, error),
	now time.Time) (int64, error) {
	var version int64
	err := s.tx(func(tx *sql.Tx) error {
		if err := tx.QueryRow("SELECT COALESCE(MAX(version), 0) + 1 FROM models").Scan(&version); err != nil {
			return err
		}
		payload, jws, err := build(version)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(s.q(`UPDATE models SET status = ?, withdrawn_at = ?, reason = ?
			WHERE feature_version = ? AND detector_version = ? AND chain_class = ? AND status = ?`),
			Withdrawn, ms(now), "superseded", g.FeatureVersion, g.DetectorVersion, g.ChainClass,
			Candidate); err != nil {
			return err
		}
		_, err = tx.Exec(s.q(`INSERT INTO models (version, feature_version, detector_version, chain_class, status,
			inputs_hash, payload, jws, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`), version, g.FeatureVersion,
			g.DetectorVersion, g.ChainClass, Candidate, inputsHash, string(payload), jws, ms(now))
		return err
	})
	return version, err
}

// Model returns one version, or nil.
func (s *Store) Model(version int64) (*Model, error) {
	m, err := scanModel(s.db.QueryRow(s.q("SELECT "+modelCols+" FROM models WHERE version = ?"), version))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return m, err
}

// Models lists models, newest first; a nil group or empty status means all.
func (s *Store) Models(g *Group, status string) ([]Model, error) {
	where, args := "1 = 1", []any{}
	if g != nil {
		where += " AND feature_version = ? AND detector_version = ? AND chain_class = ?"
		args = append(args, g.FeatureVersion, g.DetectorVersion, g.ChainClass)
	}
	if status != "" {
		where += " AND status = ?"
		args = append(args, status)
	}
	rows, err := s.db.Query(s.q("SELECT "+modelCols+" FROM models WHERE "+where+" ORDER BY version DESC"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// Latest is the newest model of a group with the status, or nil.
func (s *Store) Latest(g Group, status string) (*Model, error) {
	ms, err := s.Models(&g, status)
	if err != nil || len(ms) == 0 {
		return nil, err
	}
	return &ms[0], nil
}

// LatestInputsHash is the inputs hash of the newest candidate or published model of a group: if the inputs are the
// same, there is nothing new to combine.
func (s *Store) LatestInputsHash(g Group) (string, error) {
	ms, err := s.Models(&g, "")
	if err != nil {
		return "", err
	}
	for _, m := range ms {
		if m.Status == Candidate || m.Status == Published {
			return m.InputsHash, nil
		}
	}
	return "", nil
}

// SetModelStatus publishes or withdraws a model.
func (s *Store) SetModelStatus(version int64, status, reason string, now time.Time) (bool, error) {
	col := "withdrawn_at"
	if status == Published {
		col = "published_at"
	}
	r, err := s.db.Exec(s.q("UPDATE models SET status = ?, reason = ?, "+col+" = ? WHERE version = ?"), status,
		sql.NullString{String: reason, Valid: reason != ""}, ms(now), version)
	return affected(r, err)
}

// Report is a check report of one profile on one model.
type Report struct {
	StationID     int64
	StationFP     string
	StationStatus string
	ProfileID     string
	ModelVersion  int64
	Adopted       bool
	Kinds         map[string]spec.KindCheck
	Verdict       string
	ReceivedAt    time.Time
}

// Verdicts of a check report.
const (
	Worse    = "worse"
	NotWorse = "not_worse"
)

// PutReport stores a report, replacing an earlier one of the same profile on the same model.
func (s *Store) PutReport(r Report) error {
	kinds, _ := json.Marshal(r.Kinds)
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(s.q(`DELETE FROM check_reports WHERE station_id = ? AND profile_id = ?
			AND model_version = ?`), r.StationID, r.ProfileID, r.ModelVersion); err != nil {
			return err
		}
		_, err := tx.Exec(s.q(`INSERT INTO check_reports (station_id, profile_id, model_version, adopted, kinds,
			verdict, received_at) VALUES (?, ?, ?, ?, ?, ?, ?)`), r.StationID, r.ProfileID, r.ModelVersion,
			boolInt(r.Adopted), string(kinds), r.Verdict, ms(r.ReceivedAt))
		return err
	})
}

// Reports lists reports on one model (version > 0) or of one station (stationID > 0), oldest first.
func (s *Store) Reports(version, stationID int64) ([]Report, error) {
	where, args := "1 = 1", []any{}
	if version > 0 {
		where += " AND r.model_version = ?"
		args = append(args, version)
	}
	if stationID > 0 {
		where += " AND r.station_id = ?"
		args = append(args, stationID)
	}
	rows, err := s.db.Query(s.q(`SELECT r.station_id, st.key_fp, st.status, r.profile_id, r.model_version,
		r.adopted, r.kinds, r.verdict, r.received_at FROM check_reports r JOIN stations st ON st.id = r.station_id
		WHERE `+where+` ORDER BY r.received_at`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Report
	for rows.Next() {
		var r Report
		var kinds string
		var received int64
		if err := rows.Scan(&r.StationID, &r.StationFP, &r.StationStatus, &r.ProfileID, &r.ModelVersion, &r.Adopted,
			&kinds, &r.Verdict, &received); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(kinds), &r.Kinds); err != nil {
			return nil, err
		}
		r.ReceivedAt = time.UnixMilli(received)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AccessEntry is one line of the access log.
type AccessEntry struct {
	T       time.Time
	Address string
	KeyFP   string
	Method  string
	Path    string
	Status  int
}

// LogAccess appends to the access log.
func (s *Store) LogAccess(e AccessEntry) error {
	if len(e.Path) > 200 {
		e.Path = e.Path[:200]
	}
	_, err := s.db.Exec(s.q(`INSERT INTO access_log (t, address, key_fp, method, path, status)
		VALUES (?, ?, ?, ?, ?, ?)`), ms(e.T), e.Address, sql.NullString{String: e.KeyFP, Valid: e.KeyFP != ""},
		e.Method, e.Path, e.Status)
	return err
}

// AccessLog returns the newest entries.
func (s *Store) AccessLog(limit int) ([]AccessEntry, error) {
	rows, err := s.db.Query(s.q(`SELECT t, address, key_fp, method, path, status FROM access_log
		ORDER BY id DESC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessEntry
	for rows.Next() {
		var e AccessEntry
		var t int64
		var addr, fp sql.NullString
		if err := rows.Scan(&t, &addr, &fp, &e.Method, &e.Path, &e.Status); err != nil {
			return nil, err
		}
		e.T, e.Address, e.KeyFP = time.UnixMilli(t), addr.String, fp.String
		out = append(out, e)
	}
	return out, rows.Err()
}

// Prune forgets expired nonces and access log entries older than the retention.
func (s *Store) Prune(now time.Time, accessLogDays int) error {
	if _, err := s.db.Exec(s.q("DELETE FROM nonces WHERE expires_at < ?"), ms(now)); err != nil {
		return err
	}
	_, err := s.db.Exec(s.q("DELETE FROM access_log WHERE t < ?"),
		ms(now.Add(-time.Duration(accessLogDays)*24*time.Hour)))
	return err
}
