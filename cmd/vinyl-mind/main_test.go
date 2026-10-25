package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/config"
	"github.com/michaelpietzsch/vinyl-mind/internal/store"
)

// Closing an account on the website revokes the account's codes and every station that joined with them.
func TestRevokeInvitationStations(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.DatabaseURL = "sqlite://" + filepath.Join(cfg.DataDir, "vm.sqlite")
	a, err := open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.store.Close()
	now := time.Unix(1_790_000_000, 0)
	id, _ := a.store.AddInvitation("code-of-an-account", 3, nil, false, now)
	_, _ = a.store.AddInvitation("another-code", 3, nil, false, now)
	for _, j := range []struct{ fp, code string }{{"fp-a", "code-of-an-account"}, {"fp-b", "code-of-an-account"},
		{"fp-c", "another-code"}} {
		code := j.code
		if _, err := a.store.Join(j.fp, "pk", &code, false, 0.25, nil, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.revokeInvitationStations(id, true, now); err != nil {
		t.Fatal(err)
	}
	for fp, want := range map[string]string{"fp-a": store.Revoked, "fp-b": store.Revoked, "fp-c": store.Active} {
		if st, _ := a.store.Station(fp); st.Status != want {
			t.Errorf("%s: %s, want %s", fp, st.Status, want)
		}
	}
	invs, _ := a.store.Invitations()
	for _, i := range invs {
		if (i.ID == id) != i.Revoked {
			t.Errorf("invitation %d revoked=%v", i.ID, i.Revoked)
		}
	}
	code := "code-of-an-account"
	if _, err := a.store.Join("fp-d", "pk", &code, false, 0.25, nil, now); err == nil {
		t.Error("the revoked code must not admit another station")
	}
	if err := a.revokeInvitationStations(999, true, now); err == nil {
		t.Error("an unknown invitation is an error")
	}
}

func TestRevokeAccountCommand(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.DatabaseURL = "sqlite://" + filepath.Join(cfg.DataDir, "vm.sqlite")
	if err := run(cfg, "invite", []string{"-uses", "2", "-json"}); err != nil {
		t.Fatal(err)
	}
	if err := run(cfg, "revoke-account", []string{"-json", "no-such-account-00"}); err != nil {
		t.Fatalf("an account without stations is no error: %v", err)
	}
}
