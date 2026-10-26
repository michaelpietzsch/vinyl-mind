// Package store keeps stations, invitations, contributions per turntable profile, global models, check reports,
// nonces and a short access log.
//
// Statements are portable between SQLite and PostgreSQL, so switching is a different database URL and nothing
// else: placeholders are written as ? and rebound for PostgreSQL, booleans are integers, times are Unix
// milliseconds, JSON lives in text columns. What is stored, and what never is, is described in docs/privacy.md;
// keep the two in step.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// SchemaVersion is the version of the tables: the first schema below plus the migrations. A database written by a
// newer server is refused.
const SchemaVersion = 4

// migrations[i] takes the schema from version i+1 to i+2. Only additions, so an older backup can always be opened.
var migrations = [][]string{
	{ // 2: invitations valid only with a binding from an account service, and the account a station belongs to
		"ALTER TABLE invitations ADD COLUMN bound INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE stations ADD COLUMN account TEXT",
		"CREATE INDEX IF NOT EXISTS stations_account ON stations (account)",
		// Counters for the operator: contributions that changed something, and when the last one came
		"ALTER TABLE stations ADD COLUMN contributions_total INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE stations ADD COLUMN last_contribution_at BIGINT",
	},
	{ // 3: which features were informative in a contribution (JSON array of booleans, NULL: all)
		"ALTER TABLE contribution_kinds ADD COLUMN informative TEXT",
	},
	{ // 4: trust given by the operator (the reference for everything else), penalties once a day, the stations
		// behind a model, the error code and count in the access log, stations that left, and the event log
		"ALTER TABLE stations ADD COLUMN trust_operator INTEGER NOT NULL DEFAULT 0",
		// Full trust that never grew automatically could only come from the operator's command: keep it as such.
		// A station that reached it by growing is not promoted; the operator decides (docs/operations.md, Updates).
		"UPDATE stations SET trust_operator = 1 WHERE trust >= 1 AND trust_gained_at IS NULL",
		"ALTER TABLE stations ADD COLUMN trust_penalized_at BIGINT",
		// Set while the last combination that used the station found it far from the reference (for an anchor:
		// from the other anchors), so that the doubt about an anchor's word outlives a restart
		"ALTER TABLE stations ADD COLUMN far_at BIGINT",
		// How often a key came back after leaving, within a day: a station that churns is held to a small limit
		"ALTER TABLE stations ADD COLUMN returns INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE stations ADD COLUMN returns_since BIGINT",
		"ALTER TABLE models ADD COLUMN contributors TEXT",
		// Grows with every status change: the sequence number of the hub's signed statement about the model
		"ALTER TABLE models ADD COLUMN state_rev INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE access_log ADD COLUMN code TEXT",
		"ALTER TABLE access_log ADD COLUMN n INTEGER NOT NULL DEFAULT 1",
		`CREATE TABLE IF NOT EXISTS former_stations (
			key_fp TEXT PRIMARY KEY, account TEXT, trust DOUBLE PRECISION NOT NULL, joined_at BIGINT NOT NULL,
			left_at BIGINT NOT NULL, returns INTEGER NOT NULL DEFAULT 0, returns_since BIGINT)`,
		`CREATE TABLE IF NOT EXISTS events (
			id {ID}, t BIGINT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, subject TEXT NOT NULL,
			detail TEXT NOT NULL)`,
		"CREATE INDEX IF NOT EXISTS events_t ON events (t)",
	},
}

const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS invitations (
	id {ID}, code_hash TEXT NOT NULL UNIQUE, max_uses INTEGER NOT NULL, uses INTEGER NOT NULL DEFAULT 0,
	expires_at BIGINT, revoked INTEGER NOT NULL DEFAULT 0, created_at BIGINT NOT NULL);
CREATE TABLE IF NOT EXISTS stations (
	id {ID}, key_fp TEXT NOT NULL UNIQUE, public_key TEXT NOT NULL, status TEXT NOT NULL,
	trust {FLOAT} NOT NULL, trust_gained_at BIGINT,
	invitation_id BIGINT REFERENCES invitations(id) ON DELETE SET NULL,
	joined_at BIGINT NOT NULL, approved_at BIGINT, last_seen_at BIGINT);
