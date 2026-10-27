# Vinyl-Mind

**The hub for [vinyl-check](https://github.com/michaelpietzsch/vinyl-check) stations that learn together.**

vinyl-check learns from every judgement which detections on a record are real clicks and which are music. A
single station learns only from its own turntables, its own listener and its own records. Vinyl-Mind lets stations
pool what they learn without pooling their data: each station sends the small model of each turntable profile,
Vinyl-Mind combines them robustly into a global model, signs it and gives it back. A station uses it as the
starting point of its own training, and only if it does well on the station's own recordings.

> **Status: 0.1.7, in operation.** The author runs a public hub for vinyl-check 1.1 and later (below). Feature
> version 1 is a first step: the station's features are not yet relative to the turntable profile (that is
> version 2).

## How it works

- **Only knowledge travels, never audio.** A contribution is less than a kilobyte: per kind of event the weights of
  a logistic regression, the number of examples and the result of the station's own cross-check. No recordings,
  no record titles, no names ([privacy](docs/privacy.md)).
- **The station calls, never the hub.** Stations sit behind other people's routers and open every connection
  themselves.
- **Every request and every model is signed.** Stations sign with their own Ed25519 key, the hub signs models with
  its key; joining needs an invitation ([security](docs/security.md)).
- **No station can steer the result but the operator's own.** The stations the operator vouches for are the
  reference: every other contribution is held within a band around them and all of them together add a fifth,
  whatever they claim; a model is published only when the operator's stations that tried it found it no worse
  ([aggregation](docs/aggregation.md)).
- **Anyone can run a hub.** Nothing depends on a particular server; a studio can run one for its own stations
  ([operations](docs/operations.md)).

In a simulation with eight honest stations, two of them the operator's, attackers claiming millions of examples do
not move the combined model, twenty of them no more than two, and a new turntable profile with only 80 judgements
estimates the number of real clicks with about 12 % error instead of 25 % when it starts from the global model
([aggregation](docs/aggregation.md)).

## The public hub

The author runs Vinyl-Mind for everyone who uses vinyl-check: `https://api.vinylcheck.net`, the default server of
vinyl-check 1.1 and later, from the code in this repository (`GET /v1/server` names the version).

| | |
|---|---|
| Server key | `SHA256:p7xkFojVrEYTqJM6C12dyCh0SJAlxg+wN8VUSSa2auo` – compare it with what the station shows when it joins |
| Joining | *Settings → Vinyl-Mind → Connect*: sign in with an account on [vinylcheck.net](https://vinylcheck.net) ([how it works](docs/connect.md)), or an invitation code from the author |
| Accepted | vinyl-check 1.1.x with detector version 1; `GET /v1/server` lists what is accepted right now |
| What happens | contributions are combined every hour; a model is published when the stations' own checks found it no worse, and withdrawn when they stop doing so ([aggregation](docs/aggregation.md)) |
| What is kept | only what [privacy](docs/privacy.md) describes: models, counts, check results and the key fingerprints, no audio, no titles. Leaving deletes everything a station sent |
| Care | database and server key are backed up daily, encrypted, and picked up by a second system; the server key does not change |
| Contact | [info@vinylcheck.net](mailto:info@vinylcheck.net) |

Anyone can run their own hub instead ([operations](docs/operations.md)); a station can be pointed at any server.

## Quick start

```sh
mkdir conf && cp config.example.toml conf/config.toml   # set join_mode and trusted_proxies
docker network create proxy                             # shared with your TLS-terminating reverse proxy
docker compose -f compose.example.yml up -d
docker compose exec vinyl-mind vinyl-mind fingerprint   # publish this for your stations
docker compose exec vinyl-mind vinyl-mind invite        # a code for a station to join with
```

A station then enters the server's URL and the code under *Settings → Vinyl-Mind* and compares the fingerprint it
is shown with the one you published.

## Documentation

| | |
|---|---|
| [docs/overview.md](docs/overview.md) | the map: who talks to whom, from joining to using a model, the life of a model |
| [spec/](spec/README.md) | the contract with the stations: features, JSON schemas, signatures, test vectors |
| [docs/api.md](docs/api.md) | every endpoint with its answers and errors |
| [docs/aggregation.md](docs/aggregation.md) | how contributions are weighted, combined, published and withdrawn, and what the simulation shows |
| [docs/security.md](docs/security.md) | threats, measures, OWASP API Security Top 10, known limits |
| [docs/client-admission.md](docs/client-admission.md) | which clients may take part, and what can and cannot prove a genuine client |
| [docs/privacy.md](docs/privacy.md) | what is stored, for how long, and what never leaves a station |
| [docs/operations.md](docs/operations.md) | running a server: container, configuration, reverse proxy, commands, backups |
| [docs/station-side.md](docs/station-side.md) | what a station (vinyl-check) implements to take part |
| [docs/connect.md](docs/connect.md) | connecting a station by signing in on a website: PKCE, device flow, signed binding |

## Development

Go 1.27.3 or later.

```sh
go test ./...                 # all tests; VINYL_MIND_TEST_POSTGRES=postgres://… runs the storage tests on PostgreSQL too
go vet ./... && gofmt -l .
go run ./tools/simulate       # simulated stations, with and without attackers
go build ./cmd/vinyl-mind
```

| Directory | |
|---|---|
| `cmd/vinyl-mind` | the server and the operator's command line |
| `spec` | the specification, embedded into the binary |
| `internal/api` | HTTP interface and the read-only overview |
| `internal/hub` | combining, trust, publishing and withdrawing |
| `internal/store` | storage on SQLite or PostgreSQL |
| `internal/auth`, `internal/keys` | signed requests, keys, signed models |
| `internal/sim`, `tools/simulate` | the simulation of stations |
| `tools/probe` | checks a running server from the outside, as a station reaches it |
| `internal/client` | a minimal station, for the tests and as an example |

Contributions are welcome, see [CONTRIBUTING.md](CONTRIBUTING.md); security issues go to [SECURITY.md](SECURITY.md);
changes per version are in [CHANGELOG.md](CHANGELOG.md).

## Open points

- Rotating the server key (today, a new key means every station accepts it by hand).
- Feature version 2, relative to the turntable profile, together with vinyl-check.
- The scaling of feature version 2 from the statistics of real stations (version 1 keeps its scaling).
- Sharing feature vectors instead of weights (later stage, only with consent).

## License

Vinyl-Mind is free for personal and other non-commercial use under the
[PolyForm Noncommercial License 1.0.0](LICENSE). Using it commercially, including running it as a service for a
business, needs a commercial license from the author: please write to
[info@vinylcheck.net](mailto:info@vinylcheck.net).
