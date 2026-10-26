// Command vinyl-mind runs the server and is the operator's tool: invitations, approving and revoking stations,
// combining, publishing and withdrawing models. Everything that changes the server goes through here, never
// through the network.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/api"
	"github.com/michaelpietzsch/vinyl-mind/internal/auth"
	"github.com/michaelpietzsch/vinyl-mind/internal/config"
	"github.com/michaelpietzsch/vinyl-mind/internal/hub"
	"github.com/michaelpietzsch/vinyl-mind/internal/keys"
	"github.com/michaelpietzsch/vinyl-mind/internal/store"
)

// version is set at build time (-ldflags "-X main.version=…").
var version = "dev"

const usage = `vinyl-mind – hub for vinyl-check stations that learn together

Usage: vinyl-mind [-config file] <command> [arguments]

Server
  serve                       run the server (also creates the data directory and the server key)
  healthcheck                 exit 0 if the local server answers (for container health checks)
  init                        create the data directory and the server key, print the fingerprint
  fingerprint                 print the fingerprint of the server key, to publish for stations

Stations
  invite [-uses n] [-days d] [-bound] [-json]
                              create an invitation code (shown once); -bound: valid only with an account
                              service's binding (docs/connect.md)
  invitations [-json]         list invitation codes
  revoke-invitation <id>      make a code unusable
  revoke-invitation-stations <id> [-json]
                              revoke a code and every station that joined with it
  stations [-json]            list stations
  approve <fingerprint>       admit a waiting station
  revoke <fingerprint>        revoke a station's key and delete what it sent
  revoke-account <account> [-json]
                              revoke every station of an account (from an account service's binding)
  trust <fingerprint> <0..1|auto>
                              set a station's trust as the operator: 1 makes it an anchor (the reference the
                              others are held to); "auto" hands it back to the automatic trust

With -json, commands write one JSON object per line to stdout and nothing else; errors go to stderr with a
non-zero exit code.

Models
  combine [-dry-run]          combine changed contributions into new candidates now
  models                      list models with their check reports
  publish <version>           publish a model regardless of reports
  withdraw <version> [-reason text]

  access-log [-n 50]          the newest requests: time, client address, station key, request, status, code;
                              refused requests of unknown senders are counted per address (N > 1)
  events [-n 50]              the newest events: admissions, departures, trust changes, publications, operator
                              commands
  status [-prom]              counts of stations, contributions, models, refusals; -prom in the text format of a
                              Prometheus textfile collector
  backup [-keep n] <dir>      write a consistent copy of the database and the server key into dir; -keep: then
                              remove all but the newest n copies there
  prune                       forget expired nonces and old access log entries
  version [-json]             print the version; -json also the schema this version writes and the database's

Configuration: TOML file (-config, VINYL_MIND_CONFIG or <data>/config.toml) and the environment variables
VINYL_MIND_DATA_DIR, VINYL_MIND_DATABASE_URL, VINYL_MIND_ADMIN_TOKEN, VINYL_MIND_LISTEN.
`

func main() {
	fs := flag.NewFlagSet("vinyl-mind", flag.ExitOnError)
	cfgPath := fs.String("config", "", "configuration file")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	_ = fs.Parse(os.Args[1:])
	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(2)
	}
	cmd, args := fs.Arg(0), fs.Args()[1:]
	if cmd == "version" && !slices.Contains(args, "-json") {
		fmt.Println(version)
		return
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fail(err)
	}
	if cmd == "version" {
		// Reads the database's schema without opening it for the server, so the answer changes nothing
		out := map[string]any{"version": version, "schema": store.SchemaVersion, "database_schema": nil}
		if v, err := store.DatabaseSchema(cfg.DBURL()); err == nil {
			out["database_schema"] = v
		}
		if err := jsonLine(out); err != nil {
			fail(err)
		}
		return
	}
	if cmd == "healthcheck" {
		os.Exit(healthcheck(cfg))
	}
	if err := run(cfg, cmd, args); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "vinyl-mind:", err)
	os.Exit(1)
}

type app struct {
	cfg   config.Config
	store *store.Store
	key   ed25519.PrivateKey
	hub   *hub.Hub
	log   *slog.Logger
}

