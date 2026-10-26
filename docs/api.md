# API

The interface between a station and a Vinyl-Mind server. The formats of the documents are defined in `spec/`, the
signatures in [`spec/signing.md`](../spec/signing.md); this page lists the endpoints. `internal/client` is a
minimal station in Go that uses all of them.

## Conventions

- **TLS only.** The server itself speaks plain HTTP inside its container; the reverse proxy in front of it
  terminates TLS (see [operations](operations.md)). A station never connects without TLS.
- **The station calls, never the server.** Stations open every connection, so the server needs no access to a
  station's network.
- **Signed.** Every request except `GET /v1/server` and `GET /healthz` carries the signature headers of
  `spec/signing.md`. The path and query are signed as sent.
- **JSON** in both directions, UTF-8, at most 16 KiB per request body. Duplicate keys, unknown fields and
  numbers that are not finite are refused.
- **Bodies announce their length.** `POST` and `PUT` carry `Content-Length`; a chunked body without it is refused
  with `411 length_required`, a larger one with `413 too_large` before it is read. HTTP libraries send the length
  whenever the body is given as bytes rather than as a stream.
- **Times** are Unix seconds.
- **Errors** are `{"error": "<code>", "message": "<text for people>"}` with a fitting HTTP status. Clients decide
  by the code, people read the message.
- **The client names itself.** Every request carries `Vinyl-Mind-Client: <name>/<version>`, for example
  `vinyl-check/1.2.0`. A server may accept only some clients and versions (`GET /v1/server` lists them); others get
  `403 client_not_accepted` everywhere except `GET /v1/me` and `DELETE /v1/me`, so that a station can tell its user to
  update. The header is not signed and proves nothing about the software: it keeps out versions the hub cannot
  combine.
- **Not for browsers.** There are no CORS headers, and every answer says `nosniff`, `no-store`, no framing and no
  referrer.
- **Profile IDs** are chosen by the station at random: 16 to 64 characters of `A–Z a–z 0–9 _ -`, at least 128
  random bits (for example 16 random bytes in base64url). They never contain names or serial numbers.

## Endpoints

| Request | Who | Purpose |
|---|---|---|
| `GET /healthz` | anyone | liveness for the proxy and the container |
| `GET /v1/server` | anyone | server key and what the server accepts |
| `POST /v1/join` | a new station | register the station's key |
| `GET /v1/me` | station, also while waiting | status and everything the server holds from this station |
| `DELETE /v1/me` | station, also while waiting | leave: the key and everything sent with it are deleted |
| `PUT /v1/profiles/{profile}` | active station | replace the contribution of one profile |
| `DELETE /v1/profiles/{profile}` | active station | withdraw the contribution of one profile |
| `GET /v1/models?feature_version=…&detector_version=…&chain_class=…` | active station | current published model and candidate |
| `GET /v1/models/{version}` | active station | one model with its status |
| `PUT /v1/profiles/{profile}/checks/{version}` | active station | report how a model does on the profile's own recordings |
| `GET /admin` | operator, with token | read-only overview (HTML); off when no token is configured |

### `GET /v1/server`

```json
{
  "name": "vinyl-mind", "version": "0.1.0",
  "public_key": "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo",
  "fingerprint": "SHA256:If4x36FUomFia/hUBG/SJxt77UtqvkWqWId+9H+XIbk",
  "join": "approval",
  "feature_versions": [1],
  "clients": ["vinyl-check/1.2.*"], "detector_versions": ["1"],
  "time": 1793000000,
  "limits": {"max_body": 16384, "max_profiles": 16}
}
```

A station shows the fingerprint when it first connects, so that the user can compare it with the one the
operator publishes, and keeps the key.
`clients` and `detector_versions` are what the server accepts; empty lists mean everything. `connect_url`, when
present, is where a station connects by signing in (`docs/connect.md`). It checks `public_key` against `fingerprint`. `time` lets it notice a wrong
clock before its signed requests are refused as stale. `join` is `approval` (a station without invitation waits
for the operator) or `invitation` (a code is required).

### `POST /v1/join`

```json
{"public_key": "<base64url, 32 bytes>", "invitation": "<code, optional>", "binding": "<JWS, optional>"}
```

`binding` comes from an account service that handed out the invitation (`docs/connect.md`); it is required for
invitations created with `-bound`.

Signed with the new key itself; the key header must be the fingerprint of `public_key`. This proves that the
station holds the private key; a public key of small order, for which one signature fits every message, is
refused (`400 bad_key`).

| Status | Body or error code | Meaning |
|---|---|---|
| 201 | `{"station": "<fingerprint>", "status": "active" \| "pending", "server": "<fingerprint>"}` | joined |
| 200 | same | this key had joined before (safe to retry) |
| 403 | `invitation_invalid` | unknown, used up, expired or revoked code |
| 403 | `invitation_required` | the server only admits stations with a code |
| 403 | `client_not_accepted` | the server does not accept this client or version: update the software |
| 403 | `binding_required` | the invitation is valid only with a binding from the account service |
| 403 | `binding_invalid` | the binding is not signed by a known key, expired, or names another station or invitation |
| 403 | `revoked` | this key was revoked; a station must create a new key to join again |
| 429 | `rate_limited` | too many joins from this address (`Retry-After`) |
| 429 | `join_limit` | the server admits no more new stations today (`Retry-After`); counted inside the join, so that parallel joins cannot slip past it |

