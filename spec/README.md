# Vinyl-Mind specification

This directory is the contract between a station (vinyl-check or any other client) and a Vinyl-Mind server. A
station implements it without copying code from this repository: the JSON schemas, the feature definitions and the
two documents below are everything it needs. The server validates every document it receives against the same
files.

| File | Content |
|---|---|
| [`v1/features.json`](v1/features.json) | feature version 1: kinds, features in their order, fixed shared scaling, coefficient bound |
| [`v1/contribution.schema.json`](v1/contribution.schema.json) | what a turntable profile contributes |
| [`v1/global-model.schema.json`](v1/global-model.schema.json) | the payload of a signed global model |
| [`v1/check-report.schema.json`](v1/check-report.schema.json) | how a profile reports a global model on its own master recordings |
| [`v1/examples/`](v1/examples) | valid documents, and in `invalid/` documents a server must reject |
| [`signing.md`](signing.md) | station keys, signed requests, signed models |

## Versions

A **feature version** fixes which features a model uses, how they are computed and how they are scaled. Weights
are only comparable within one feature version, so the server never combines contributions of different feature
versions, and it never combines different **detector versions** either (a changed detector changes what the
features measure). Each feature version has its own directory with a complete set of schemas. Within a version,
changes are additive and optional, so an older station stays valid.

| Feature version | Status | Features |
|---|---|---|
| 1 | provisional | the ten features of the station's model as computed today, not yet relative to the turntable profile |
| 2 | planned | the same features relative to the measured turntable profile (the profile as zero point) |

Feature version 1 makes it possible to build and test the whole exchange now. Weights from very different signal
chains are only partly comparable in it; that is what version 2 is for. Its fixed scaling is provisional and is
replaced by statistics of real stations before a server accepts the first real contributions.

## Kinds and coefficients

A model has one logistic regression per kind of event: `click`, `tick` (crackle) and `pop`. For one event with
features `x`, the probability that it is a real defect and not music is

```
z[i] = (x[i] - center[i]) / scale[i]
p    = 1 / (1 + exp(-(coef[0] + coef[1] * z[0] + ... + coef[10] * z[9])))
```

`center` and `scale` are the **fixed shared scaling** from `features.json`, the same on every station. A station
that standardises with its own mean and spread converts its model exactly, without retraining. With its own model
`(mu, sd, b)`, where `p = sigmoid(b[0] + sum(b[i + 1] * (x[i] - mu[i]) / sd[i]))`:

```
coef[i + 1] = b[i + 1] * scale[i] / sd[i]
coef[0]     = b[0] + sum(b[i + 1] / sd[i] * (center[i] - mu[i]))
```

Both forms give the same probability for every event. The shared form is what makes weights of different stations
comparable, and it is the form in which a station uses a global model as the prior of its own training.

## What a contribution contains

Per kind: the coefficients, the number of examples (real and false alarm, and split into manual judgements and
master classifications), the number of recordings with a master the model was cross-checked on, and the result of
that cross-check (mean absolute relative error of the estimated and of the raw count per recording, optionally the
AUC). Per profile: the coarse chain class, the feature, detector and client versions.

A kind is only sent when the station has fitted a model for it and cross-checked it. Besides the schema, a server
checks that `n_manual + n_master` equals `n_real + n_false`, that all numbers are finite and that the body is at
most 16 KiB.

**Never contained:** audio, features of single events, record titles, notes, Discogs data, names, places, hardware
serial numbers, credentials. The profile ID in the URL is chosen at random by the station.

## Chain classes

`chain.source` is `usb_turntable` or `turntable_interface`, `chain.cartridge` is `mm`, `mc` or `unknown`. A global
model is built for `all` profiles and, once a class has enough profiles of its own, for the class
`<source>.<cartridge>` (for example `turntable_interface.mc`). A station uses the model of its class when there is
one and the `all` model otherwise.