// open prepares the data directory, the server key and the database. Every command but backup brings the
// database to this version's schema; a backup copies it as it is, so that a copy made with a newer server does
// not take the way back to the older one.
func open(cfg config.Config, migrate bool) (*app, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	// Also an existing directory (a volume, or one made by an older version) is for the server's user only
	if err := os.Chmod(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	key, err := keys.LoadOrCreate(cfg.ServerKeyPath())
	if err != nil {
		return nil, err
	}
	var st *store.Store
	if migrate {
		st, err = store.Open(cfg.DBURL())
	} else {
		st, err = store.OpenAsIs(cfg.DBURL())
	}
	if err != nil {
		return nil, err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return &app{cfg: cfg, store: st, key: key, hub: hub.New(cfg, st, key, log), log: log}, nil
}

func arg(args []string, i int, name string) (string, error) {
	if len(args) <= i {
		return "", fmt.Errorf("missing %s", name)
	}
	return args[i], nil
}

func versionArg(args []string) (int64, error) {
	s, err := arg(args, 0, "model version")
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(s, 10, 64)
}

func run(cfg config.Config, cmd string, args []string) error {
	known := map[string]bool{"serve": true, "init": true, "fingerprint": true, "invite": true, "invitations": true,
		"revoke-invitation": true, "revoke-invitation-stations": true, "revoke-account": true, "trust": true, "stations": true, "approve": true, "revoke": true, "combine": true, "models": true,
		"publish": true, "withdraw": true, "prune": true, "backup": true, "access-log": true, "events": true,
		"status": true}
	if !known[cmd] {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
	a, err := open(cfg, cmd != "backup")
	if err != nil {
		return err
	}
	defer a.store.Close()
	now := time.Now()
	out := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	defer out.Flush()

	switch cmd {
	case "serve":
		return a.serve()
	case "init", "fingerprint":
		fmt.Println(keys.Fingerprint(a.key.Public().(ed25519.PublicKey)))
	case "invite":
		f := flag.NewFlagSet("invite", flag.ExitOnError)
		uses := f.Int("uses", 1, "how many stations may join with the code")
		days := f.Int("days", 14, "days until the code expires (0: never)")
		asJSON := f.Bool("json", false, "one JSON object on stdout")
		bound := f.Bool("bound", false, "valid only with a binding from an account service")
		parse(f, args)
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		code := keys.Encode(b)
		var exp *time.Time
		if *days > 0 {
			t := now.Add(time.Duration(*days) * 24 * time.Hour)
			exp = &t
		}
		id, err := a.store.AddInvitation(code, *uses, exp, *bound, now)
		if err != nil {
			return err
		}
		a.event(now, "invite", strconv.FormatInt(id, 10), fmt.Sprintf("uses %d, days %d, bound %v", *uses, *days, *bound))
		if *asJSON {
			return jsonLine(map[string]any{"id": id, "code": code, "max_uses": *uses, "expires_at": unix(exp),
				"bound": *bound})
		}
		fmt.Printf("invitation %d: %s\n(shown only now; valid for %d station(s)", id, code, *uses)
		if exp != nil {
			fmt.Printf(" until %s", exp.UTC().Format("2006-01-02 15:04 UTC"))
		}
		fmt.Println(")")
	case "invitations":
		f := flag.NewFlagSet("invitations", flag.ExitOnError)
		asJSON := f.Bool("json", false, "one JSON object per line")
		parse(f, args)
		invs, err := a.store.Invitations()
		if err != nil {
			return err
		}
		if *asJSON {
			for _, i := range invs {
				if err := jsonLine(map[string]any{"id": i.ID, "uses": i.Uses, "max_uses": i.MaxUses,
					"expires_at": unix(i.ExpiresAt), "revoked": i.Revoked, "bound": i.Bound,
					"created_at": i.CreatedAt.Unix()}); err != nil {
					return err
				}
			}
			return nil
		}
		fmt.Fprintln(out, "ID\tUSED\tEXPIRES\tREVOKED\tCREATED")
		for _, i := range invs {
			fmt.Fprintf(out, "%d\t%d/%d\t%s\t%v\t%s\n", i.ID, i.Uses, i.MaxUses, when(i.ExpiresAt), i.Revoked,
				i.CreatedAt.UTC().Format(time.DateTime))
		}
	case "revoke-invitation":
		s, err := arg(args, 0, "invitation ID")
		if err != nil {
			return err
		}
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		if err := found(a.store.RevokeInvitation(id)); err != nil {
			return err
		}
		a.event(now, "revoke-invitation", s, "")
	case "revoke-invitation-stations":
		f := flag.NewFlagSet("revoke-invitation-stations", flag.ExitOnError)
		asJSON := f.Bool("json", false, "one JSON object per revoked station")
		pos := parse(f, args)
		s, err := arg(pos, 0, "invitation ID")
		if err != nil {
			return err
		}
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		return a.revokeInvitationStations(id, *asJSON, now)
	case "revoke-account":
		f := flag.NewFlagSet("revoke-account", flag.ExitOnError)
		asJSON := f.Bool("json", false, "one JSON object per revoked station")
		pos := parse(f, args)
		account, err := arg(pos, 0, "account")
		if err != nil {
			return err
		}
		fps, err := a.store.RevokeAccount(account, now)
		if err != nil {
			return err
		}
		for _, fp := range fps {
			a.event(now, "revoke", fp, "account "+account+" revoked")
			if *asJSON {
				if err := jsonLine(map[string]any{"fingerprint": fp, "account": account}); err != nil {
					return err
				}
			}
		}
		if !*asJSON {
			fmt.Printf("%d station(s) of account %s revoked\n", len(fps), account)
		}
	case "trust":
		fp, err := arg(args, 0, "station fingerprint")
		if err != nil {
			return err
		}
		v, err := arg(args, 1, "trust between 0 and 1, or auto")
		if err != nil {
			return err
		}
		st, err := a.store.Station(fp)
		if err != nil {
			return err
		}
		if st == nil {
			return errors.New("not found")
		}
		if v == "auto" {
			t := math.Min(st.Trust, cfg.Aggregation.TrustAutoMax)
			if err := a.store.SetOperatorTrust(st.ID, t, false); err != nil {
				return err
			}
			a.event(now, "trust", fp, fmt.Sprintf("%.3f to %.3f, automatic again", st.Trust, t))
			return nil
		}
		t, err := strconv.ParseFloat(v, 64)
		if err != nil || t < 0 || t > 1 {
			return fmt.Errorf("trust must be a number between 0 and 1 or \"auto\", not %q", v)
		}
		if err := a.store.SetOperatorTrust(st.ID, t, true); err != nil {
			return err
		}
		a.event(now, "trust", fp, fmt.Sprintf("%.3f to %.3f, set by the operator", st.Trust, t))
	case "stations":
		f := flag.NewFlagSet("stations", flag.ExitOnError)
		asJSON := f.Bool("json", false, "one JSON object per line")
		parse(f, args)
		sts, err := a.store.Stations()
		if err != nil {
			return err
		}
		if *asJSON {
			for _, s := range sts {
				if err := jsonLine(map[string]any{"fingerprint": s.KeyFP, "status": s.Status, "trust": s.Trust,
					"profiles": s.Profiles, "joined_at": s.JoinedAt.Unix(), "last_seen_at": unix(s.LastSeenAt),
					"invitation_id": s.InvitationID, "account": nullable(s.Account),
					"contributions": s.Contributions, "last_contribution_at": unix(s.LastContrib),
					"examples": s.Examples, "reports": s.Reports}); err != nil {
					return err
				}
			}
			return nil
		}
		fmt.Fprintln(out, "FINGERPRINT\tSTATUS\tTRUST\tPROFILES\tJOINED\tLAST SEEN")
		for _, s := range sts {
			fmt.Fprintf(out, "%s\t%s\t%.2f\t%d\t%s\t%s\n", s.KeyFP, s.Status, s.Trust, s.Profiles,
				s.JoinedAt.UTC().Format(time.DateTime), when(s.LastSeenAt))
		}
	case "approve", "revoke":
		fp, err := arg(args, 0, "station fingerprint")
		if err != nil {
			return err
		}
		if !auth.ValidFingerprint(fp) {
			return fmt.Errorf("%q is not a fingerprint (SHA256:…)", fp)
		}
		status := store.Active
		if cmd == "revoke" {
			status = store.Revoked
		}
		if err := found(a.store.SetStatus(fp, status, now)); err != nil {
			return err
		}
		a.event(now, cmd, fp, "")
	case "combine":
		f := flag.NewFlagSet("combine", flag.ExitOnError)
		dry := f.Bool("dry-run", false, "show the combination without storing anything")
		_ = f.Parse(args)
		outcomes, err := a.hub.Combine(now, *dry)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "FEATURES\tDETECTOR\tCLASS\tKIND\tPROFILES\tSTATIONS\tRESULT")
		for _, o := range outcomes {
			for kind, r := range o.Kinds {
				result := "combined"
				if r.Coef == nil {
					result = r.Reason
				}
				fmt.Fprintf(out, "%d\t%s\t%s\t%s\t%d\t%d\t%s\n", o.Group.FeatureVersion, o.Group.DetectorVersion,
					o.Group.ChainClass, kind, r.Profiles, r.Stations, result)
			}
			if o.Version > 0 {
				fmt.Fprintf(out, "\t\t\t\t\t\tnew candidate %d\n", o.Version)
			} else {
				fmt.Fprintf(out, "\t\t\t\t\t\t%s\n", o.Skipped)
			}
		}
	case "models":
		ms, err := a.store.Models(nil, "")
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "VERSION\tFEATURES\tDETECTOR\tCLASS\tSTATUS\tPROFILES\tSTATIONS\tNOT WORSE\tWORSE\tANCHORS +/-\tCREATED\tREASON")
		for _, m := range ms {
			t, err := a.hub.Tally(m.Version, now)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%d\t%d\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%.1f/%.1f\t%s\t%s\n", m.Version,
				m.Group.FeatureVersion, m.Group.DetectorVersion, m.Group.ChainClass, m.Status, t.Reports, t.Stations,
				t.NotWorse, t.Worse, t.AnchorNotWorse, t.AnchorWorse, m.CreatedAt.UTC().Format(time.DateTime), m.Reason)
		}
	case "publish":
		v, err := versionArg(args)
		if err != nil {
			return err
		}
		return found(a.hub.Publish(v, now))
	case "withdraw":
		v, err := versionArg(args)
		if err != nil {
			return err
		}
		f := flag.NewFlagSet("withdraw", flag.ExitOnError)
		reason := f.String("reason", "", "why the model is withdrawn (stations see it)")
		_ = f.Parse(args[1:])
		return found(a.hub.Withdraw(v, *reason, now))
	case "prune":
		return a.store.Prune(now, cfg.AccessLogDays)
	case "access-log":
		f := flag.NewFlagSet("access-log", flag.ExitOnError)
		n := f.Int("n", 50, "number of entries")
		_ = f.Parse(args)
		entries, err := a.store.AccessLog(*n)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "TIME\tADDRESS\tKEY\tREQUEST\tSTATUS\tCODE\tN")
		for _, e := range entries {
			// %q: the path came from the client, and a terminal must not interpret it
			fmt.Fprintf(out, "%s\t%s\t%s\t%s %q\t%d\t%s\t%d\n", e.T.UTC().Format(time.DateTime), e.Address, e.KeyFP,
				e.Method, e.Path, e.Status, e.Code, e.N)
		}
	case "events":
		f := flag.NewFlagSet("events", flag.ExitOnError)
		n := f.Int("n", 50, "number of entries")
		_ = f.Parse(args)
		events, err := a.store.Events(*n)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "TIME\tACTOR\tACTION\tSUBJECT\tDETAIL")
		for _, e := range events {
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%q\n", e.T.UTC().Format(time.DateTime), e.Actor, e.Action, e.Subject,
				e.Detail)
		}
	case "status":
		f := flag.NewFlagSet("status", flag.ExitOnError)
		prom := f.Bool("prom", false, "Prometheus text format, for a textfile collector")
		_ = f.Parse(args)
		return a.status(os.Stdout, *prom, now)
	case "backup":
		f := flag.NewFlagSet("backup", flag.ExitOnError)
		keep := f.Int("keep", 0, "afterwards keep only the newest n backups in dir (0: all)")
		dir, err := arg(parse(f, args), 0, "backup directory")
		if err != nil {
			return err
		}
		if *keep < 0 {
			return errors.New("-keep must not be negative")
		}
		stamp, err := a.backup(dir, now)
		if err != nil {
			return err
		}
		return pruneBackups(dir, stamp, *keep)
	}
	return nil
}

