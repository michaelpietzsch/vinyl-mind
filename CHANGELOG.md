# Changelog

Versions of the server. The specification has its own versions (feature versions in `spec/`); a server release
names the feature versions it accepts.

## Unreleased

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
