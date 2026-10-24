package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/spec"
)

// eachDB runs a test on SQLite and, if VINYL_MIND_TEST_POSTGRES holds a database URL, on PostgreSQL as well, so
// that "PostgreSQL is only configuration" stays true.
func eachDB(t *testing.T, fn func(t *testing.T, s *Store)) {
	t.Run("sqlite", func(t *testing.T) {
		// A space, an umlaut and (where the file system allows it) a question mark in the path, which the SQLite
		// URI must escape
		name := "my data ü?"
		if runtime.GOOS == "windows" {
			name = "my data ü"
		}
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		s, err := Open("sqlite://" + filepath.Join(dir, "vm.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		fn(t, s)
	})
	url := os.Getenv("VINYL_MIND_TEST_POSTGRES")
	if url == "" {
		return
	}
	t.Run("postgres", func(t *testing.T) {
		s, err := Open(url)
		if err != nil {
			t.Fatal(err)
		}
		reset(t, s)
		s.Close()
		if s, err = Open(url); err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		fn(t, s)
	})
}

func reset(t *testing.T, s *Store) {
	for _, tbl := range []string{"access_log", "nonces", "check_reports", "models", "contribution_kinds",
		"contributions", "stations", "invitations", "meta"} {
		if _, err := s.db.Exec("DROP TABLE IF EXISTS " + tbl + " CASCADE"); err != nil {
			t.Fatal(err)
		}
	}
}

var t0 = time.Unix(1_790_000_000, 0)

func contribution(t *testing.T) *spec.Contribution {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "spec", "v1", "examples", "contribution.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c spec.Contribution
	if _, err := spec.Validate(spec.DocContribution, body, &c); err != nil {
		t.Fatal(err)
	}
	return &c
}

func TestJoinAndInvitations(t *testing.T) {
	eachDB(t, func(t *testing.T, s *Store) {
		exp := t0.Add(time.Hour)
		if _, err := s.AddInvitation("code-one", 1, &exp, t0); err != nil {
			t.Fatal(err)
		}
		code := "code-one"
		st, err := s.Join("fp-a", "pk-a", &code, true, 0.25, t0)
		if err != nil || st.Status != Active || st.ApprovedAt == nil || st.Trust != 0.25 {
			t.Fatalf("join with code: %+v %v", st, err)
		}
		again, err := s.Join("fp-a", "pk-a", &code, true, 0.25, t0)
		if err != nil || again.ID != st.ID {
			t.Fatalf("joining again must return the same station: %+v %v", again, err)
		}
		if _, err := s.Join("fp-b", "pk-b", &code, true, 0.25, t0); !errors.Is(err, ErrInvitationInvalid) {
			t.Fatalf("used-up code: %v", err)
		}
		other := "code-two"
		if _, err := s.AddInvitation(other, 5, &exp, t0); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Join("fp-c", "pk-c", &other, true, 0.25, t0.Add(2*time.Hour)); !errors.Is(err, ErrInvitationInvalid) {
			t.Fatalf("expired code: %v", err)
		}
		invs, _ := s.Invitations()
		if _, err := s.RevokeInvitation(invs[0].ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Join("fp-c", "pk-c", &other, true, 0.25, t0); !errors.Is(err, ErrInvitationInvalid) {
			t.Fatalf("revoked code: %v", err)
		}
		if _, err := s.Join("fp-d", "pk-d", nil, false, 0.25, t0); !errors.Is(err, ErrInvitationRequired) {
			t.Fatalf("no code where one is required: %v", err)
		}
		p, err := s.Join("fp-d", "pk-d", nil, true, 0.25, t0)
		if err != nil || p.Status != Pending || p.ApprovedAt != nil {
			t.Fatalf("pending: %+v %v", p, err)
		}
		if ok, _ := s.SetStatus("fp-d", Active, t0); !ok {
			t.Fatal("approve")
		}
		if p, _ = s.Station("fp-d"); p.Status != Active || p.ApprovedAt == nil {
			t.Fatalf("approved: %+v", p)
		}
		if none, err := s.Station("fp-none"); none != nil || err != nil {
			t.Fatalf("unknown station: %v %v", none, err)
		}
	})
}

func TestContributionsReplaceAndCascade(t *testing.T) {
	eachDB(t, func(t *testing.T, s *Store) {
		code := "c"
		_, _ = s.AddInvitation(code, 10, nil, t0)
		a, _ := s.Join("fp-a", "pk-a", &code, true, 0.25, t0)
		c := contribution(t)
		changed, err := s.PutContribution(a.ID, "profile-aaaaaaaaaaaa", c, 2, t0)
		if err != nil || !changed {
			t.Fatalf("first put: %v %v", changed, err)
		}
		first, _ := s.StationContributions(a.ID)
		changed, err = s.PutContribution(a.ID, "profile-aaaaaaaaaaaa", c, 2, t0.Add(time.Minute))
		if err != nil || changed {
			t.Fatalf("unchanged put must not count as a change: %v %v", changed, err)
		}
		same, _ := s.StationContributions(a.ID)
		if same[0].ID != first[0].ID || !same[0].ReceivedAt.After(first[0].ReceivedAt) {
			t.Fatalf("unchanged put keeps the ID and updates the time: %+v", same[0])
		}
		if !same[0].same(c) || same[0].ChainClass != "turntable_interface.mc" {
			t.Fatalf("round trip lost data: %+v", same[0])
		}
		k := c.Kinds["click"]
		k.Coef = append([]float64(nil), k.Coef...)
		k.Coef[1] += 0.5
		c.Kinds["click"] = k
		if changed, _ = s.PutContribution(a.ID, "profile-aaaaaaaaaaaa", c, 2, t0); !changed {
			t.Fatal("changed coefficients must count as a change")
		}
		_, _ = s.PutContribution(a.ID, "profile-bbbbbbbbbbbb", c, 2, t0)
		if _, err := s.PutContribution(a.ID, "profile-cccccccccccc", c, 2, t0); !errors.Is(err, ErrTooManyProfiles) {
			t.Fatalf("third profile over a limit of two: %v", err)
		}
		all, _ := s.ActiveContributions()
		if len(all) != 2 || len(all[0].Kinds) != 2 || all[0].StationFP != "fp-a" {
			t.Fatalf("active contributions: %+v", all)
		}
		if ok, _ := s.DeleteContribution(a.ID, "profile-bbbbbbbbbbbb"); !ok {
			t.Fatal("delete")
		}
		// Revoking deletes contributions; leaving deletes the station itself
		if _, err := s.SetStatus("fp-a", Revoked, t0); err != nil {
			t.Fatal(err)
		}
		if all, _ = s.ActiveContributions(); len(all) != 0 {
			t.Fatalf("revoked station still contributes: %+v", all)
		}
		b, _ := s.Join("fp-b", "pk-b", &code, true, 0.25, t0)
		_, _ = s.PutContribution(b.ID, "profile-dddddddddddd", c, 2, t0)
		if err := s.Leave(b); err != nil {
			t.Fatal(err)
		}
		var n int
		_ = s.db.QueryRow("SELECT COUNT(*) FROM contribution_kinds").Scan(&n)
		if st, _ := s.Station("fp-b"); st != nil || n != 0 {
			t.Fatalf("leave left data behind: %+v, %d kinds", st, n)
		}
	})
}

func TestNonces(t *testing.T) {
	eachDB(t, func(t *testing.T, s *Store) {
		if ok, err := s.UseNonce("fp", "n1", t0); !ok || err != nil {
			t.Fatalf("first use: %v %v", ok, err)
		}
		if ok, err := s.UseNonce("fp", "n1", t0); ok || err != nil {
			t.Fatalf("replay must be refused: %v %v", ok, err)
		}
		if ok, _ := s.UseNonce("other", "n1", t0); !ok {
			t.Fatal("nonces are per key")
		}
		if err := s.Prune(t0.Add(time.Second), 14); err != nil {
			t.Fatal(err)
		}
		if ok, _ := s.UseNonce("fp", "n1", t0.Add(time.Minute)); !ok {
			t.Fatal("pruned nonce")
		}
	})
}

func TestModelsAndReports(t *testing.T) {
	eachDB(t, func(t *testing.T, s *Store) {
		g := Group{FeatureVersion: 1, DetectorVersion: "1", ChainClass: spec.ClassAll}
		build := func(v int64) ([]byte, string, error) {
			b, _ := json.Marshal(map[string]int64{"version": v})
			return b, "jws", nil
		}
		v1, err := s.AddModel(g, "h1", build, t0)
		if err != nil || v1 != 1 {
			t.Fatalf("first model: %d %v", v1, err)
		}
		v2, _ := s.AddModel(g, "h2", build, t0)
		m1, _ := s.Model(v1)
		if m1.Status != Withdrawn || m1.Reason != "superseded" || m1.Payload != `{"version":1}` {
			t.Fatalf("older candidate must be superseded: %+v", m1)
		}
		if h, _ := s.LatestInputsHash(g); h != "h2" {
			t.Fatalf("inputs hash: %q", h)
		}
		if ok, _ := s.SetModelStatus(v2, Published, "", t0); !ok {
			t.Fatal("publish")
		}
		latest, _ := s.Latest(g, Published)
		if latest == nil || latest.Version != v2 || latest.PublishedAt == nil {
			t.Fatalf("latest published: %+v", latest)
		}
		if none, _ := s.Latest(Group{FeatureVersion: 1, DetectorVersion: "2", ChainClass: spec.ClassAll},
			Published); none != nil {
			t.Fatal("groups must not mix")
		}
		code := "c"
		_, _ = s.AddInvitation(code, 10, nil, t0)
		a, _ := s.Join("fp-a", "pk-a", &code, true, 0.25, t0)
		r := Report{StationID: a.ID, ProfileID: "p1", ModelVersion: v2, Adopted: true, Verdict: Worse,
			Kinds:      map[string]spec.KindCheck{"click": {LocalErr: 0.1, GlobalErr: 0.2, MasterRecordings: 2}},
			ReceivedAt: t0}
		_ = s.PutReport(r)
		r.Verdict = NotWorse
		if err := s.PutReport(r); err != nil {
			t.Fatal(err)
		}
		rs, _ := s.Reports(v2, 0)
		if len(rs) != 1 || rs[0].Verdict != NotWorse || !rs[0].Adopted || rs[0].Kinds["click"].GlobalErr != 0.2 {
			t.Fatalf("report must be replaced: %+v", rs)
		}
	})
}

func TestAccessLog(t *testing.T) {
	eachDB(t, func(t *testing.T, s *Store) {
		_ = s.LogAccess(AccessEntry{T: t0, Address: "192.0.2.1", Method: "GET", Path: "/v1/server", Status: 200})
		_ = s.LogAccess(AccessEntry{T: t0.Add(20 * 24 * time.Hour), KeyFP: "fp", Method: "PUT", Path: "/v1/x",
			Status: 204})
		if err := s.Prune(t0.Add(20*24*time.Hour), 14); err != nil {
			t.Fatal(err)
		}
		log, _ := s.AccessLog(10)
		if len(log) != 1 || log[0].KeyFP != "fp" || log[0].Address != "" {
			t.Fatalf("access log after pruning: %+v", log)
		}
	})
}