// warnAboutAnchors says at start what the operator may not notice otherwise: without an anchor nothing is
// combined, and without an anchor that does not contribute nothing is published automatically.
func (a *app) warnAboutAnchors() {
	stations, err := a.store.Stations()
	if err != nil {
		a.log.Error("stations", "err", err)
		return
	}
	anchors, voters := 0, 0
	for _, st := range stations {
		if st.Status == store.Active && st.TrustOperator && st.Trust >= a.cfg.Aggregation.NewcomerTrust {
			anchors++
			if st.Profiles == 0 {
				voters++
			}
		}
	}
	switch {
	case anchors == 0:
		a.log.Warn("no anchor: nothing will be combined until the operator trusts a station (vinyl-mind trust <fp> 1)")
	case voters == 0 && a.cfg.Publication.Auto:
		a.log.Warn("every anchor contributes: no candidate can be published automatically until an anchor that does "+
			"not contribute (and has its own account) checks and reports", "anchors", anchors)
	default:
		a.log.Info("anchors", "anchors", anchors, "not_contributing", voters)
	}
}

// event records an operator's command in the event log next to the server's own events.
func (a *app) event(now time.Time, action, subject, detail string) {
	if err := a.store.AddEvent(store.Event{T: now, Actor: "cli", Action: action, Subject: subject,
		Detail: detail}); err != nil {
		fmt.Fprintln(os.Stderr, "vinyl-mind: event log:", err)
	}
}

