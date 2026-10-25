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
| Station | public key and its fingerprint, status (waiting, active, revoked), trust, when it joined, was approved and was last seen, which invitation it used; if it joined through an account service, the **opaque account id** from the binding (random, unrelated to e-mail or name); how many contributions it sent and when the last one came | until the station leaves; a revoked key stays so that it cannot join again |
| Invitation | the SHA-256 of the code (never the code), how often it may be and was used, expiry | until the operator deletes the database |
| Contribution, per profile | the station's random profile ID, feature, detector and client version, coarse chain class (USB turntable or turntable with interface; MM, MC or unknown), when it was trained and received; per kind the coefficients, the counts of examples and the cross-check errors | until the station replaces or withdraws it, leaves, or is revoked |
| Check report | profile ID, model version, per kind the error of the local and the global model, whether the model was adopted, the verdict | until the station leaves or is revoked, or the model is deleted |
| Global model | the combined coefficients, their spread, how many profiles, stations and examples are behind each kind | kept: models are combinations and hold nothing that belongs to a single station |
| Nonce | the random value of each signed request, with the station's key | until the request's timestamp is stale (about six minutes) |
| Access log | time, client address, station key (if the request was signed), method, path without query, status | `access_log_days` (14 days), against abuse |

The server's own log (standard error of the container) records start and stop, new candidates, published and
withdrawn models, trust changes with the station's key fingerprint, and errors. It does not record request
bodies.

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
