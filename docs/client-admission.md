# Which clients may take part

Vinyl-Mind should only hear from genuine stations, preferably the builds the vinyl-check author compiles and
distributes. This page records what can be done about that, what is done, and what is decided.

## What cannot be done

A server cannot reliably tell which program is calling it when the program runs on a device its user controls (a
Raspberry Pi, a Mac, a Windows PC). Whatever a binary contains, a key, a secret or a checksum, can be extracted, and
then any script can pretend to be that build; obfuscation only makes it slower. vinyl-check's source is readable, so
how a client behaves is no secret either. A real proof would need hardware attestation (a TPM, or the attestation
services of a platform vendor), which is not practically available on a Raspberry Pi or for a Python desktop app.

## What matters instead

What must be protected is the global model and the server, and that depends on **who** takes part and **what** they
send, not on which program sends it. A genuine client with deliberately wrong judgements is more dangerous than a
rebuilt one with honest ones. That is covered by the design: joining only with an invitation, a key per station
that can be revoked, strict validation of every contribution, influence capped by trust, one vote per station, and
every station checking a model on its own recordings before using it ([security.md](security.md)).

## The measures

| | Measure | Effect | Status |
|---|---|---|---|
| 1 | Invitations only for people who use the official builds; one code per station; revoke on misuse | the strongest barrier: admission by a person, not by software | **implemented**: on the public hub an account on vinylcheck.net hands out one code per connected station, bound to the station's key ([connect.md](connect.md)); a closed account takes its stations with it (`revoke-account`); invitation codes by hand remain for self-hosted hubs |
| 2 | Accept only known client and detector versions (`[clients]` in the configuration) | keeps out versions the hub cannot combine, and versions that must no longer contribute | **implemented** |
| 3 | A release key built into each official release, signing the join request; the server accepts known release keys only | keeps out casual scripts and self-built clients; does not stop someone who extracts the key | open decision |
| 4 | Signed downloads: code signing on Windows and macOS, signed packages for the Raspberry Pi, published checksums | protects users from tampered downloads (not the server) | part of vinyl-check's release process |

Measure 2 works with the header `Vinyl-Mind-Client: <name>/<version>` on every request (`docs/api.md`). A version
that is no longer accepted can still ask for its status and leave, so the station can tell its user to update.

Automatic sign-up makes invitations cheap. The hub therefore limits new stations per day, counts all new
stations together as at most a fifth of a model, and lets them vote only after a week, weighted by trust
([aggregation.md](aggregation.md)).

Measure 3 would also exclude versions that hobbyists build from the source, which the license allows for private
use; it needs a decision before it is built, and work in vinyl-check's release pipeline.
