package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// Station status values.
const (
	Pending = "pending"
	Active  = "active"
	Revoked = "revoked"
)

var (
	ErrInvitationInvalid  = errors.New("invitation invalid")
	ErrInvitationRequired = errors.New("invitation required")
)

// Station is one registered key.
type Station struct {
	ID            int64
	KeyFP         string
	PublicKey     string
	Status        string
	Trust         float64
	TrustGainedAt *time.Time
	InvitationID  *int64
	JoinedAt      time.Time
	ApprovedAt    *time.Time
	LastSeenAt    *time.Time
	Profiles      int
}

// Invitation is a code that lets stations join without waiting for approval.
type Invitation struct {
	ID        int64
	MaxUses   int
	Uses      int
	ExpiresAt *time.Time
	Revoked   bool
	CreatedAt time.Time
}

// CodeHash is how an invitation code is kept. The codes carry 128 random bits, so a plain hash is enough to make a
// copy of the database useless for joining.
func CodeHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// AddInvitation stores a new code (only its hash).
func (s *Store) AddInvitation(code string, maxUses int, expires *time.Time, now time.Time) (int64, error) {
	var id int64
	err := s.db.QueryRow(s.q(`INSERT INTO invitations (code_hash, max_uses, uses, expires_at, revoked, created_at)
		VALUES (?, ?, 0, ?, 0, ?) RETURNING id`), CodeHash(code), maxUses, msPtr(expires), ms(now)).Scan(&id)
	return id, err
}

