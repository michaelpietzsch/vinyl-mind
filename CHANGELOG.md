# Changelog

Versions of the server. The specification has its own versions (feature versions in `spec/`); a server release
names the feature versions it accepts.

## 0.1.8 – 2026-10-27

The findings of the security review of 26 October 2026 (hub protocol and aggregation), schema 4.

- **Anchors.** The reference for everything is the stations the operator trusts (`vinyl-mind trust … 1`):
  without one there is no model, their coefficients define a band every other contribution is pulled into before
  it is combined, only they decide about publication, and their trust never changes automatically. Automatic
  trust stops at `trust_auto_max` (0.9). All other stations share `newcomer_share` in equal parts, whatever they
  claim; a station's claimed examples are capped across its profiles. A weight no anchor learned stays the prior.
- **Far** is now two or more learned coefficients outside the band; the penalty falls at most once a day and only
  for a changed contribution; gains stop below the full level and, from reports, go to contributing stations only.
- **Candidates and publication.** A candidate is under review for `review_hours` (48): not replaced, not published
  before; a new combination within 0.02 of it keeps it. The hash that decides whether anything changed is over the
  result, not over row ids. Contributors and stations of their accounts cannot vote for their own candidate. A
  withdrawal does not bring an older model back. Every model entry carries a signed status (`state_jws`).
  Candidates past the review age are decided by the background job.
- **Protocol.** The nonce is checked before the station's rate limit (a replay costs the station nothing); address
  limits count IPv6 per /64; `joins_per_day` is counted inside the join; a station that left comes back with its
  trust and age (30 days); public keys of small order are refused; only plain `application/json` is read (415);
  a malformed `X-Forwarded-For` is refused (400); the access log keeps the path as sent, with the error code,
  counts refused requests of unknown senders per address instead of listing them, and is capped; nonces are kept
  a day beyond their expiry.
- **Operator.** `vinyl-mind events` (the event log: joins, departures, trust changes, publications, group states,
  commands; kept a year), `vinyl-mind status [-prom]` (counts for a textfile collector, with the anchors and the
  state of every group), `trust <fp> auto`, the publication settings and a warning about missing anchors in the
  startup log. Bindings may name the hub (`aud`), checked against `[accounts] audience`; `audience_required`
  refuses bindings that do not, once every account service names the hub. `backup` never migrates the database.
- **After the second review round.** Two anchors are a reference only while they agree; an anchor is held to the
  others alone only once three others learned the coefficient, and one that is far from the other anchors is no
  anchor in that combination (one bad anchor among two or three cannot pull the good ones, nor define a
  coefficient only it learned). A group that cannot be served (no anchor, anchors disagree, nobody can publish)
  is reported once per change. The migration makes anchors only of stations whose full trust the operator set. A
  key may return five times a day. Requests without a verified key are all counted (per address, IPv6 per /64),
  written in one
  transaction off the request path. The signed status carries a `seq` that counts status changes.
- **After the third and fourth review rounds.** A coefficient has a reference only where more than half of all
  anchors stand behind one position (within the band of their median; two anchors: within √2 bands of each
  other). A minority defines nothing, be it two with a second opinion among many, colluders among the few that
  learned a coefficient, or one of two equal camps: the coefficient is kept at the published model or the prior,
  nobody is held or far on it, and the operator is told. An anchor is demoted on the coefficients every anchor
  learned, and never when the far anchors are half of all anchors or more. An anchor that the last combination
  found far (`anchor_demoted`, `anchor_far`; recorded in the database) decides nothing about publication until a
  combination finds it in line again. Each state of a group is reported at most once a day, a state that is not a
  model once a day as long as it lasts. `backup` refuses a missing database instead of creating one; the
  background job finishes its step before the process ends; a stored self-check error that is not a number
  excludes the contribution.
- **After the fifth review round.** The majority behind a coefficient is always one of all anchors, also in the
  view of an anchor that is held to the others (a minority just beyond the band could otherwise pass for the
  majority in every honest anchor's view); the position is the largest group of anchors within the band, not the
  surroundings of their median, which a minority just short of half skews; and an anchor behind the majority
  position is never far on it. `vinyl-mind trust` lifts the doubt about a station; `stations` shows
  since when a station is in doubt, `status` counts them. The scenario tests of the review are in the repository,
  with bounds, and the CI runs them: the anchor matrix, colluders at tuned offsets, the availability under honest
  heterogeneity, and the simulation sweeps with and without a second opinion.
- Documentation: the public hub the author runs, and that vinyl-check 1.1.0 implements the station side.

## 0.1.7 – 2026-10-25

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
