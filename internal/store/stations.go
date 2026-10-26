package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
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
	ErrBindingRequired    = errors.New("binding required")
	ErrJoinLimit          = errors.New("join limit reached")
)

// FormerDays is how long a station that left is remembered: a key or account that comes back within this time
// starts with the trust it had, not with a clean slate.
const FormerDays = 30

// InvitationCheck decides, before an invitation is used up, whether this join may use it, and returns the account
// the station is to belong to (empty for none). An error leaves the invitation untouched.
type InvitationCheck func(invitationID int64, bound bool) (account string, err error)

// Station is one registered key.
type Station struct {
	ID            int64
	KeyFP         string
	PublicKey     string
	Status        string
	Trust         float64
	TrustOperator bool       // the trust was set by the operator; it never changes automatically
	TrustGainedAt *time.Time // last automatic gain
	TrustPenalty  *time.Time // last automatic penalty
	InvitationID  *int64
	JoinedAt      time.Time
	ApprovedAt    *time.Time
	LastSeenAt    *time.Time
	Profiles      int
	Account       string // opaque account id from an account service's binding, empty without
	Contributions int    // accepted contributions that changed something, since joining
	LastContrib   *time.Time
	Examples      int // examples behind the current contributions (real plus false alarms, all kinds)
	Reports       int
}

// Invitation is a code that lets stations join without waiting for approval.
type Invitation struct {
	ID        int64
	MaxUses   int
	Uses      int
	ExpiresAt *time.Time
	Revoked   bool
	Bound     bool // valid only with a binding from an account service
	CreatedAt time.Time
}

// CodeHash is how an invitation code is kept. The codes carry 128 random bits, so a plain hash is enough to make a
// copy of the database useless for joining.
func CodeHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// AddInvitation stores a new code (only its hash).
func (s *Store) AddInvitation(code string, maxUses int, expires *time.Time, bound bool, now time.Time) (int64, error) {
	var id int64
	err := s.db.QueryRow(s.q(`INSERT INTO invitations (code_hash, max_uses, uses, expires_at, revoked, bound,
		created_at) VALUES (?, ?, 0, ?, 0, ?, ?) RETURNING id`), CodeHash(code), maxUses, msPtr(expires),
		boolInt(bound), ms(now)).Scan(&id)
	return id, err
}

