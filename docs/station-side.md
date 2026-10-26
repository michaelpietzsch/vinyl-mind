# What a station needs

Vinyl-Mind is the hub; vinyl-check is the station. This page lists what vinyl-check has to implement to take part,
in the order it is needed. vinyl-check 1.1.0 implements all of it (`vinylcheck/vinylmind.py`); the page stays as
the reference for what a station does and why. The exchange itself is defined in `spec/` and `docs/api.md`;
`internal/client` is a minimal client in Go that does all of it.

## Before anything is sent

### 1. A detector version

vinyl-check has no version for its detector today. Every contribution and every model carries a
`detector_version`, and the hub never combines contributions of different detector versions, because a changed
detector changes what the features measure. vinyl-check needs one constant (for example `detect.VERSION = "1"`)
that is raised whenever the detection or the features change, and it is sent as a string.

### 2. One model per turntable profile

`learning.training_data()` collects the examples of all recordings into one model. A contribution belongs to one
turntable profile, so training must run per profile (`recordings.profile_id`), with the same rules as today
(`MIN_PER_CLASS`, `MIN_MASTER_RECORDINGS`, cross-check per recording, `MANUAL_WEIGHT`). The model over all
profiles may stay for the station's own use.

### 3. The shared scaling

`_fit()` standardises with the data's own mean and spread (`mu`, `sd`), so weights of two stations mean different
things. A contribution is sent in the fixed **shared scaling** of `spec/v1/features.json`. The conversion is exact
and needs no retraining (`spec/README.md`):

```python
coef[i + 1] = b[i + 1] * scale[i] / sd[i]
coef[0]     = b[0] + sum(b[i + 1] / sd[i] * (center[i] - mu[i]) for i in range(10))
```

The order of the features is `learning.FEATURES` (feature version 1 is exactly today's ten features). A global
model can be used directly with `_predict()` by passing `{"mu": center, "sd": scale, "b": coef}`.

A feature that took one single value in all examples of the kind (on a station without a second opinion: `gc`
always 0.5, `gc_missing` always 1) is **not informative**. `_fit()` cannot learn its weight, only trade it off
against the intercept. Report the prior's weight instead and shift the intercept (`spec/README.md`, *Features that
never vary*), and send `informative` with `false` at its place:

```python
z0 = (x0[i] - center[i]) / scale[i]          # x0[i]: the one value the feature took
coef[0] += (coef[i + 1] - prior[i + 1]) * z0  # predictions stay the same
coef[i + 1] = prior[i + 1]                    # the global model's weight, 0 without one
informative[i] = False
```

### 4. The chain class

A profile needs two coarse facts the station does not store yet: `chain.source` (`usb_turntable` or
`turntable_interface`) and `chain.cartridge` (`mm`, `mc` or `unknown`). Two fields on the profile, asked for in the
profile dialog, with `unknown` allowed for the cartridge.

## Settings → Vinyl-Mind

As described in vinyl-check's `docs/architecture.md`:

| Setting | Behaviour |
|---|---|
| Contribute to Vinyl-Mind | off by default; sends the contributions of all profiles after retraining |
| Use the global model | off by default; fetches models, checks them, uses them as the prior |
| Server | the default server's URL, defined in one place in the code and overridable in the configuration file; *Reset to default* |
| Server key | fingerprint of the server key, shown after the first connection, to compare with the one the operator publishes |
| Join | with an optional invitation code |
| Status | joined or waiting for approval, last contribution, model version in use, result of the last check |
| What is sent | the exact JSON of the next contribution, before anything leaves the station |
| Leave | deletes the station on the server (`DELETE /v1/me`) and its key locally |

## Joining

With a hub that names a `connect_url`, the station connects by signing in, as `docs/connect.md` describes (PKCE in
the browser on the same computer, device flow otherwise). The steps below are what happens underneath, and the way
for hubs without accounts.

1. `GET /v1/server`: check that `public_key` matches `fingerprint`, show the fingerprint, store the key. A different
   key later is refused unless the user accepts it explicitly. Compare `time` with the local clock and warn if they
   are more than a minute apart: signed requests are refused beyond 300 seconds.
2. Create an Ed25519 key pair **per server** (Python: `cryptography`), keep the private key in the station's state
   directory, readable only by the service user, never in the database export or a report.
3. `POST /v1/join` with the public key and the code, signed with the new key (`spec/signing.md`). The answer is
   `active` or `pending`; poll `GET /v1/me` while pending.

