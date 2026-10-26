# Security

What Vinyl-Mind protects, against whom, and how. The measures are checked against the
[OWASP API Security Top 10 (2023)](https://owasp.org/API-Security/editions/2023/en/0x11-t10/) below. Vulnerabilities
are reported privately, see [SECURITY.md](../SECURITY.md).

## What is at stake

| Asset | Why it matters |
|---|---|
| the global models | stations use them to estimate how many detections are real; a poisoned model would mislead every station that adopts it |
| the server's signing key | whoever holds it can sign models in the server's name |
| the stations' keys | whoever holds one can contribute and report as that station |
| what stations send | little and anonymous by design (`docs/privacy.md`), but it is theirs |

## Threats and answers

| Threat | Answer |
|---|---|
| someone on the network reads or changes traffic | TLS at the reverse proxy; on top, every request is signed by the station and every model by the server, so a changed request or model is refused even if TLS were broken |
| a recorded request is sent again | timestamp within ±300 s and a nonce that is accepted only once per key; the server key's fingerprint is part of the signed message, so a request is worthless on another server |
| the server's database is copied | it holds only public keys and hashes of invitation codes: nothing that lets anyone act as a station or join |
| many fake stations | joining needs an invitation code (or the operator's approval); joins are rate-limited per address (IPv6: per /64) and per day for the whole server, counted inside the join; all stations the operator has not vouched for share 20 % of a model in equal parts, whatever they claim; their votes decide nothing; claimed example counts are capped per profile and per station (`docs/aggregation.md`) |
| a station sends bad or deliberately wrong weights | self-check filter; every contribution is pulled into a band around the operator's anchors before it is combined, so its influence is bounded by the band, not by the coefficient bound; robust combination (trimmed mean or median); a contribution outside the band on two or more coefficients costs trust, once a day; automatic trust never reaches the anchors' level |
| patient stations that behave well and then turn | automatic trust stops below the full level, so they stay in the 20 % group; a model needs an anchor; anchors are never demoted automatically; a candidate is published only after the review window, and only by anchors that did not contribute to it |
| an invitation from an account service is intercepted | it travels only from the account service to the station (PKCE, never in a URL), and it is bound to the station's key by a signed binding: no other key can use it (`docs/connect.md`) |
| the account service is broken into | the hub has no endpoint for it; the damage is the few invitations in its pool, each revocable, and its signing key alone gives nothing without them |
| a station tries to decide about a model alone | one vote per station on check reports, decisions need several stations and the anchors' word; contributors cannot vote for their own candidate; profiles that only report count against the profile limit |
| a poisoned model gets published anyway | every station checks a model on its own recordings before it uses it, and reports; a model the anchors find worse is withdrawn, and a withdrawal does not bring an older model back by itself |
| a key that is no key | public keys of small order (for which one signature fits every message) are refused at the join |
| a station is compromised | the operator revokes its key (`vinyl-mind revoke`); its contributions and reports are deleted at once |
| the server is compromised | the attacker can sign models. Stations still adopt a model only if it does well on their own recordings. The key lives only in the data directory and its encrypted backup |
| the network is flooded | body limit 16 KiB, header limit 16 KiB, only plain JSON, tight timeouts, token buckets per address and per station (a replay does not use the station's quota), a profile limit per station; refused requests of unknown senders are counted per address, not listed; nothing grows without bound (nonces expire, the access log is pruned and capped) |

## OWASP API Security Top 10 (2023)

| Risk | How Vinyl-Mind meets it |
|---|---|
| API1 Broken object level authorization | every object a station can touch is keyed by the station itself: contributions and reports are stored and looked up per station, and no endpoint returns another station's data. Models are shared by design and readable only by active stations |
| API2 Broken authentication | one mechanism for every station request: Ed25519 signatures with timestamp and nonce over method, path, query, body hash and server key. Joining proves possession of the private key. No passwords, no bearer tokens for stations. The operator's overview needs a token of at least 32 characters, compared in constant time, and ten wrong tokens from an address block it for ten minutes |
| API3 Broken object property level authorization | strict JSON schemas with `additionalProperties: false`, duplicate keys and unknown fields refused; answers contain only what the station may see (no trust values, no other stations) |
| API4 Unrestricted resource consumption | limits on body and header size, timeouts, rate limits per address, per station and for joins, at most `max_profiles_per_station` profiles, retention for nonces and the access log |
| API5 Broken function level authorization | there are no administrative endpoints for stations; everything that changes the server is a command on the server. Waiting stations may only read their status and leave |
| API6 Unrestricted access to sensitive business flows | joining needs an invitation; contributing and reporting are limited per station, and their influence on models is capped by trust and by one vote per station |
| API7 Server side request forgery | the server never makes outgoing requests; it can run in a network without a route to the internet |
| API6 (addition) | which client versions may take part is a configured list; it keeps incompatible versions out, but a header proves nothing, so admission rests on invitations and keys ([client-admission.md](client-admission.md)) |
| API8 Security misconfiguration | no CORS, `nosniff`, `no-store`, no framing, no referrer, a CSP of `default-src 'none'`; errors carry a code and a short message, never internals; unknown configuration keys stop the server; the container runs as non-root on a read-only file system without capabilities |
| API9 Improper inventory management | one versioned API (`/v1`), every endpoint documented in `docs/api.md`, no debug or metrics endpoints; unknown paths answer 404 |
| API10 Unsafe consumption of APIs | the server consumes no other APIs |

## Building and shipping

- Go with few dependencies, `go.sum` pinned, Go toolchain at least 1.27.3 (older versions have known
  vulnerabilities in the standard library).
- Fuzz tests feed arbitrary input to the parser of the specification, the signature headers, the binding, the
  whole HTTP interface and signed contributions on their way into the database: the server must never answer with
  a server error or crash, and must never accept or store what breaks the rules. A minute of each found nothing
  (several million inputs); `go test -fuzz=<name> -fuzztime=…` runs them longer.
- CI: `gofmt`, `go vet`, tidy modules, `govulncheck`, tests with the race detector on Linux, macOS and Windows,
  storage tests on PostgreSQL, a container smoke test, and a Trivy scan of the image that fails on fixable HIGH or
  CRITICAL findings. GitHub Actions are pinned by commit and keep no credentials in the checkout. Dependabot
  updates actions, modules and base images.
- The image is a static binary on a hardened base (Docker Hardened Images, pinned by digest), without shell,
  published only from `main` and version tags, with provenance and SBOM.
- Request bodies must announce their length: chunked bodies get 411, announced oversized ones 413 before anything is
  read.

## Known limits

- The server key cannot be rotated yet. Losing it means every station accepts a new key by hand.
- Feature version 1 does not yet relate the features to the turntable profile, so weights of very different
  chains combine less well; that limits the benefit, not the safety.
- Everything the hub combines is the stations' own word, and automatic trust is a heuristic that consistency can
  fake. The hub therefore builds on the operator's word: the stations he marks as anchors define the reference and
  decide about publication, everyone else adds at most a fifth within a band around them. Against anchors that
  turn, the hub holds them to the other anchors and reports it; against a majority of anchors that collude there
  is no defence but the stations' own checks and the operator.
- Without an anchor there is no model: the operator has to mark at least one station before anything is combined.
- `401 unknown_key` tells whoever knows a fingerprint whether that key is registered. That is deliberate: a
  station needs it to join again after it left or the hub lost its database, and fingerprints are not guessable.
