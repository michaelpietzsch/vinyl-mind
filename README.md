# Vinyl-Mind

**The hub for [vinyl-check](https://github.com/michaelpietzsch/vinyl-check) stations that learn together.**

vinyl-check learns from every judgement which detections on a record are real clicks and which are music. A
single station learns only from its own turntables, its own listener and its own records. Vinyl-Mind lets stations
pool what they learn without pooling their data: each station sends the small model of each turntable profile,
Vinyl-Mind combines them robustly into a global model, signs it and gives it back. A station uses it as the
starting point of its own training, and only if it does well on the station's own recordings.

> **Status: 0.1, alpha.** Feature version 1 is provisional: the station's features are not yet relative to the
> turntable profile, and its shared scaling is replaced by values from real stations before the first real
> contributions. The server, the specification and the security model are complete for a first, small group of
> invited stations.

## How it works

- **Only knowledge travels, never audio.** A contribution is a few hundred bytes: per kind of event the weights of
  a logistic regression, the number of examples and the result of the station's own cross-check. No recordings,
  no record titles, no names ([privacy](docs/privacy.md)).
- **The station calls, never the hub.** Stations sit behind other people's routers and open every connection
  themselves.
- **Every request and every model is signed.** Stations sign with their own Ed25519 key, the hub signs models with
  its key; joining needs an invitation ([security](docs/security.md)).
- **No single station can steer the result.** Weighted by examples and self-check, capped per station by trust,
  combined by a trimmed mean, published only when most stations that tried it found it no worse
  ([aggregation](docs/aggregation.md)).
- **Anyone can run a hub.** Nothing depends on a particular server; a studio can run one for its own stations
  ([operations](docs/operations.md)).

In a simulation with eight stations, two attackers claiming millions of examples do not move the combined model,
and a new turntable profile with only 80 judgements estimates the number of real clicks with about 10 % error
instead of 25 % when it starts from the global model.

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
| [spec/](spec/README.md) | the contract with the stations: features, JSON schemas, signatures, test vectors |
| [docs/api.md](docs/api.md) | every endpoint with its answers and errors |
| [docs/aggregation.md](docs/aggregation.md) | how contributions are weighted, combined, published and withdrawn, and what the simulation shows |
| [docs/security.md](docs/security.md) | threats, measures, OWASP API Security Top 10, known limits |
| [docs/privacy.md](docs/privacy.md) | what is stored, for how long, and what never leaves a station |
| [docs/operations.md](docs/operations.md) | running a server: container, configuration, reverse proxy, commands, backups |
| [docs/station-side.md](docs/station-side.md) | what a station (vinyl-check) implements to take part |

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

Contributions are welcome, see [CONTRIBUTING.md](CONTRIBUTING.md); security issues go to [SECURITY.md](SECURITY.md).

## Open points

- Rotating the server key (today, a new key means every station accepts it by hand).
- Feature version 2, relative to the turntable profile, together with vinyl-check.
- The fixed scaling of feature version 1 from real stations.
- Sharing feature vectors instead of weights (later stage, only with consent).

## License

Vinyl-Mind is free for personal and other non-commercial use under the
[PolyForm Noncommercial License 1.0.0](LICENSE). Using it commercially, including running it as a service for a
business, needs a commercial license from the author: please write to
[pietzsch@michaelpietzsch.de](mailto:pietzsch@michaelpietzsch.de).