Every request carries `Vinyl-Mind-Client: vinyl-check/<version>`, and a contribution's `client` says the same.
If the server answers `403 client_not_accepted`, the station stops contributing and tells the user to update; the
list of accepted versions is in `GET /v1/server`. Status and leaving keep working.

`401 unknown_key` (the hub does not know the key: the station left, or the hub lost its database) and `403 revoked`
end the connection: the station forgets the hub connection and its key and offers to connect again, with a new key.
After `revoked` that is the only way; after `unknown_key` joining again with the same key would also work, but a new
one is never wrong. The profiles, their models and an adopted global model stay: they belong to the station, not to
the connection.

The hub's URL is `https`. Plain `http` only for a hub on a loopback address, for tests and development.

Every later request is signed as `spec/signing.md` describes, with a new nonce each time. The test vector there
checks the encodings. Bodies are sent as bytes with `Content-Length` (in Python: `content=` or `data=` with
`bytes`, never a generator or file stream), since chunked bodies are refused; the signature covers exactly those
bytes anyway.

## Contributing

- Each profile gets a random **profile ID** of its own (16 random bytes, base64url), stored with the profile. A new
  cartridge or a new alignment is a new profile, and so a new ID.
- After a retraining, and only while no recording runs, send `PUT /v1/profiles/{id}` for each profile that has at
  least one kind with a fitted and cross-checked model (`usable` or not: the hub filters by the self-check). From
  the station's model per kind: `coef` (converted as above), `n_real`, `n_false`, `n_manual` (the `manual` count),
  `n_master` (`n_real + n_false - n_manual`), `master_recordings`, `cv_err_est`, `cv_err_raw` (above 10 and 100
  sent as 10 and 100), `informative` when a feature did not vary, optionally `auc` (`_auc()` exists but is not
  reported by `train()` yet).
- A kind with a coefficient outside `coef_bound` (±50 in the shared scaling) is left out of the contribution, the
  other kinds are sent. Such a weight comes from a degenerate fit (for example a feature that separates the examples
  perfectly); clipping it would send a different model than the one the station uses.
- When offline or on errors: keep the newest contribution per profile, retry with growing pauses, honour
  `Retry-After` on 429. An unchanged contribution costs nothing on the server.

## Using the global model

1. Poll `GET /v1/models?feature_version=1&detector_version=…&chain_class=…` a few times a day with `If-None-Match`;
   usually the answer is 304.
2. For a new `published` or `candidate` entry: verify the JWS with the accepted server key **before** parsing the
   payload (`alg` EdDSA, `typ` `vinyl-mind-model+json`, `kid` equal to the accepted fingerprint, payload `server`
   equal to `kid`), then validate it
   against `spec/v1/global-model.schema.json` and check that feature and detector version are the station's own.
3. **Check it** on the profile's own recordings with a master: estimate the real clicks per recording once with the
   local model and once with the global one, as the cross-check does, and report both errors per kind with
   `PUT /v1/profiles/{id}/checks/{version}`.
4. **Adopt it** only if it is not worse than the local model on those recordings. Adopting means using it as the
   **prior** of the profile's training: train in the shared scaling and replace the ridge penalty towards zero by a
   penalty towards the global coefficients. The simulation (`docs/aggregation.md`) found two things:
   - the **intercept** must be pulled towards the global intercept too; left free, it adapts to the few records a
     new profile has seen and gives most of the benefit away;
   - a prior strength of about **100** (instead of `L2 = 1`) worked best for profiles with few judgements. The
     station's own check on its master recordings decides in the end, so the strength can also be chosen by it.
5. When `GET /v1/models/{version}` reports the model in use as `withdrawn`, drop it and fall back to the local
   model or the published one, if the hub names one: a withdrawal does not bring an older model back.
6. Never adopt a candidate, only a published model, and never a model with a smaller version than the one adopted
   last for the group unless the hub says so in a `state_jws` it signed: every entry carries this signed statement
   of its status (`docs/api.md`), and a station that keeps it with the model can tell a status it was given from
   one that was changed on the way. Its `seq` grows with every status change of the model: accept a statement about
   a model only if its `seq` is greater than the last one seen for that model, whatever the clocks say.

## Privacy on the station

The preview under *What is sent* shows exactly the JSON of the contribution. Nothing else ever goes to the hub: no
audio, no features of single detections, no record titles, notes, Discogs data, names or serial numbers
(`docs/privacy.md`).

## Later

- **Feature version 2**: the features relative to the measured turntable profile (vinyl-check stage 0). It gets its
  own directory in `spec/` with its own fixed scaling; stations send it once they compute it.
- The scaling of feature version 1 is fixed and never changes; the means and spreads of real stations go into
  the scaling of feature version 2.
