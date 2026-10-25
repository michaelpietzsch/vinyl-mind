# How Vinyl-Mind combines

Stations train small models locally and send their weights. Vinyl-Mind combines them into a global model, signs
it and publishes it once the stations that tried it found it no worse than their own. This page describes the
method and why it is built this way. The code is in `internal/hub`; all parameters are in the `[aggregation]`
and `[publication]` sections of the configuration (`config.example.toml`).

## Groups

Weights are only comparable when they mean the same thing, so contributions are combined only within a **group**:

- the same **feature version** (which features, computed how, in which shared scaling, see `spec/`),
- the same **detector version** (a changed detector changes what the features measure),
- per **kind** of event (`click`, `tick`, `pop`), each with its own logistic regression.

Within a group there is always a model over **all chain classes**. A chain class (`usb_turntable.mm`,
`turntable_interface.mc`, …) gets a model of its own once it has `min_class_profiles` (5) profiles; a station uses
the model of its class when there is one and the model over all classes otherwise.

## Which contributions count

A kind of a contribution is left out when

- its self-check error (`cv_err_est`, the mean relative error of the estimated count per recording in the
  station's cross-check) is above `max_est_error` (0.30, the station's own limit), or
- it does not beat the raw count (`cv_err_est >= cv_err_raw`): the station itself would not use such a model.

A kind is only combined with at least `min_profiles` (3) usable profiles from at least `min_stations` (2)
stations, so that one station with several turntables cannot make a model on its own.

## Weights

Each usable profile gets

```
evidence = min(max_evidence, manual_weight × judgements by hand + master classifications)
                                          (manual_weight = 5 as on the station, max_evidence = 5000)
quality  = 1 − cv_err_est / max_est_error (1 = perfect self-check, 0 = at the limit)
weight   = evidence × quality
```

The quality factor falls to zero at the limit, so there is no jump between a profile that just counts and one
that is just left out. The counts are what a station claims, not what anyone can check, so beyond `max_evidence`
more claims buy no more weight.

### Limited influence per station

The weights of a station's profiles are added up, and each station's **share** of the total is capped:

```
cap = max_station_share × trust        (max_station_share = 0.25)
```

Shares follow the weights; what a capped station would get beyond its cap goes to the others in proportion
(water-filling). If the caps together are below 1, which happens with few stations or many new ones, every station
is at its cap and the shares follow the caps, that is, the trust. Within a station, its profiles share its part
by weight.

Tying the cap to trust keeps a single new station small: it starts at `trust_start` (0.25) and so has at most
6.25 % of the mean, however many examples it claims. A fixed cap alone would not be enough: two fake stations at
25 % each would hold half the mean, more than any trimming removes.

### All new stations together

Capping each new station is not enough either when stations are cheap to make, for example with sign-up on a
website. Twenty new stations at 6.25 % each would together outweigh everyone else. So **all stations below
`newcomer_trust` (full trust, 1.0) together get at most `newcomer_share` (20 %)** of the mean; the relations within
each group stay as they were. One-sided 20 % is exactly what the trimmed mean cuts off.

While every station is new, at the very start, there is no established group to protect, and the group cap does
nothing. The operator therefore marks the stations he knows as trusted (`vinyl-mind trust <fingerprint> 1`).
Trust earned by consistency alone is cheap to fake (a station can send back the published model), which is why
only full trust leaves the new group, and why trust grows by at most one step a day.

### Trust

- A station whose contribution lies **far from the combination** loses `trust_penalty` (0.15) at once, down to
  `trust_min` (0.05). Far means: the median over its coefficients of the distance to the combined coefficient,
  measured in spreads (with a floor of 0.1), is above `consistency_k` (3).
- A station whose contributions are consistent, or whose check reports agree with the decision on a model, gains
  `trust_step` (0.1), **at most once per day**, up to 1. Sending often does not earn trust faster.
- Trust is judged on the model over all chain classes only, so that one contribution is not counted twice.

## Combining

Per coefficient, over the usable profiles with their final shares:

- **`trimmed_mean`** (default): the weighted mean after cutting off `trim` (20 %) of the weight at each end.
  Weights at the boundary are cut partially, so the result moves smoothly when a weight changes.