// status prints the counts an operator watches: as text, or in the Prometheus exposition format for a textfile
// collector (`vinyl-mind status -prom > vinyl-mind.prom`). Nothing in it names an address or a key.
func (a *app) status(w io.Writer, prom bool, now time.Time) error {
	type metric struct {
		name, help, labels string
		value              float64
	}
	var ms []metric
	add := func(name, help, labels string, value float64) {
		ms = append(ms, metric{name, help, labels, value})
	}
	schema, _ := a.store.Meta("schema_version")
	add("vinyl_mind_info", "Version of the server and schema of the database.",
		fmt.Sprintf(`version=%q,schema=%q`, version, schema), 1)
	stations, err := a.store.Stations()
	if err != nil {
		return err
	}
	byClass := map[string]int{}
	for _, st := range stations {
		class := "new"
		switch {
		case st.TrustOperator:
			class = "operator"
		case st.Trust >= 0.5:
			class = "established"
		}
		byClass[st.Status+"|"+class]++
	}
	for _, status := range []string{store.Pending, store.Active, store.Revoked} {
		for _, class := range []string{"operator", "established", "new"} {
			add("vinyl_mind_stations", "Stations by status and trust class (operator: trust set by the operator).",
				fmt.Sprintf(`status=%q,trust=%q`, status, class), float64(byClass[status+"|"+class]))
		}
	}
	for _, table := range []string{"contributions", "nonces", "access_log", "events", "former_stations"} {
		n, err := a.store.Count(table)
		if err != nil {
			return err
		}
		add("vinyl_mind_rows", "Rows per table (growth of nonces and access log).", fmt.Sprintf(`table=%q`, table),
			float64(n))
	}
	models, err := a.store.Models(nil, "")
	if err != nil {
		return err
	}
	byStatus := map[string]int{}
	for _, m := range models {
		byStatus[m.Status]++
	}
	for _, status := range []string{store.Candidate, store.Published, store.Withdrawn} {
		add("vinyl_mind_models", "Global models by status.", fmt.Sprintf(`status=%q`, status),
			float64(byStatus[status]))
	}
	last, _ := a.store.Meta("last_combine_at")
	lastAt, _ := strconv.ParseFloat(last, 64)
	add("vinyl_mind_last_combine_timestamp_seconds", "When contributions were last combined (0: never).", "", lastAt)
	joins, err := a.store.JoinsSince(now.Add(-24 * time.Hour))
	if err != nil {
		return err
	}
	add("vinyl_mind_joins_24h", "Stations that joined in the last 24 hours.", "", float64(joins))
	refusals, err := a.store.Refusals(now.Add(-24 * time.Hour))
	if err != nil {
		return err
	}
	for _, k := range slices.Sorted(maps.Keys(refusals)) {
		status, code, _ := strings.Cut(k, " ")
		add("vinyl_mind_refusals_24h", "Requests refused in the last 24 hours by status and error code.",
			fmt.Sprintf(`status=%q,code=%q`, status, code), float64(refusals[k]))
	}
	// The anchors: how many, how many could vote on something, and how fresh their contributions are
	anchors, voters, oldest := 0, 0, 0.0
	for _, st := range stations {
		if st.Status != store.Active || !st.TrustOperator || st.Trust < a.cfg.Aggregation.NewcomerTrust {
			continue
		}
		anchors++
		if st.Profiles == 0 {
			voters++
		} else if st.LastContrib != nil {
			oldest = math.Max(oldest, now.Sub(*st.LastContrib).Seconds())
		}
	}
	add("vinyl_mind_anchors", "Active stations the operator trusts at the full level.", "", float64(anchors))
	add("vinyl_mind_anchors_not_contributing", "Anchors without contributions: the ones that can vote on any candidate.",
		"", float64(voters))
	add("vinyl_mind_anchor_contribution_age_seconds", "Age of the oldest anchor contribution.", "", oldest)
	// What the last combination found per group
	var states []hub.GroupState
	if raw, _ := a.store.Meta("last_combine"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &states)
	}
	byState := map[string]int{}
	far := 0
	for _, g := range states {
		byState[g.State]++
		far += g.Far
	}
	for _, state := range []string{"model", "no_anchor", "anchors_disagree", "no_voter", "too_few"} {
		add("vinyl_mind_groups", "Groups by what the last combination found (no_voter: a model nobody can publish).",
			fmt.Sprintf(`state=%q`, state), float64(byState[state]))
	}
	add("vinyl_mind_far_stations", "Stations far from the reference in the last combination (all groups).", "",
		float64(far))
	seen := map[string]bool{}
	for _, m := range ms {
		if prom {
			if !seen[m.name] {
				fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", m.name, m.help, m.name)
				seen[m.name] = true
			}
			if m.labels != "" {
				fmt.Fprintf(w, "%s{%s} %g\n", m.name, m.labels, m.value)
			} else {
				fmt.Fprintf(w, "%s %g\n", m.name, m.value)
			}
		} else {
			fmt.Fprintf(w, "%s %s %g\n", m.name, m.labels, m.value)
		}
	}
	return nil
}

