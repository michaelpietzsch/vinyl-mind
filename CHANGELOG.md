# Changelog

Versions of the server. The specification has its own versions (feature versions in `spec/`); a server release
names the feature versions it accepts.

## Unreleased

- Features that never vary: a contribution says per kind which features were `informative` (optional, one boolean
  per feature). For the others the station sends the prior's weight; the hub combines each weight only from the
  profiles that learned it, with the shares worked out again among them, and judges "far" only on those (schema 3).
- Feature version 1 is stable: its scaling never changes, a changed scaling is a new feature version.
  `gc_missing` is centred on 1, its most common value, so that a constant feature does not shift the intercept.
- `cv_err_est` and `cv_err_raw` above the schema's maximum are sent as the maximum.
- Documented: the `typ` check of a model, how `server_url` is compared, that a revoked station cannot leave.
- `sqlite://<path>` takes the path literally (a `?` is part of the name); documented and tested with the permissions.

## 0.1.6 – 2026-10-25

- `vinyl-mind backup -keep n <dir>` removes all but the newest n backups in dir afterwards; files it did not
  write stay.
- The server logs at start what it admits: join mode, accepted clients and detector versions, `connect_url` and the
  fingerprints of the binding keys.
- `vinyl-mind version -json` shows the version, the schema it writes and the database's schema, read without opening
  the database for the server (nothing is created or migrated).
- The SQLite database (with its -wal and -shm files) and the data directory are readable by the server's user only
  (0600, 0700), also when they already existed with wider permissions.

## 0.1.5 – 2026-10-25

- Connecting by signing in on a website (`docs/connect.md`): `[accounts]` with `connect_url` (named in
  `GET /v1/server`) and `binding_keys`; `POST /v1/join` takes a `binding` signed by the account service that ties the
  invitation to the station's key (403 `binding_required` / `binding_invalid`, checked before the code is used up);
  `invite -bound`; the opaque account id per station; `revoke-account`.
- `stations -json` also shows `account`, `contributions`, `last_contribution_at`, `examples` and `reports`.
- Schema 2, migrated automatically (only additions). An older server refuses a database at schema 2: back up
  before updating (`vinyl-mind backup`).
- Fuzz tests for the specification, signatures, binding and the whole HTTP interface.

- Hardening against many cheap stations (for sign-up that hands out invitations automatically): claimed example
  counts are capped (`max_evidence`); all stations below full trust together get at most `newcomer_share` (20 %) of a
  model; check-report votes are weighted by trust and count only after `min_vote_age_days` (7); at most
  `joins_per_day` (20) new stations a day. In the simulation, forty new attackers move the model as little as two.
- `vinyl-mind trust <fingerprint> <0..1>` to mark known stations; `revoke-invitation-stations <id>` revokes a code
  and every station that joined with it; `-json` (one object per line) for `invite`, `invitations`, `stations`.

## 0.1.3 – 2026-10-25

- Clients name themselves with `Vinyl-Mind-Client: <name>/<version>`. `[clients]` in the configuration lists the
  accepted clients (`name/version` or `name/prefix*`) and detector versions; others get `403 client_not_accepted`
  except for status and leaving. `GET /v1/server` shows the lists. A contribution's `client` must match the header.
- `tools/probe` checks that a client which is not on the server's list is refused.

## 0.1.2 – 2026-10-24

- Request bodies must announce their length: `POST`, `PUT` and `PATCH` without `Content-Length` are refused with
  `411 length_required`, an announced length over 16 KiB with `413` before anything is read.
- The container's base images are pinned by digest, so that builds are reproducible.

## 0.1.1 – 2026-10-24

- `vinyl-mind access-log [-n 50]` shows the newest requests with client address and station key, to check
  `trusted_proxies` and to look into abuse without the web overview.
- `tools/probe` checks a running server from the outside, the way a station reaches it, optionally with a full
  join, signed requests and leave; `-connect <ip>` for a name that does not resolve yet.

## 0.1.0 – 2026-10-24

First version, for a small group of invited stations.

- Specification v1: feature version 1 (the station's ten features with a provisional shared scaling), JSON schemas
  for contributions, global models and check reports, Ed25519 request signatures, JWS-signed models, test vectors.
- Joining with an invitation code or the operator's approval; every request signed with timestamp and nonce;
  rate limits per address, per station and for joins.
- Contributions per turntable profile, combined per feature version, detector version, kind and chain class:
  weighted by examples and self-check, each station's share capped by its trust, trimmed mean or median.
- Candidates are published when most reporting stations find them not worse and withdrawn when most find them
  worse; one vote per station.
- Read-only overview for the operator behind a token; all changes by command line (`invite`, `approve`, `revoke`,
  `combine`, `publish`, `withdraw`, `backup`).
- SQLite or PostgreSQL; container image on a hardened base, non-root, read-only.
- Simulator (`tools/simulate`) and CI with tests on Linux, macOS, Windows and PostgreSQL, govulncheck and Trivy.