- **`median`**: the weighted median. More robust, less precise with few profiles.

With each coefficient comes its **spread**, the weighted interquartile range of the contributions. It travels
with the model (stations can see how much the profiles agree) and is the yardstick for "far" above.

A model is a **candidate** when it is built. A new candidate is only built when the contributions of the group or
the aggregation settings changed since the last model (the inputs hash); an older candidate of the same group is
then superseded. The background job combines every `combine_interval_s` (1 hour); `vinyl-mind combine` does it at
once (`-dry-run` shows the result without storing anything). Before a model is stored, it is validated against
`spec/v1/global-model.schema.json` like a station would, and signed with the server key.

## Publishing and withdrawing

With weights only, the server has no data to test a model on. The stations test it: each station that uses
global models evaluates a candidate on its own recordings with a master and reports the estimate error of its
local model and of the global one per kind (`PUT /v1/profiles/{id}/checks/{version}`).

A report's **verdict** is *worse* when the global model's error, averaged over the reported kinds, exceeds the
local model's by more than `tolerance` (0.01); otherwise *not worse*. Only reports of active stations count, one
per profile and model.

**Each station has one vote**: the majority of its profiles' verdicts, and no vote on a tie. Counting profiles
instead would let a station that invents profiles decide alone; profiles that only report also count against the
station's profile limit (`max_profiles_per_station`). **A vote weighs as much as the station's trust**, and only
stations that joined at least `min_vote_age_days` (7) days ago vote: many new stations cannot decide about a model
that established ones have checked.

| Situation | Rule (with `auto = true`) |
|---|---|
| reports from at least `min_reports` (3) profiles of at least `min_report_stations` (2) voting stations, and the trust of the stations finding a candidate not worse outweighs the trust of those finding it worse | published |
| the same number of reports, and the trust of the stations finding a candidate or a published model worse outweighs the others | withdrawn |
| equal weight on both sides, or not enough reports yet | wait for the next report |

The newest published model of a group is the one stations get; when it is withdrawn, the previous published one
is current again. The operator can publish or withdraw by hand (`vinyl-mind publish`, `vinyl-mind withdraw`), which
matters at the start, when there are not yet enough stations to report.

A station adopts a published model only if it is not worse on its own master recordings either (principle 4 of
the concept). Together with the limited influence above, a poisoned model cannot spread silently: it would have to
pass the robust combination and then the checks of most stations.

## What the simulation shows

`go run ./tools/simulate` simulates stations as vinyl-check works today: the station's training is rebuilt in Go
(ridge-penalised logistic regression, Newton's method, judgements by hand five times, cross-check by leaving out
one recording with a master), each chain shifts the features differently, and each record has its own mix of
music, crackle and loud passages. Eight honest stations with six recordings of 300 detections each contribute;
two new stations send a reversed model claiming ten million examples and a perfect self-check. A new profile with
only 2 × 40 judged detections is then measured on 20 recordings it has not seen. A second run has twenty attackers
instead of two.

Over 20 random seeds (median):

| | |
|---|---|
| distance of the combination to the truth, honest stations only | 0.052 |
| the same with the two attackers | 0.061 |
| the same with twenty attackers, as automatic sign-up could make them | 0.074 |
| the same with a plain weighted mean (no caps, no trimming, no self-check filter) | 2.40 |
| new profile, raw count | 190 % off |
| new profile, its own model only | 25 % off |
| new profile, global model as it is | 11 % off |
| new profile, own model with the global model as prior | 10 % off, better than its own model in 20 of 20 seeds |
| new profile, own model with the plain mean as prior | 142 % off |

Two findings from the simulation shape the station side (`docs/station-side.md`):

- The prior must pull the **intercept** towards the global model as well, not only the weights. Left free, the
  intercept adapts to the few records the new profile has seen and gives most of the benefit away.
- A prior strength of about 100 (the ridge penalty towards the global coefficients, in the shared scaling) worked
  best for small profiles. The station's own check on its master recordings decides in the end.

The simulation covers the method, not the real features: with feature version 1 the features are not yet
relative to the turntable profile, and how well weights of real, different chains combine can only be measured
with real stations. The tests in `internal/sim` keep the two claims above true for six seeds.