CREATE TABLE IF NOT EXISTS contributions (
	id {ID}, station_id BIGINT NOT NULL REFERENCES stations(id) ON DELETE CASCADE, profile_id TEXT NOT NULL,
	feature_version INTEGER NOT NULL, detector_version TEXT NOT NULL, client_name TEXT NOT NULL,
	client_version TEXT NOT NULL, chain_class TEXT NOT NULL, trained_at BIGINT, received_at BIGINT NOT NULL,
	UNIQUE (station_id, profile_id));
CREATE TABLE IF NOT EXISTS contribution_kinds (
	contribution_id BIGINT NOT NULL REFERENCES contributions(id) ON DELETE CASCADE, kind TEXT NOT NULL,
	coef TEXT NOT NULL, n_real INTEGER NOT NULL, n_false INTEGER NOT NULL, n_manual INTEGER NOT NULL,
	n_master INTEGER NOT NULL, master_recordings INTEGER NOT NULL, cv_err_est {FLOAT} NOT NULL,
	cv_err_raw {FLOAT} NOT NULL, auc {FLOAT}, PRIMARY KEY (contribution_id, kind));
CREATE TABLE IF NOT EXISTS models (
	version BIGINT PRIMARY KEY, feature_version INTEGER NOT NULL, detector_version TEXT NOT NULL,
	chain_class TEXT NOT NULL, status TEXT NOT NULL, inputs_hash TEXT NOT NULL, payload TEXT NOT NULL,
	jws TEXT NOT NULL, created_at BIGINT NOT NULL, published_at BIGINT, withdrawn_at BIGINT, reason TEXT);
CREATE INDEX IF NOT EXISTS models_group ON models (feature_version, detector_version, chain_class, status);
CREATE TABLE IF NOT EXISTS check_reports (
	station_id BIGINT NOT NULL REFERENCES stations(id) ON DELETE CASCADE, profile_id TEXT NOT NULL,
	model_version BIGINT NOT NULL REFERENCES models(version) ON DELETE CASCADE, adopted INTEGER NOT NULL,
	kinds TEXT NOT NULL, verdict TEXT NOT NULL, received_at BIGINT NOT NULL,
	PRIMARY KEY (station_id, profile_id, model_version));
CREATE TABLE IF NOT EXISTS nonces (
	key_fp TEXT NOT NULL, nonce TEXT NOT NULL, expires_at BIGINT NOT NULL, PRIMARY KEY (key_fp, nonce));
