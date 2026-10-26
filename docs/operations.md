# Running a Vinyl-Mind server

Anyone can run an instance, for example a studio for its own stations. Nothing in the server depends on a
particular host name. This page uses `vinyl-mind.example.org` as a placeholder.

## What you need

- A host with Docker (or any machine for the static binary).
- A reverse proxy that terminates TLS for a host name of its own, for example Caddy, nginx or Traefik.
- About 256 MB of memory and a fraction of a CPU. The data is a few megabytes.

The server makes **no outgoing connections**: no external APIs, not even DNS. It can run in a network without a
way to the internet.

## Container

```sh
docker build -t vinyl-mind:0.1.0 .                       # hardened bases need `docker login dhi.io`, see Dockerfile
mkdir conf && cp config.example.toml conf/config.toml    # adjust, at least join_mode and trusted_proxies
docker network create proxy                              # the network the reverse proxy shares with the server
docker compose -f compose.example.yml up -d
docker compose exec vinyl-mind vinyl-mind fingerprint    # publish this fingerprint for your stations
```

The image runs as UID 65532 on a read-only file system with a health check of its own (`vinyl-mind healthcheck`).
It has no shell. `compose.example.yml` adds `tmpfs /tmp`, no capabilities, no new privileges and resource limits.
Images built in this repository's CI are published to the GitHub Container Registry with provenance and an SBOM.

Without Docker: `CGO_ENABLED=0 go build ./cmd/vinyl-mind`, then run `vinyl-mind serve` as an unprivileged user
with `VINYL_MIND_DATA_DIR` pointing to a directory only that user can read.

## Configuration

`config.example.toml` lists every setting with its default; unknown keys stop the server instead of being ignored.
The server reads the file at start, so restart it after a change (`docker compose up -d --force-recreate`).
Paths and secrets come from the environment:

| Variable | Meaning |
|---|---|
| `VINYL_MIND_DATA_DIR` | database and server key (`/data` in the container) |
| `VINYL_MIND_CONFIG` | path of the TOML file |
| `VINYL_MIND_DATABASE_URL` | `postgres://…` to use PostgreSQL instead of SQLite; nothing else changes. `sqlite://<path>` takes the path literally, without query parameters |
| `VINYL_MIND_ADMIN_TOKEN` | at least 32 characters; switches on the read-only overview at `/admin` |
| `VINYL_MIND_LISTEN` | address to listen on, default `:8080` |

With an account service (`docs/connect.md`), `[accounts]` names its `connect_url` and its public
`binding_keys`. The hub makes no connection to it.

Three settings matter most:

- `join_mode`: `invitation` admits only stations with a code from `vinyl-mind invite`; `approval` lets stations
  without a code wait until `vinyl-mind approve`. For a server on the internet, `invitation` is the safer choice.
- `[clients]`: which client software and detector versions may take part, for example
  `allowed = ["vinyl-check/1.2.*"]` and `detector_versions = ["1"]`. Add a version when it is released, remove one
  that must not contribute any more; its stations can still see their status and leave. Empty lists accept everything.
  This keeps out versions the hub cannot combine; it is not proof that a client is genuine
  ([client-admission.md](client-admission.md)).
- `trusted_proxies`: the address of your reverse proxy. Only from there is `X-Forwarded-For` believed; without it,
  rate limits and the access log see the proxy's address for every client.

## Reverse proxy

The proxy must

- **terminate TLS** (and set HSTS); the server itself only speaks HTTP and never sees a certificate,
- serve Vinyl-Mind at the **root of its own host name** and pass the **path unchanged**: stations sign the path,
  so any rewriting breaks every request,
- pass `Host` and set `X-Forwarded-For`,
- block `/admin` from the outside (the overview also needs the token, but has no business on the internet),
- limit request bodies to 16 KiB.

Example for Caddy:

```
vinyl-mind.example.org {
	request_body {
		max_size 16KB
	}
	handle /admin* {
		respond 403
	}
	handle /v1/* {
		reverse_proxy vinyl-mind:8080
	}
	handle {
		respond 404
	}
}
```

`GET /v1/server` is unsigned, cheap and answers 200: a good target for external monitoring of reachability and
certificate. `GET /healthz` is for the proxy and the container.

## Checking from the outside

`go run ./tools/probe -url https://vinyl-mind.example.org -fingerprint SHA256:…` checks a running server the way
a station reaches it: the server key and the clock, security headers and HSTS, that `/admin` and `/healthz` are
blocked at the proxy, that unsigned and unknown requests and oversized bodies are refused. With
`-invitation <code>` (from `vinyl-mind invite -uses 1 -days 1`) it also joins, makes signed requests with a query
and leaves again, which deletes the test station; only this shows that the proxy passes path and body unchanged.

## Daily work

Everything that changes the server is a command; over the network nothing can be changed.

