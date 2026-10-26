package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	Version      int64
	Group        Group
	Status       string
	ResultHash   string // identifies the combination (coefficients, spread, counts) and the settings behind it
	Contributors string // JSON: the stations and accounts whose contributions went in (column contributors)
	StateRev     int64  // grows with every status change: the sequence number of the signed status
	Payload      string
	JWS          string
	CreatedAt    time.Time
	PublishedAt  *time.Time
	WithdrawnAt  *time.Time
	Reason       string
}

const modelCols = `version, feature_version, detector_version, chain_class, status, inputs_hash, contributors,
	payload, jws, created_at, published_at, withdrawn_at, reason, state_rev`

func scanModel(r scanner) (*Model, error) {
	var m Model
	var created int64
	var pub, wd sql.NullInt64
	var reason, contributors sql.NullString
	if err := r.Scan(&m.Version, &m.Group.FeatureVersion, &m.Group.DetectorVersion, &m.Group.ChainClass, &m.Status,
		&m.ResultHash, &contributors, &m.Payload, &m.JWS, &created, &pub, &wd, &reason, &m.StateRev); err != nil {
		return nil, err
	}
	m.CreatedAt, m.PublishedAt, m.WithdrawnAt = time.UnixMilli(created), fromMS(pub), fromMS(wd)
	m.Reason, m.Contributors = reason.String, contributors.String
	return &m, nil
}

