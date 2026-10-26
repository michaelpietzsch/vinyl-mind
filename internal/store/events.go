package store

import (
	"database/sql"
	"errors"
	"time"
)

// Event is one line of the event log: what changed the server's state or was refused at its door, by whom. The
// server writes admissions and refusals, the command line writes the operator's decisions, so that both are in
// one place with the database they concern (docs/operations.md).
type Event struct {
	T       time.Time
	Actor   string // "server", "cli" or "review"
	Action  string // e.g. "join", "leave", "trust", "publish"
	Subject string // the station key, model version or invitation the event is about
	Detail  string
}

// AddEvent appends to the event log.
func (s *Store) AddEvent(e Event) error {
	if len(e.Detail) > 500 {
		e.Detail = e.Detail[:500]
	}
	_, err := s.db.Exec(s.q("INSERT INTO events (t, actor, action, subject, detail) VALUES (?, ?, ?, ?, ?)"),
		ms(e.T), e.Actor, e.Action, e.Subject, e.Detail)
	return err
}

// Events returns the newest entries.
func (s *Store) Events(limit int) ([]Event, error) {
	rows, err := s.db.Query(s.q("SELECT t, actor, action, subject, detail FROM events ORDER BY id DESC LIMIT ?"),
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var t int64
		if err := rows.Scan(&t, &e.Actor, &e.Action, &e.Subject, &e.Detail); err != nil {
			return nil, err
		}
		e.T = time.UnixMilli(t)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Meta reads one value of the small key-value table (empty if unset).
func (s *Store) Meta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(s.q("SELECT value FROM meta WHERE key = ?"), key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetMeta writes one value of the key-value table.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(s.q(`INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`), key, value)
	return err
}