// Invitations lists all codes, newest first.
func (s *Store) Invitations() ([]Invitation, error) {
	rows, err := s.db.Query(`SELECT id, max_uses, uses, expires_at, revoked, created_at FROM invitations
		ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invitation
	for rows.Next() {
		var inv Invitation
		var exp sql.NullInt64
		var created int64
		if err := rows.Scan(&inv.ID, &inv.MaxUses, &inv.Uses, &exp, &inv.Revoked, &created); err != nil {
			return nil, err
		}
		inv.ExpiresAt, inv.CreatedAt = fromMS(exp), time.UnixMilli(created)
		out = append(out, inv)
	}
	return out, rows.Err()
}

// RevokeInvitation makes a code unusable.
func (s *Store) RevokeInvitation(id int64) (bool, error) {
	r, err := s.db.Exec(s.q("UPDATE invitations SET revoked = 1 WHERE id = ?"), id)
	return affected(r, err)
}

const stationCols = `s.id, s.key_fp, s.public_key, s.status, s.trust, s.trust_gained_at, s.invitation_id,
	s.joined_at, s.approved_at, s.last_seen_at,
	(SELECT COUNT(*) FROM contributions c WHERE c.station_id = s.id)`

type scanner interface{ Scan(...any) error }

func scanStation(r scanner) (*Station, error) {
	var st Station
	var gained, approved, seen, inv sql.NullInt64
	var joined int64
	if err := r.Scan(&st.ID, &st.KeyFP, &st.PublicKey, &st.Status, &st.Trust, &gained, &inv, &joined, &approved,
		&seen, &st.Profiles); err != nil {
		return nil, err
	}
	st.TrustGainedAt, st.ApprovedAt, st.LastSeenAt = fromMS(gained), fromMS(approved), fromMS(seen)
	st.JoinedAt = time.UnixMilli(joined)
	if inv.Valid {
		st.InvitationID = &inv.Int64
	}
	return &st, nil
}

// Station returns the station with this key fingerprint, or nil.
func (s *Store) Station(fp string) (*Station, error) {
	return s.station(s.db, fp)
}

type querier interface {
	QueryRow(string, ...any) *sql.Row
}

func (s *Store) station(q querier, fp string) (*Station, error) {
	st, err := scanStation(q.QueryRow(s.q("SELECT "+stationCols+" FROM stations s WHERE s.key_fp = ?"), fp))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return st, err
}

// Stations lists all stations in the order they joined.
func (s *Store) Stations() ([]Station, error) {
	rows, err := s.db.Query("SELECT " + stationCols + " FROM stations s ORDER BY s.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Station
	for rows.Next() {
		st, err := scanStation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *st)
	}
	return out, rows.Err()
}

// Join registers a station. With a valid invitation code it is active at once; without one it waits for the
// operator, if allowPending. Joining again with the same key returns the existing station unchanged, so a station
// can retry after a lost answer without using up an invitation.
func (s *Store) Join(fp, publicKey string, code *string, allowPending bool, trust float64,
	now time.Time) (*Station, error) {
	var out *Station
	err := s.tx(func(tx *sql.Tx) error {
		existing, err := s.station(tx, fp)
		if err != nil || existing != nil {
			out = existing
			return err
		}
		status := Pending
		var invID any
		if code != nil {
			var id int64
			// The conditions in the UPDATE are the check: two stations racing for the last use cannot both win
			err := tx.QueryRow(s.q(`UPDATE invitations SET uses = uses + 1
				WHERE code_hash = ? AND revoked = 0 AND uses < max_uses AND (expires_at IS NULL OR expires_at > ?)
				RETURNING id`), CodeHash(*code), ms(now)).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrInvitationInvalid
			}
			if err != nil {
				return err
			}
			status, invID = Active, id
		} else if !allowPending {
			return ErrInvitationRequired
		}
		var approved any
		if status == Active {
			approved = ms(now)
		}
		if _, err := tx.Exec(s.q(`INSERT INTO stations (key_fp, public_key, status, trust, invitation_id, joined_at,
			approved_at) VALUES (?, ?, ?, ?, ?, ?, ?)`), fp, publicKey, status, trust, invID, ms(now),
			approved); err != nil {
			return err
		}
		out, err = s.station(tx, fp)
		return err
	})
	return out, err
}

// JoinsSince counts the stations that joined since t.
func (s *Store) JoinsSince(t time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(s.q("SELECT COUNT(*) FROM stations WHERE joined_at >= ?"), ms(t)).Scan(&n)
	return n, err
}

// SetStatus approves or revokes a station. Revoking also deletes its contributions and reports, as leaving does;
// the row stays so that the key cannot join again.
func (s *Store) SetStatus(fp, status string, now time.Time) (bool, error) {
	found := false
	err := s.tx(func(tx *sql.Tx) error {
		st, err := s.station(tx, fp)
		if err != nil || st == nil {
			return err
		}
		found = true
		if status == Active {
			_, err = tx.Exec(s.q("UPDATE stations SET status = ?, approved_at = ? WHERE id = ?"), status, ms(now),
				st.ID)
		} else {
			_, err = tx.Exec(s.q("UPDATE stations SET status = ? WHERE id = ?"), status, st.ID)
		}
		if err != nil || status != Revoked {
			return err
		}
		if _, err := tx.Exec(s.q("DELETE FROM contributions WHERE station_id = ?"), st.ID); err != nil {
			return err
		}
		_, err = tx.Exec(s.q("DELETE FROM check_reports WHERE station_id = ?"), st.ID)
		return err
	})
	return found, err
}

// Leave deletes a station with everything it sent (the cascades take contributions and reports).
func (s *Store) Leave(st *Station) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(s.q("DELETE FROM nonces WHERE key_fp = ?"), st.KeyFP); err != nil {
			return err
		}
		_, err := tx.Exec(s.q("DELETE FROM stations WHERE id = ?"), st.ID)
		return err
	})
}

// Touch records that a station was seen.
func (s *Store) Touch(id int64, now time.Time) error {
	_, err := s.db.Exec(s.q("UPDATE stations SET last_seen_at = ? WHERE id = ?"), ms(now), id)
	return err
}

// SetTrust stores a station's trust; gainedAt is set when the trust grew, to limit growth to once per day.
func (s *Store) SetTrust(id int64, trust float64, gainedAt *time.Time) error {
	if gainedAt != nil {
		_, err := s.db.Exec(s.q("UPDATE stations SET trust = ?, trust_gained_at = ? WHERE id = ?"), trust,
			ms(*gainedAt), id)
		return err
	}
	_, err := s.db.Exec(s.q("UPDATE stations SET trust = ? WHERE id = ?"), trust, id)
	return err
}

// UseNonce remembers a nonce; false means it was used before (a replayed request).
func (s *Store) UseNonce(fp, nonce string, expires time.Time) (bool, error) {
	r, err := s.db.Exec(s.q(`INSERT INTO nonces (key_fp, nonce, expires_at) VALUES (?, ?, ?)
		ON CONFLICT DO NOTHING`), fp, nonce, ms(expires))
	return affected(r, err)
}

func affected(r sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}
