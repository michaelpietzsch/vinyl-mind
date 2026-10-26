# Privacy

Vinyl-Mind is built so that it knows as little as possible: enough to combine models and to keep out abuse,
nothing about the people, places, records or recordings behind a station. This page lists what a server stores and
what it never sees. If a change to the code would alter this list, the change must update this page too.

## What never leaves a station

Audio, of any length. Features of single detections. Record titles, catalogue numbers, Discogs data, notes,
photos. Names of people, studios or places. Hardware serial numbers, device names, host names. Credentials.

The contribution format (`spec/v1/contribution.schema.json`) has no field for any of this, and the server refuses
documents with fields it does not know.

## What the server stores

| What | Content | How long |
|---|---|---|
| Station | public key and its fingerprint, status (waiting, active, revoked), trust and whether the operator set it, when it joined, was approved, was last seen, last gained and last lost trust, which invitation it used; if it joined through an account service, the **opaque account id** from the binding (random, unrelated to e-mail or name); how many contributions it sent and when the last one came | until the station leaves; a revoked key stays so that it cannot join again |
| Former station | fingerprint, account id, trust and when it joined and left | 30 days after leaving, so that a key or account that comes back keeps its trust |
| Invitation | the SHA-256 of the code (never the code), how often it may be and was used, expiry | until the operator deletes the database |
| Contribution, per profile | the station's random profile ID, feature, detector and client version, coarse chain class (USB turntable or turntable with interface; MM, MC or unknown), when it was trained and received; per kind the coefficients, the counts of examples and the cross-check errors | until the station replaces or withdraws it, leaves, or is revoked |
| Check report | profile ID, model version, per kind the error of the local and the global model, whether the model was adopted, the verdict | until the station leaves or is revoked, or the model is deleted |
| Global model | the combined coefficients, their spread, how many profiles, stations and examples are behind each kind, and which stations and accounts contributed (ids) | kept: models are combinations and hold nothing that belongs to a single station |
| Nonce | the random value of each signed request, with the station's key | a day beyond the request's timestamp, then pruned |
| Access log | time, client address, station key (if the request was signed), method, path without query (as sent, not decoded), status and error code; refused requests of unknown senders (rate limit, unknown path) are counted per address and answer, not listed | `access_log_days` (14 days) and at most 500 000 rows, against abuse |
| Event log | time, actor (server, review, command line), action, subject (station key, model version or invitation id) and a short detail: joins and departures, trust changes, candidates, publications and withdrawals, the operator's commands; never an invitation code | until the operator deletes the database (`vinyl-mind events`) |

The server's own log (standard error of the container) records start and stop, new candidates, published and
withdrawn models, trust changes with the station's key fingerprint, refused signatures and replays with the
client address, and errors. It does not record request bodies.

An account service that hands out invitations (`docs/connect.md`) keeps the personal data of its accounts (e-mail,
name) itself; the hub only ever sees the opaque account id.

## Leaving and revoking

`DELETE /v1/me` deletes the station with its contributions, check reports and nonces. Revoking a key
(`vinyl-mind revoke`) deletes the contributions and reports too and keeps only the key with the status revoked.
Global models built earlier stay; they are averages over several stations and cannot be traced back to one.

## What the operator sees

The overview and the command line show stations by key fingerprint, their status, trust and number of profiles,
the contributions per group, the models with their reports, and the recent requests including client addresses
(`vinyl-mind access-log`).
There is nothing more to see: a profile ID is random, and the coefficients of a contribution describe how a
detector behaves on a chain, not what was played on it.

## Backups

The data directory holds the database and the server's signing key. A backup of it contains everything in the
table above and the key that signs models; keep it as carefully as the server.