// backup writes the database and the server key side by side, readable only by the owner: together they are
// everything needed to restore the server, and the key must never be readable by anyone else.
func (a *app) backup(dir string, now time.Time) (stamp string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	stamp = now.UTC().Format("20060102-150405")
	db := filepath.Join(dir, "vinyl-mind-"+stamp+".sqlite")
	if err := a.store.Backup(db); err != nil {
		return "", err
	}
	if err := os.Chmod(db, 0o600); err != nil {
		return "", err
	}
	key, err := os.ReadFile(a.cfg.ServerKeyPath())
	if err != nil {
		return "", err
	}
	keyCopy := filepath.Join(dir, "vinyl-mind-"+stamp+"-server-key.pem")
	if err := os.WriteFile(keyCopy, key, 0o600); err != nil {
		return "", err
	}
	fmt.Println(db)
	fmt.Println(keyCopy)
	return stamp, nil
}

var backupName = regexp.MustCompile(`^vinyl-mind-([0-9]{8}-[0-9]{6})(\.sqlite|-server-key\.pem)$`)

// pruneBackups removes all but the newest keep backups in dir, never the one just written (stamp), even if the
// clock went back. It touches only files named as backup names them, so copies an operator put there stay. Removed
// files go to stderr: stdout lists only what was written, for scripts that pick the backup up.
func pruneBackups(dir, stamp string, keep int) error {
	if keep < 1 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	files := map[string][]string{}
	for _, e := range entries {
		if m := backupName.FindStringSubmatch(e.Name()); m != nil && e.Type().IsRegular() && m[1] != stamp {
			files[m[1]] = append(files[m[1]], e.Name())
		}
	}
	older := slices.Sorted(maps.Keys(files))
	for _, s := range older[:max(0, len(older)-(keep-1))] {
		for _, name := range files[s] {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "removed", filepath.Join(dir, name))
		}
	}
	return nil
}