CREATE TABLE IF NOT EXISTS access_log (
	id {ID}, t BIGINT NOT NULL, address TEXT, key_fp TEXT, method TEXT NOT NULL, path TEXT NOT NULL,
	status INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS access_log_t ON access_log (t);
`

// Store is the database.
type Store struct {
	db *sql.DB
	pg bool
}

// Open connects to "sqlite://<path>" or "postgres://…" and creates the tables if needed. Everything after
// "sqlite://" is the file's path, taken literally: a "?" is part of the name, there are no query parameters.
func Open(url string) (*Store, error) { return open(url, true) }

// OpenAsIs connects without creating or migrating anything, for a backup: a copy made with a newer server must
// not turn the database into the newer schema on the way.
func OpenAsIs(url string) (*Store, error) { return open(url, false) }

func open(url string, migrate bool) (*Store, error) {
	s := &Store{}
	var err error
	switch {
	case strings.HasPrefix(url, "sqlite://"):
		path := strings.TrimPrefix(url, "sqlite://")
		if !migrate {
			// Opened as is, there is nothing to create: a missing file is a wrong path, not an empty database
			if _, err := os.Stat(path); err != nil {
				return nil, fmt.Errorf("no database at %s: %w", path, err)
			}
		}
		esc := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
		// Foreign keys are off by default in SQLite, and the cascades depend on them
		s.db, err = sql.Open("sqlite", "file:"+esc+
			"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
		if err == nil {
			// One connection serialises all access. At this scale (a request per station every few minutes)
			// that costs nothing and rules out SQLITE_BUSY between concurrent writers.
			s.db.SetMaxOpenConns(1)
		}
	case strings.HasPrefix(url, "postgres://"), strings.HasPrefix(url, "postgresql://"):
		s.pg = true
		s.db, err = sql.Open("pgx", url)
	default:
		return nil, fmt.Errorf("database URL must start with sqlite:// or postgres://")
	}
	if err != nil {
		return nil, err
	}
	if migrate {
		if err := s.migrate(); err != nil {
			s.db.Close()
			return nil, err
		}
	}
	if !s.pg {
		// The database holds the access log with client addresses: readable by the server's user only. SQLite
		// creates its -wal and -shm files with the database file's permissions, so later ones follow.
		path := strings.TrimPrefix(url, "sqlite://")
		for _, f := range []string{path, path + "-wal", path + "-shm"} {
			if err := os.Chmod(f, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
				s.db.Close()
				return nil, err
			}
		}
	}
	return s, nil
}

// DatabaseSchema reads the schema version of a database without opening it for the server: nothing is created,
// migrated or changed, so it is safe to ask before or next to a running server.
func DatabaseSchema(url string) (int, error) {
	var db *sql.DB
	var err error
	switch {
	case strings.HasPrefix(url, "sqlite://"):
		path := strings.TrimPrefix(url, "sqlite://")
		if _, err := os.Stat(path); err != nil {
			return 0, err
		}
		esc := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
		db, err = sql.Open("sqlite", "file:"+esc+"?mode=ro&_pragma=busy_timeout(5000)")
	case strings.HasPrefix(url, "postgres://"), strings.HasPrefix(url, "postgresql://"):
		db, err = sql.Open("pgx", url)
	default:
		return 0, fmt.Errorf("database URL must start with sqlite:// or postgres://")
	}
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var v string
	if err := db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&v); err != nil {
		return 0, err
	}
	return strconv.Atoi(v)
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Dialect is "sqlite" or "postgres".
func (s *Store) Dialect() string {
	if s.pg {
		return "postgres"
	}
	return "sqlite"
}

func (s *Store) migrate() error {
	dialect := strings.NewReplacer("{ID}", "INTEGER PRIMARY KEY", "{FLOAT}", "REAL")
	if s.pg {
		dialect = strings.NewReplacer("{ID}", "BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY",
			"{FLOAT}", "DOUBLE PRECISION")
	}
	ddl := dialect.Replace(schema)
	return s.tx(func(tx *sql.Tx) error {
		for _, stmt := range strings.Split(ddl, ";") {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("schema: %w", err)
			}
		}
		var v string
		version := 1 // a database without a version was created just now, by the first schema above
		err := tx.QueryRow(s.q("SELECT value FROM meta WHERE key = ?"), "schema_version").Scan(&v)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.Exec(s.q("INSERT INTO meta (key, value) VALUES (?, ?)"), "schema_version", "1"); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if version, err = strconv.Atoi(v); err != nil {
				return fmt.Errorf("schema version %q", v)
			}
		}
		if version > SchemaVersion {
			return fmt.Errorf("database schema %d is newer than this server (%d)", version, SchemaVersion)
		}
		for ; version < SchemaVersion; version++ {
			for _, stmt := range migrations[version-1] {
				if _, err := tx.Exec(dialect.Replace(stmt)); err != nil {
					return fmt.Errorf("migration to schema %d: %w", version+1, err)
				}
			}
		}
		_, err = tx.Exec(s.q("UPDATE meta SET value = ? WHERE key = ?"), strconv.Itoa(SchemaVersion), "schema_version")
		return err
	})
}

// q rebinds ? placeholders to $1, $2, … for PostgreSQL. No statement here has a ? inside a string literal.
func (s *Store) q(query string) string {
	if !s.pg {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Store) tx(fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func msPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}

func fromMS(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64)
	return &t
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
