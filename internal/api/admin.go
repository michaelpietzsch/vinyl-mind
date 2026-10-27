package api

import (
	"crypto/subtle"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/michaelpietzsch/vinyl-mind/internal/hub"
	"github.com/michaelpietzsch/vinyl-mind/internal/store"
)

// authorized accepts the admin token as a bearer token or as the password of HTTP basic authentication (any user
// name), so that the overview opens in a browser. The comparison takes the same time whatever the input.
func (s *Server) authorized(r *http.Request) bool {
	got := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got = strings.TrimPrefix(h, "Bearer ")
	} else if _, pw, ok := r.BasicAuth(); ok {
		got = pw
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.AdminToken)) == 1
}

type modelRow struct {
	store.Model
	Tally hub.Tally
}

type groupRow struct {
	FeatureVersion  int
	DetectorVersion string
	ChainClass      string
	Profiles        int
	Stations        int
	Kinds           string
}

// admin is the operator's read-only overview. Changes go through the command line only, which keeps everything
// that can alter the server off the network.
func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AdminToken == "" {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
		return
	}
	addr := limitKey(info(r).addr)
	if !s.authorized(r) {
		// Ten wrong tokens per address in ten minutes, then nothing more is checked from there for a while
		if !s.adminFails.Allow(addr, s.now()) {
			w.Header().Set("Retry-After", "600")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many failed attempts")
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="vinyl-mind", charset="UTF-8"`)
		writeError(w, http.StatusUnauthorized, "unauthorized", "admin token required")
		return
	}
	stations, err := s.store.Stations()
	if err != nil {
		s.internal(w, err)
		return
	}
	invitations, err := s.store.Invitations()
	if err != nil {
		s.internal(w, err)
		return
	}
	models, err := s.store.Models(nil, "")
	if err != nil {
		s.internal(w, err)
		return
	}
	contribs, err := s.store.ActiveContributions()
	if err != nil {
		s.internal(w, err)
		return
	}
	log, err := s.store.AccessLog(50)
	if err != nil {
		s.internal(w, err)
		return
	}
	rows := make([]modelRow, 0, len(models))
	for _, m := range models {
		t, err := s.hub.Tally(m.Version, s.now())
		if err != nil {
			s.internal(w, err)
			return
		}
		rows = append(rows, modelRow{Model: m, Tally: t})
	}
	type gkey struct {
		fv       int
		det, cls string
	}
	groups := map[gkey]*groupRow{}
	gStations := map[gkey]map[int64]bool{}
	gKinds := map[gkey]map[string]bool{}
	for _, c := range contribs {
		k := gkey{c.FeatureVersion, c.DetectorVersion, c.ChainClass}
		if groups[k] == nil {
			groups[k] = &groupRow{FeatureVersion: k.fv, DetectorVersion: k.det, ChainClass: k.cls}
			gStations[k], gKinds[k] = map[int64]bool{}, map[string]bool{}
		}
		groups[k].Profiles++
		gStations[k][c.StationID] = true
		for kind := range c.Kinds {
			gKinds[k][kind] = true
		}
	}
	var grows []groupRow
	for k, g := range groups {
		g.Stations = len(gStations[k])
		var ks []string
		for kind := range gKinds[k] {
			ks = append(ks, kind)
		}
		sort.Strings(ks)
		g.Kinds = strings.Join(ks, ", ")
		grows = append(grows, *g)
	}
	sort.Slice(grows, func(i, j int) bool {
		a, b := grows[i], grows[j]
		if a.FeatureVersion != b.FeatureVersion {
			return a.FeatureVersion < b.FeatureVersion
		}
		if a.DetectorVersion != b.DetectorVersion {
			return a.DetectorVersion < b.DetectorVersion
		}
		return a.ChainClass < b.ChainClass
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if err := overview.Execute(w, map[string]any{
		"Fingerprint": s.fp, "Version": s.version, "Now": s.now(), "JoinMode": s.cfg.JoinMode,
		"Stations": stations, "Invitations": invitations, "Models": rows, "Groups": grows, "Log": log,
	}); err != nil {
		s.log.Error("overview", "err", err)
	}
}

var overview = template.Must(template.New("overview").Funcs(template.FuncMap{
	"t": func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") },
	"tp": func(t *time.Time) string {
		if t == nil {
			return "–"
		}
		return t.UTC().Format("2006-01-02 15:04")
	},
	"short": func(fp string) string {
		if len(fp) > 19 {
			return fp[:19] + "…"
		}
		return fp
	},
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Vinyl-Mind overview</title>
<style>
:root { --bg: #fafaf8; --fg: #1d1d1b; --muted: #6b6b66; --line: #deded8; --accent: #9a3412; }
@media (prefers-color-scheme: dark) { :root { --bg: #161615; --fg: #ecece6; --muted: #9b9b94; --line: #33332f;
  --accent: #fb923c; } }
body { background: var(--bg); color: var(--fg); font: 14px/1.45 system-ui, sans-serif; margin: 0 auto;
  max-width: 1100px; padding: 16px; }
h1 { font-size: 20px; margin: 0 0 4px; } h2 { font-size: 15px; margin: 28px 0 8px; color: var(--accent); }
p.meta { color: var(--muted); margin: 0; } code { font-size: 12px; }
.wrap { overflow-x: auto; } table { border-collapse: collapse; width: 100%; }
th, td { text-align: left; padding: 4px 10px 4px 0; border-bottom: 1px solid var(--line); white-space: nowrap; }
th { color: var(--muted); font-weight: 500; } td.num, th.num { text-align: right; }
.empty { color: var(--muted); }
</style></head><body>
<h1>Vinyl-Mind</h1>
<p class="meta">version {{.Version}} · server key <code>{{.Fingerprint}}</code> · joining: {{.JoinMode}} ·
{{t .Now}} UTC · read only</p>

<h2>Stations</h2>
{{if .Stations}}<div class="wrap"><table><tr><th>Key</th><th>Status</th><th class="num">Trust</th>
<th class="num">Profiles</th><th>Joined</th><th>Last seen</th></tr>
{{range .Stations}}<tr><td><code>{{short .KeyFP}}</code></td><td>{{.Status}}</td>
<td class="num">{{printf "%.2f" .Trust}}</td><td class="num">{{.Profiles}}</td><td>{{t .JoinedAt}}</td>
<td>{{tp .LastSeenAt}}</td></tr>{{end}}</table></div>{{else}}<p class="empty">No stations yet.</p>{{end}}

<h2>Contributions of active stations</h2>
{{if .Groups}}<div class="wrap"><table><tr><th>Feature version</th><th>Detector</th><th>Chain class</th>
<th class="num">Profiles</th><th class="num">Stations</th><th>Kinds</th></tr>
{{range .Groups}}<tr><td>{{.FeatureVersion}}</td><td>{{.DetectorVersion}}</td><td>{{.ChainClass}}</td>
<td class="num">{{.Profiles}}</td><td class="num">{{.Stations}}</td><td>{{.Kinds}}</td></tr>{{end}}
</table></div>{{else}}<p class="empty">No contributions yet.</p>{{end}}

<h2>Models</h2>
{{if .Models}}<div class="wrap"><table><tr><th class="num">Version</th><th>Group</th><th>Status</th>
<th class="num">Profiles</th><th class="num">Stations</th><th class="num">Not worse</th><th class="num">Worse</th>
<th>Created</th><th>Published</th><th>Reason</th></tr>
{{range .Models}}<tr><td class="num">{{.Version}}</td>
<td>{{.Group.FeatureVersion}} / {{.Group.DetectorVersion}} / {{.Group.ChainClass}}</td><td>{{.Status}}</td>
<td class="num">{{.Tally.Reports}}</td><td class="num">{{.Tally.Stations}}</td>
<td class="num">{{.Tally.NotWorse}}</td><td class="num">{{.Tally.Worse}}</td><td>{{t .CreatedAt}}</td>
<td>{{tp .PublishedAt}}</td><td>{{.Reason}}</td></tr>{{end}}</table></div>
{{else}}<p class="empty">No models yet.</p>{{end}}

<h2>Invitations</h2>
{{if .Invitations}}<div class="wrap"><table><tr><th class="num">ID</th><th class="num">Used</th><th>Expires</th>
<th>Revoked</th><th>Created</th></tr>
{{range .Invitations}}<tr><td class="num">{{.ID}}</td><td class="num">{{.Uses}} / {{.MaxUses}}</td>
<td>{{tp .ExpiresAt}}</td><td>{{if .Revoked}}yes{{end}}</td><td>{{t .CreatedAt}}</td></tr>{{end}}
</table></div>{{else}}<p class="empty">No invitations.</p>{{end}}

<h2>Recent requests</h2>
{{if .Log}}<div class="wrap"><table><tr><th>Time</th><th>Address</th><th>Key</th><th>Request</th>
<th class="num">Status</th></tr>
{{range .Log}}<tr><td>{{t .T}}</td><td>{{.Address}}</td><td><code>{{short .KeyFP}}</code></td>
<td>{{.Method}} {{.Path}}</td><td class="num">{{.Status}}</td></tr>{{end}}</table></div>
{{else}}<p class="empty">No requests logged.</p>{{end}}
</body></html>
`))