| Command | |
|---|---|
| `vinyl-mind fingerprint` | the server key's fingerprint, to publish |
| `vinyl-mind invite -uses 1 -days 14` | a new invitation code, shown once and kept only as a hash |
| `vinyl-mind invitations`, `revoke-invitation <id>` | list and revoke codes |
| `vinyl-mind stations` | stations with status, trust, profiles, last contact, and since when the last combination that used the station found it far (*far since*): an anchor in that state decides nothing until a combination finds it in line again |
| `vinyl-mind approve <fingerprint>` | admit a waiting station (`join_mode = "approval"`) |
| `vinyl-mind revoke <fingerprint>` | revoke a key and delete what the station sent |
| `vinyl-mind trust <fingerprint> <0..1\|auto>` | set a station's trust as the operator: 1 makes it an **anchor**, the reference every other station is held to and the word that publishes models; `auto` hands it back to the automatic trust. Either lifts the doubt about a station found far (*far since*): the operator has looked. Mark at least one station you know before anything is combined: without an anchor there is no model ([aggregation.md](aggregation.md)) |
| `vinyl-mind revoke-invitation-stations <id>` | revoke a code and every station that joined with it, for example when one station is disconnected |
| `vinyl-mind revoke-account <account>` | revoke every station of an account, when the account service closes or blocks it |
| `vinyl-mind invite -bound` | a code that is valid only with an account service's binding (`docs/connect.md`), for the pool of an account service |
| `vinyl-mind combine [-dry-run]` | combine now instead of waiting for the background job |
| `vinyl-mind models` | models with status and check reports |
| `vinyl-mind publish <version>`, `withdraw <version> -reason "…"` | the operator's decisions, for example at the start, when too few stations report |
| `vinyl-mind backup [-keep n] <dir>` | consistent copy of database and server key, while the server runs; `-keep 3` then keeps only the newest three there (for example before every update) |
| `vinyl-mind version -json` | version, the schema it writes and the schema of the database, without changing anything |
| `vinyl-mind access-log [-n 50]` | the newest requests with client address and error code: check `trusted_proxies`, look into abuse; refused requests of unknown senders appear as one line per address and answer with their count |
| `vinyl-mind events [-n 50]` | the event log: joins and departures, trust changes with their reasons, anchors far from the other anchors (`anchor_demoted`, `anchor_far`), group states, candidates, publications and withdrawals, and the operator's own commands |
| `vinyl-mind status [-prom]` | counts of stations by status and trust class, rows per table, models by status, the last combination and the state of every group, anchors and stations in doubt, joins and refusals of the last day; `-prom` writes the Prometheus text format for a textfile collector |

In a container: `docker compose exec vinyl-mind vinyl-mind <command>`. With `-json`, `invite`, `invitations`,
`stations` and `revoke-invitation-stations` write one JSON object per line to stdout and nothing else, for scripts
such as an account service that hands out invitations; errors go to stderr with a non-zero exit code.

If invitations are handed out automatically (sign-up on a website), new stations are cheap. The server limits them
to `joins_per_day` (20) in total, gives all stations you have not vouched for together `newcomer_share` of a model
in equal parts ([aggregation.md](aggregation.md)), and lets only your anchors decide about publication. Mark the
stations you know as anchors (`trust … 1`) before that; everything else follows from them.

## The server key and backups

`server-key.pem` in the data directory signs every global model.

- **If it is lost**, every station has to accept a new key explicitly; the server cannot prove it is the same one.
- **If it leaks**, anyone can sign models in the server's name.

Back up the data directory with `vinyl-mind backup <dir>` (SQLite `VACUUM INTO` plus the key, both readable only by
the owner), encrypt the backup and keep it off the server. With PostgreSQL, use `pg_dump` for the database and copy
the key file. Rotating the key is not supported yet (see the open points in the README).

## Updates

Before an update, back up with the running version (`vinyl-mind backup`, as in the example deploy); the new
version brings the database to its schema at the first start, and the old version then refuses to start. `backup`
itself never migrates. After an update to 0.1.8 or later, look at `vinyl-mind stations`: the stations the operator
set to full trust before are anchors now; a station whose trust had grown to 1 by itself is not, and the operator
decides with `trust … 1`. Without an anchor nothing is combined, and without an anchor that does not contribute
nothing is published automatically (`vinyl-mind status`, the startup log). Candidates built before 0.1.8 carry
no record of their contributors, so the stations behind them can still vote for them; decide about those by hand
(`publish`, `withdraw`) or wait for the next candidate.

Replace the image and restart. The database records its schema version; a server older than the database refuses
to start rather than damaging it. Stations keep working while the server is down: they retry later and keep using
their local models.

## Logs

The server writes to standard error: start and stop, what the configuration admits, new candidates, published and
withdrawn models, trust changes, refused signatures and replays, and errors, never request bodies. The event log in
the database (`vinyl-mind events`) keeps admissions, departures, trust changes, publications and the operator's
commands; the access log with client addresses is deleted after `access_log_days` (14). See `docs/privacy.md` for
everything that is stored.