// revokeInvitationStations takes back a code and every station that joined with it, for example when the account
// a code was given to is closed. The code is revoked first, so that no station can join with it in between.
func (a *app) revokeInvitationStations(id int64, asJSON bool, now time.Time) error {
	if err := found(a.store.RevokeInvitation(id)); err != nil {
		return err
	}
	a.event(now, "revoke-invitation", strconv.FormatInt(id, 10), "with its stations")
	sts, err := a.store.Stations()
	if err != nil {
		return err
	}
	n := 0
	for _, s := range sts {
		if s.InvitationID == nil || *s.InvitationID != id || s.Status == store.Revoked {
			continue
		}
		a.event(now, "revoke", s.KeyFP, fmt.Sprintf("invitation %d revoked", id))
		if _, err := a.store.SetStatus(s.KeyFP, store.Revoked, now); err != nil {
			return err
		}
		n++
		if asJSON {
			if err := jsonLine(map[string]any{"fingerprint": s.KeyFP, "invitation_id": id}); err != nil {
				return err
			}
		}
	}
	if !asJSON {
		fmt.Printf("invitation %d revoked, %d station(s) revoked\n", id, n)
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// jsonLine writes one JSON object as one line, for scripts that read the output line by line.
func jsonLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Println(string(b))
	return err
}

func unix(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := t.Unix()
	return &v
}

// parse reads flags wherever they stand, before or after the positional arguments, and returns the positionals.
func parse(f *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		_ = f.Parse(args)
		args = f.Args()
		if len(args) == 0 {
			return pos
		}
		pos, args = append(pos, args[0]), args[1:]
	}
}

