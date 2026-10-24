# Invalid examples

Each file must be rejected by a server; the tests check that it is.

| File | Why it is rejected |
|---|---|
| `contribution-unknown-field.json` | a field the schema does not know (here: audio, which never leaves a station) |
| `contribution-coef-length.json` | ten coefficients instead of intercept plus ten weights |
| `contribution-counts-mismatch.json` | `n_manual + n_master` does not equal `n_real + n_false` |
| `contribution-feature-version.json` | feature version 2 sent against the schema of version 1 |
| `contribution-unknown-kind.json` | a kind the feature version does not define (`crackle` is called `tick`) |
| `contribution-coef-bound.json` | a coefficient outside the plausible bound of ±50 |