A key or account that left within 30 days comes back with the trust it had, and the key with its age.

### `GET /v1/me`

```json
{
  "station": "SHA256:…", "status": "active", "joined_at": 1793000000, "approved_at": 1793000000,
  "profiles": [{"profile_id": "…", "feature_version": 1, "detector_version": "1",
                "chain_class": "turntable_interface.mc", "kinds": ["click", "tick"], "received_at": 1793000000}],
  "reports": [{"profile_id": "…", "model_version": 12, "verdict": "not_worse", "adopted": true,
               "received_at": 1793000000}]
}
```

Only the station's own data. There is no endpoint that shows one station anything about another.

### `PUT /v1/profiles/{profile}`

Body: a contribution (`spec/v1/contribution.schema.json`). It replaces the profile's previous contribution.

| Status | Body or error code | Meaning |
|---|---|---|
| 200 | `{"profile_id": "…", "changed": true, "received_at": …}` | stored; `changed` is false when it equals the previous one |
| 400 | `bad_profile_id` | the ID does not have the form above |
| 409 | `too_many_profiles` | the station has `max_profiles` profiles already |
| 413 | `too_large` | body over 16 KiB |
| 422 | `invalid` | the document does not meet the specification; `message` names the place |
| 422 | `feature_version_not_accepted` | the server does not combine this feature version |
| 422 | `detector_version_not_accepted` | the server does not combine this detector version |

The `client` of the contribution must be the same as the `Vinyl-Mind-Client` header.

A station sends after retraining, only while no recording runs, and retries later when it is offline. An unchanged
contribution costs nothing: the server does not build a new model for it.

### `GET /v1/models`

Query: `feature_version` and `detector_version` (required), `chain_class` (optional, default `all`).

```json
{
  "published": {"version": 12, "status": "published", "feature_version": 1, "detector_version": "1",
                "chain_class": "all", "created_at": …, "published_at": …, "withdrawn_at": null, "jws": "…",
                "state_jws": "…"},
  "candidate": null
}
```

For a chain class, the server returns the model of that class if there is one and the model over all classes
otherwise; `chain_class` in the answer says which. `published` is the model published last, as long as it is
still published: a withdrawal does not bring an older model back. The response has an `ETag`; with
`If-None-Match` the server answers `304 Not Modified` when neither the published model nor the candidate changed,
so a station can poll often at no cost.

The model itself is the JWS in `jws` (see `spec/signing.md`). A station verifies it with the accepted server key
before it parses the payload, and checks the payload against `spec/v1/global-model.schema.json`. `state_jws` is
the hub's signed statement about the entry (`typ` `vinyl-mind-state+json`, payload `spec`
`vinyl-mind/model-state`, `server`, `version`, `status`, `published_at`, `withdrawn_at`, `at`), so that a
station can keep a status it was told with proof of who told it; verifying it is optional.

### `GET /v1/models/{version}`

The same entry for one version. A withdrawn model has `status: "withdrawn"`, a `reason` and no `jws`: a station
that uses it drops it and falls back to its local model or the published one, if there is one.

### `PUT /v1/profiles/{profile}/checks/{version}`

Body: a check report (`spec/v1/check-report.schema.json`): per kind, the estimate error of the local and of the
global model on the profile's own recordings with a master, and whether the station adopted the model.

| Status | Body or error code | Meaning |
|---|---|---|
| 200 | `{"verdict": "not_worse" \| "worse", "model_status": "…"}` | stored; the model may have been published or withdrawn by it |
| 404 | `not_found` | no such model |
| 409 | `withdrawn` | the model has been withdrawn (or superseded by a newer candidate) |
| 409 | `too_many_profiles` | a new profile ID beyond `max_profiles`: profiles that only report count too |
| 422 | `invalid` | the report does not meet the specification, or names a kind the model does not have |

A profile does not need a contribution of its own to report: a station may use global models without contributing.

## Errors of signed requests

| Status | Code | Meaning, and what a station does |
|---|---|---|
| 401 | `unsigned`, `malformed_signature` | headers missing or malformed: a bug in the client |
| 401 | `stale` | timestamp more than 300 s off: check the clock (compare with `time` from `/v1/server`) |
| 401 | `bad_signature` | the signature does not verify: a bug, or the request was changed on the way |
| 401 | `replayed` | the nonce was used before: create a new nonce per request (a replay does not use the station's quota) |
| 401 | `unknown_key` | the key has not joined (or the station left): join again |
| 403 | `pending` | waiting for the operator's approval: try later |
| 403 | `revoked` | the operator revoked the key: stop, tell the user. Leaving (`DELETE /v1/me`) is refused too: what the station sent is already deleted, and the key stays on record so that it cannot join again |
| 400 | `bad_client` | the `Vinyl-Mind-Client` header is missing or not `name/version` |
| 403 | `client_not_accepted` | this version is not accepted (any more): tell the user to update |
| 429 | `rate_limited` | too many requests: wait as `Retry-After` says (address limits count per IPv4 address or IPv6 /64) |
| 400 | `bad_forwarded` | a reverse proxy forwarded an entry that is not an address: a proxy misconfiguration |
| 411, 413 | `length_required`, `too_large` | a body needs a Content-Length, and at most 16 KiB |
| 415 | `unsupported_media_type` | the body must be plain `application/json` (no other type, no Content-Encoding) |