func when(t *time.Time) string {
	if t == nil {
		return "–"
	}
	return t.UTC().Format(time.DateTime)
}

func found(ok bool, err error) error {
	if err == nil && !ok {
		return errors.New("not found")
	}
	return err
}

// serve runs the HTTP server and, in the background, housekeeping and combining.
func (a *app) serve() error {
	srv := api.New(a.cfg, a.store, a.key, a.hub, version, nil, a.log)
	hs := &http.Server{
		Addr: a.cfg.Listen, Handler: srv.Handler(),
		// Tight limits: the requests are small, and slow clients should not hold connections
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second,
		IdleTimeout: 120 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go a.background(ctx, srv)
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	a.log.Info("listening", "addr", a.cfg.Listen, "version", version, "database", a.store.Dialect(),
		"server_key", srv.Fingerprint())
	// What the configuration admits, so that a check after a change needs no access to the file
	bindingKeys := make([]string, 0, len(a.cfg.BindingKeys()))
	for fp := range a.cfg.BindingKeys() {
		bindingKeys = append(bindingKeys, fp)
	}
	slices.Sort(bindingKeys)
	a.log.Info("admission", "join_mode", a.cfg.JoinMode, "clients", a.cfg.Clients.Allowed,
		"detector_versions", a.cfg.Clients.DetectorVersions, "connect_url", a.cfg.Accounts.ConnectURL,
		"binding_keys", bindingKeys)
	p := a.cfg.Publication
	a.log.Info("publication", "auto", p.Auto, "review_hours", p.ReviewHours, "min_reports", p.MinReports,
		"min_report_stations", p.MinReportStations, "min_vote_age_days", p.MinVoteAgeDays,
		"trust_auto_max", a.cfg.Aggregation.TrustAutoMax)
	a.warnAboutAnchors()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	a.log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := hs.Shutdown(sctx)
	srv.Flush()
	return err
}

func (a *app) background(ctx context.Context, srv *api.Server) {
	combineEvery := time.Duration(a.cfg.Publication.CombineIntervalS) * time.Second
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	lastCombine := time.Time{}
	for {
		now := time.Now()
		srv.Flush()
		if err := a.store.Prune(now, a.cfg.AccessLogDays); err != nil {
			a.log.Error("prune", "err", err)
		}
		// A candidate that has reached the review age is decided here, not only when a report arrives
		if err := a.hub.ReviewDue(now); err != nil {
			a.log.Error("review", "err", err)
		}
		if combineEvery > 0 && now.Sub(lastCombine) >= combineEvery {
			lastCombine = now
			if _, err := a.hub.Combine(now, false); err != nil {
				a.log.Error("combine", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// healthcheck asks the local server; the container image has no shell or curl for this.
func healthcheck(cfg config.Config) int {
	addr := cfg.Listen
	if len(addr) > 0 && addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
