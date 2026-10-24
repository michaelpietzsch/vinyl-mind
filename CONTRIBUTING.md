# Contributing to Vinyl-Mind

Thank you for helping. Vinyl-Mind is the hub that lets vinyl-check stations learn together, so changes here affect
every station that takes part. Please keep that in mind, especially for anything that touches what is stored, what
leaves a station, or how contributions are combined.

## Before you start

- **Changes to the specification** (`spec/`) are changes to a contract that stations implement. A new field must
  be optional within a feature version; anything else needs a new feature version. Open an issue first.
- **Privacy is a design rule, not a feature.** Nothing that identifies a person, a place or a device may be added to
  what a station sends or what the server stores (see [docs/privacy.md](docs/privacy.md)).
- **Security issues** are reported privately to the maintainer, not in a public issue.

## Making a change

1. Fork the repository and create a branch.
2. Make your change with tests and, where it affects operators or stations, documentation.
3. Run `gofmt -l .` (no output), `go vet ./...` and `go test ./...` locally. CI runs the same checks, and the
   tests once more against PostgreSQL.
4. Open the pull request with a description of what changes and why, and how you tested it. For changes to the
   aggregation, include the output of `go run ./tools/simulate` before and after.

Code, comments, commit messages and documentation are in English. Comments explain why, not what. Lines are at most
120 characters. New dependencies need a good reason: the server should stay small enough to read.

## License of contributions

Vinyl-Mind is free for non-commercial use under the [PolyForm Noncommercial License 1.0.0](LICENSE) and is licensed
separately for commercial use. So that both remain possible, by contributing you agree that your contribution is
published under the same license, and you grant the maintainer a perpetual, worldwide, royalty-free right to use,
change and license your contribution under any terms, including commercial licenses. You confirm that you wrote the
contribution yourself or have the right to submit it under these terms.