// Invitations lists all codes, newest first.
func (s *Store) Invitations() ([]Invitation, error) {
	rows, err := s.db.Query(`SELECT id, max_uses, uses, expires_at, revoked, bound, created_at FROM invitations
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
		if err := rows.Scan(&inv.ID, &inv.MaxUses, &inv.Uses, &exp, &inv.Revoked, &inv.Bound, &created); err != nil {
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
	s.joined_at, s.approved_at, s.last_seen_at, s.trust_operator, s.trust_penalized_at,
	(SELECT COUNT(*) FROM contributions c WHERE c.station_id = s.id), s.account, s.contributions_total,
	s.last_contribution_at,
	(SELECT COALESCE(SUM(k.n_real + k.n_false), 0) FROM contribution_kinds k
	   JOIN contributions c ON c.id = k.contribution_id WHERE c.station_id = s.id),
	(SELECT COUNT(*) FROM check_reports r WHERE r.station_id = s.id)`

type scanner interface{ Scan(...any) error }

func scanStation(r scanner) (*Station, error) {
	var st Station
	var gained, approved, seen, inv, penalized sql.NullInt64
	var joined int64
	var account sql.NullString
	var lastContrib sql.NullInt64
	if err := r.Scan(&st.ID, &st.KeyFP, &st.PublicKey, &st.Status, &st.Trust, &gained, &inv, &joined, &approved,
		&seen, &st.TrustOperator, &penalized, &st.Profiles, &account, &st.Contributions, &lastContrib, &st.Examples,
		&st.Reports); err != nil {
		return nil, err
	}
	st.Account, st.LastContrib, st.TrustPenalty = account.String, fromMS(lastContrib), fromMS(penalized)
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
//
// With a code, check (if not nil) runs before the code is used up; a code created as bound needs a check that
// accepts it. maxPerDay (0: no limit) caps the stations that may join in 24 hours, counted inside the transaction
// so that parallel joins cannot slip past it together. A key or account that left within FormerDays keeps the
// trust it had, and the key its age, so that leaving and coming back resets nothing.
func (s *Store) Join(fp, publicKey string, code *string, allowPending bool, trust float64, check InvitationCheck,
	maxPerDay int, now time.Time) (*Station, error) {
	var out *Station
	err := s.tx(func(tx *sql.Tx) error {
		existing, err := s.station(tx, fp)
		if err != nil || existing != nil {
			out = existing
			return err
		}
		if maxPerDay > 0 {
			n, err := s.joinsSince(tx, now.Add(-24*time.Hour))
			if err != nil {
				return err
			}
			if n >= maxPerDay {
				return ErrJoinLimit
			}
		}
		status := Pending
		var invID, accountVal any
		if code != nil {
			var id int64
			var bound bool
			err := tx.QueryRow(s.q(`SELECT id, bound FROM invitations
				WHERE code_hash = ? AND revoked = 0 AND uses < max_uses AND (expires_at IS NULL OR expires_at > ?)`),
				CodeHash(*code), ms(now)).Scan(&id, &bound)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrInvitationInvalid
			}
			if err != nil {
				return err
			}
			account := ""
			if check != nil {
				if account, err = check(id, bound); err != nil {
					return err
				}
			} else if bound {
				return ErrBindingRequired
			}
			// The conditions in the UPDATE repeat the check: two stations racing for the last use cannot both win
			n, err := tx.Exec(s.q(`UPDATE invitations SET uses = uses + 1 WHERE id = ? AND uses < max_uses`), id)
			if err != nil {
				return err
			}
			if used, _ := n.RowsAffected(); used == 0 {
				return ErrInvitationInvalid
			}
			status, invID = Active, id
			if account != "" {
				accountVal = account
			}
		} else if !allowPending {
			return ErrInvitationRequired
		}
		var approved any
		if status == Active {
			approved = ms(now)
		}
		joined := ms(now)
		// What a key or account left with, it comes back with
		var formerTrust float64
		var formerJoined int64
		err = tx.QueryRow(s.q("SELECT trust, joined_at FROM former_stations WHERE key_fp = ?"), fp).Scan(&formerTrust,
			&formerJoined)
		if err == nil {
			trust, joined = math.Min(trust, formerTrust), formerJoined
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if account, ok := accountVal.(string); ok {
			var lowest sql.NullFloat64
			if err := tx.QueryRow(s.q("SELECT MIN(trust) FROM former_stations WHERE account = ?"), account).Scan(
				&lowest); err != nil {
				return err
			}
			if lowest.Valid {
				trust = math.Min(trust, lowest.Float64)
			}
		}
		if _, err := tx.Exec(s.q(`INSERT INTO stations (key_fp, public_key, status, trust, invitation_id, joined_at,
			approved_at, account) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`), fp, publicKey, status, trust, invID, joined,
			approved, accountVal); err != nil {
			return err
		}
		if _, err := tx.Exec(s.q("DELETE FROM former_stations WHERE key_fp = ?"), fp); err != nil {
			return err
		}
		out, err = s.station(tx, fp)
		return err
	})
	return out, err
}

// joinsSince counts the stations that joined since t, including those that have left since.
func (s *Store) joinsSince(q querier, t time.Time) (int, error) {
	var n, former int
	if err := q.QueryRow(s.q("SELECT COUNT(*) FROM stations WHERE joined_at >= ?"), ms(t)).Scan(&n); err != nil {
		return 0, err
	}
	if err := q.QueryRow(s.q("SELECT COUNT(*) FROM former_stations WHERE joined_at >= ?"), ms(t)).Scan(
		&former); err != nil {
		return 0, err
	}
	return n + former, nil
}

// RevokeAccount revokes every station of an account and deletes what they sent, and returns their fingerprints.
func (s *Store) RevokeAccount(account string, now time.Time) ([]string, error) {
	rows, err := s.db.Query(s.q("SELECT key_fp FROM stations WHERE account = ? AND status <> ?"), account, Revoked)
	if err != nil {
		return nil, err
	}
	var fps []string
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			rows.Close()
			return nil, err
		}
		fps = append(fps, fp)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, fp := range fps {
		if _, err := s.SetStatus(fp, Revoked, now); err != nil {
			return nil, err
		}
	}
	return fps, nil
}

// JoinsSince counts the stations that joined since t, including those that have left since.
func (s *Store) JoinsSince(t time.Time) (int, error) { return s.joinsSince(s.db, t) }

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

// Leave deletes a station with everything it sent (the cascades take contributions and reports). Key, account,
// trust and age stay in former_stations for FormerDays, for a station that comes back.
func (s *Store) Leave(st *Station, now time.Time) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(s.q("DELETE FROM nonces WHERE key_fp = ?"), st.KeyFP); err != nil {
			return err
		}
		var account any
		if st.Account != "" {
			account = st.Account
		}
		if _, err := tx.Exec(s.q(`INSERT INTO former_stations (key_fp, account, trust, joined_at, left_at)
			VALUES (?, ?, ?, ?, ?) ON CONFLICT (key_fp) DO UPDATE SET account = excluded.account,
			trust = excluded.trust, joined_at = excluded.joined_at, left_at = excluded.left_at`), st.KeyFP, account,
			st.Trust, ms(st.JoinedAt), ms(now)); err != nil {
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

// SetTrust stores an automatic gain; gainedAt limits growth to once per day.
func (s *Store) SetTrust(id int64, trust float64, gainedAt time.Time) error {
	_, err := s.db.Exec(s.q("UPDATE stations SET trust = ?, trust_gained_at = ? WHERE id = ?"), trust,
		ms(gainedAt), id)
	return err
}

// PenalizeTrust stores an automatic penalty; penalizedAt limits penalties to once per day.
func (s *Store) PenalizeTrust(id int64, trust float64, penalizedAt time.Time) error {
	_, err := s.db.Exec(s.q("UPDATE stations SET trust = ?, trust_penalized_at = ? WHERE id = ?"), trust,
		ms(penalizedAt), id)
	return err
}

// SetOperatorTrust stores the operator's decision. With operator true the trust is the operator's and never
// changes automatically; false hands the station back to the automatic trust at the given value.
func (s *Store) SetOperatorTrust(id int64, trust float64, operator bool) error {
	_, err := s.db.Exec(s.q("UPDATE stations SET trust = ?, trust_operator = ? WHERE id = ?"), trust,
		boolInt(operator), id)
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