// AddModel stores a new candidate. build receives the version number, which is part of the signed payload, and
// returns payload and JWS. An older candidate of the same group is superseded.
func (s *Store) AddModel(g Group, resultHash, contributors string, build func(version int64) ([]byte, string, error),
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
		if _, err := tx.Exec(s.q(`UPDATE models SET status = ?, withdrawn_at = ?, reason = ?, state_rev = state_rev + 1
			WHERE feature_version = ? AND detector_version = ? AND chain_class = ? AND status = ?`),
			Withdrawn, ms(now), "superseded", g.FeatureVersion, g.DetectorVersion, g.ChainClass,
			Candidate); err != nil {
			return err
		}
		_, err = tx.Exec(s.q(`INSERT INTO models (version, feature_version, detector_version, chain_class, status,
			inputs_hash, contributors, payload, jws, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), version,
			g.FeatureVersion, g.DetectorVersion, g.ChainClass, Candidate, resultHash, contributors, string(payload),
			jws, ms(now))
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

// LatestResultHash is the result hash of the newest model of a group, whatever its status: if a new combination
// has the same hash, there is nothing new to store, and a candidate the anchors withdrew is not built again from
// the same inputs.
func (s *Store) LatestResultHash(g Group) (string, error) {
	ms, err := s.Models(&g, "")
	if err != nil || len(ms) == 0 {
		return "", err
	}
	return ms[0].ResultHash, nil
}

// Published is the model stations are to use: the one published last, if it is still published. A withdrawn
// model does not hand its place back to an older one; that is the operator's decision (publish).
func (s *Store) Published(g Group) (*Model, error) {
	rows, err := s.db.Query(s.q("SELECT "+modelCols+` FROM models WHERE feature_version = ? AND detector_version = ?
		AND chain_class = ? AND published_at IS NOT NULL
		ORDER BY published_at DESC, CASE WHEN status = 'published' THEN 0 ELSE 1 END, version DESC LIMIT 1`),
		g.FeatureVersion, g.DetectorVersion, g.ChainClass)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	m, err := scanModel(rows)
	if err != nil || m.Status != Published {
		return nil, err
	}
	return m, nil
}

// SetModelStatus publishes or withdraws a model.
func (s *Store) SetModelStatus(version int64, status, reason string, now time.Time) (bool, error) {
	col := "withdrawn_at"
	if status == Published {
		col = "published_at"
	}
	r, err := s.db.Exec(s.q("UPDATE models SET status = ?, reason = ?, "+col+" = ?, state_rev = state_rev + 1 WHERE version = ?"),
		status, sql.NullString{String: reason, Valid: reason != ""}, ms(now), version)
	return affected(r, err)
}

// Report is a check report of one profile on one model.
type Report struct {
	StationID        int64
	StationFP        string
	StationStatus    string
	StationTrust     float64
	StationOperator  bool
	StationAccount   string
	StationJoined    time.Time
	StationPenalized *time.Time // the station's last penalty, for an anchor: the last report of it being far
	ProfileID        string
	ModelVersion     int64
	Adopted          bool
	Kinds            map[string]spec.KindCheck
	Verdict          string
	ReceivedAt       time.Time
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
	rows, err := s.db.Query(s.q(`SELECT r.station_id, st.key_fp, st.status, st.trust, st.trust_operator, st.account,
		st.joined_at, st.trust_penalized_at, r.profile_id, r.model_version, r.adopted, r.kinds, r.verdict, r.received_at
		FROM check_reports r JOIN stations st ON st.id = r.station_id WHERE `+where+` ORDER BY r.received_at`),
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Report
	for rows.Next() {
		var r Report
		var kinds string
		var received, joined int64
		var penalized sql.NullInt64
		var account sql.NullString
		if err := rows.Scan(&r.StationID, &r.StationFP, &r.StationStatus, &r.StationTrust, &r.StationOperator,
			&account, &joined, &penalized, &r.ProfileID, &r.ModelVersion, &r.Adopted, &kinds, &r.Verdict,
			&received); err != nil {
			return nil, err
		}
		r.StationAccount, r.StationPenalized = account.String, fromMS(penalized)
		if err := json.Unmarshal([]byte(kinds), &r.Kinds); err != nil {
			return nil, err
		}
		r.ReceivedAt, r.StationJoined = time.UnixMilli(received), time.UnixMilli(joined)
		out = append(out, r)
	}
	return out, rows.Err()
}

// StationProfiles is the set of profile IDs a station has used, in contributions or in check reports. It bounds
// how many profiles a station can report for, so that it cannot invent profiles to fill the database.
func (s *Store) StationProfiles(stationID int64) (map[string]bool, error) {
	rows, err := s.db.Query(s.q(`SELECT profile_id FROM contributions WHERE station_id = ?
		UNION SELECT profile_id FROM check_reports WHERE station_id = ?`), stationID, stationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// Backup writes a consistent copy of a SQLite database while the server keeps running. PostgreSQL has its own
// tools for this (pg_dump).
func (s *Store) Backup(path string) error {
	if s.pg {
		return fmt.Errorf("the database is PostgreSQL: back it up with pg_dump")
	}
	_, err := s.db.Exec("VACUUM INTO ?", path)
	return err
}

// AccessEntry is one line of the access log. Refused requests are counted, not listed: an entry with N > 1
// stands for that many requests from the address with the same status and code.
type AccessEntry struct {
	T       time.Time
	Address string
	KeyFP   string
	Method  string
	Path    string
	Status  int
	Code    string // the error code of the answer, empty for a success
	N       int
}

// MaxAccessLog bounds the access log in rows, whatever the retention in days: the oldest go first.
const MaxAccessLog = 500_000

// LogAccess appends to the access log.
func (s *Store) LogAccess(e AccessEntry) error {
	return s.LogAccesses([]AccessEntry{e})
}

// LogAccesses appends several entries in one transaction.
func (s *Store) LogAccesses(entries []AccessEntry) error {
	return s.tx(func(tx *sql.Tx) error {
		for _, e := range entries {
			if len(e.Path) > 200 {
				e.Path = e.Path[:200]
			}
			if e.N < 1 {
				e.N = 1
			}
			if _, err := tx.Exec(s.q(`INSERT INTO access_log (t, address, key_fp, method, path, status, code, n)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`), ms(e.T), e.Address,
				sql.NullString{String: e.KeyFP, Valid: e.KeyFP != ""}, e.Method, e.Path, e.Status,
				sql.NullString{String: e.Code, Valid: e.Code != ""}, e.N); err != nil {
				return err
			}
		}
		return nil
	})
}

// AccessLog returns the newest entries.
func (s *Store) AccessLog(limit int) ([]AccessEntry, error) {
	rows, err := s.db.Query(s.q(`SELECT t, address, key_fp, method, path, status, code, n FROM access_log
		ORDER BY id DESC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessEntry
	for rows.Next() {
		var e AccessEntry
		var t int64
		var addr, fp, code sql.NullString
		if err := rows.Scan(&t, &addr, &fp, &e.Method, &e.Path, &e.Status, &code, &e.N); err != nil {
			return nil, err
		}
		e.T, e.Address, e.KeyFP, e.Code = time.UnixMilli(t), addr.String, fp.String, code.String
		out = append(out, e)
	}
	return out, rows.Err()
}

// Refusals counts the requests refused since t by status and code, for the operator's overview.
func (s *Store) Refusals(t time.Time) (map[string]int, error) {
	rows, err := s.db.Query(s.q(`SELECT status, COALESCE(code, ''), SUM(n) FROM access_log
		WHERE t >= ? AND status >= 400 GROUP BY status, code`), ms(t))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status, n int
		var code string
		if err := rows.Scan(&status, &code, &n); err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%d %s", status, code)] = n
	}
	return out, rows.Err()
}

// Count returns the rows of a table, for the operator's overview.
func (s *Store) Count(table string) (int, error) {
	switch table {
	case "nonces", "access_log", "contributions", "events", "former_stations":
	default:
		return 0, fmt.Errorf("no count for %q", table)
	}
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
	return n, err
}

// Prune forgets nonces a day after they expired (a clock that went back cannot revive them), access log entries
// older than the retention or beyond MaxAccessLog, and stations that left more than FormerDays ago.
func (s *Store) Prune(now time.Time, accessLogDays int) error {
	if _, err := s.db.Exec(s.q("DELETE FROM nonces WHERE expires_at < ?"), ms(now.Add(-24*time.Hour))); err != nil {
		return err
	}
	if _, err := s.db.Exec(s.q("DELETE FROM access_log WHERE t < ?"),
		ms(now.Add(-time.Duration(accessLogDays)*24*time.Hour))); err != nil {
		return err
	}
	if _, err := s.db.Exec(s.q(`DELETE FROM access_log WHERE id <= (SELECT COALESCE(MAX(id), 0) FROM access_log) - ?`),
		MaxAccessLog); err != nil {
		return err
	}
	if _, err := s.db.Exec(s.q("DELETE FROM former_stations WHERE left_at < ?"),
		ms(now.Add(-FormerDays*24*time.Hour))); err != nil {
		return err
	}
	_, err := s.db.Exec(s.q("DELETE FROM events WHERE t < ?"), ms(now.Add(-EventDays*24*time.Hour)))
	return err
}

// EventDays is how long the event log is kept.
const EventDays = 365
