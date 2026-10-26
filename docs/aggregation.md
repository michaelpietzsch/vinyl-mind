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

A station's claim is bounded across all its profiles as well: together they count at most twice `max_evidence`,
so sixteen profiles do not buy sixteen times the weight of one.

### The operator's anchors

Everything in a contribution is the station's own word. The one thing the hub can rely on is the operator's word
about stations he knows: **anchors** are stations whose trust the operator set to the full level
(`vinyl-mind trust <fingerprint> 1`). Automatic trust never gets there (`trust_auto_max`, 0.9), so no station can
become an anchor by behaving well for a while. The anchors are the reference for everything:

- **No anchor, no model.** A kind is combined only with at least one anchor among its usable profiles, in every
  group, every chain class and (below) every set of profiles a weight is combined from. A group of newcomers alone
  cannot make a model, not even for a chain class of their own.
- **Shares.** The anchors share `1 − newcomer_share` (80 %) among themselves by their weight, each capped at
  `max_station_share` (0.25) of that part (water-filling: what a capped station would get beyond its cap goes to
  the others; if the caps together are below the part, every anchor is at its cap). **All other stations share
  `newcomer_share` (20 %) in equal parts**, whatever they claim and however many they are: neither many stations
  nor big claims buy more than an equal part of a fifth. Within a station, its profiles share its part by weight.
- **The band.** Per coefficient, the reference is the median over the anchors behind the majority position (next
  bullet; see *informative* below for who learned a coefficient), and the band around it is `consistency_k` (3)
  robust spreads wide; with fewer than three anchors there is no spread to measure and `consistency_floor` (0.25)
  stands in, with more it is their median absolute deviation, which a minority of them cannot inflate. **Every
  other station's coefficients are pulled into the band** before anything is combined (winsorising). What a
  station claims beyond the band is never averaged in: the influence of one contribution is bounded by the band,
  not by the ±50 of the specification. A station outside the band on two or more of the coefficients it learned
  is **far** from the reference.
- **A majority of all anchors, or no reference.** Per coefficient, the anchors that learned it are grouped: the
  largest group of them within the floor band of one position (two: within √2 bands of each other) stands
  behind it. Only when they are **more than half of all anchors** is that the reference. The majority is always one of
  all anchors, also when an anchor is held to the others: it is left out of the centre it is held to, never of
  the question who the majority is, since a minority just beyond the band would otherwise pass for the majority
  in every honest anchor's view. A minority defines nothing: not two anchors with a second opinion (`gc`,
  `gc_missing`) among many, not colluders who are the majority among the few that learned a coefficient, not one
  of two equal camps. Such a coefficient has **no reference**: it is kept at the published model, or where the
  anchors that did not learn it left it (the prior), nobody is held on it and nobody is far on it, and the
  operator is told (`anchors_disagree`; `vinyl-mind combine -dry-run` names the coefficients). With neither to
  keep it at, there is no model for that kind. An anchor that stands behind the majority position is held to
  nothing on that coefficient and is never far on it: it agrees. The price of the rule is that a second opinion
  is learned only once most anchors have one; that is the honest state of the threat model.
- **Anchors among themselves.** An anchor is held too: to the median of all anchors while they are few (a median
  of three stands one bad anchor), and to the other anchors alone once at least three of them learned the
  coefficient. An anchor that is far from the other anchors **on the coefficients every anchor learned** is **no
  anchor in that combination**: it is held like a newcomer, and a coefficient only it learned stays the prior
  instead of its word. Coefficients only some anchors learned say nothing about who is far: among the few that
  hold a second opinion, a pair of colluders would be the majority and the honest anchors beside them the ones
  found far. Only other anchors can bring a demotion about, never newcomers; nobody is demoted when the anchors
  found far are half of all anchors or more (a tie the rest cannot judge); and it changes no trust: the operator
  is told (`anchor_demoted`, or `anchor_far` for an anchor held but not demoted, once a day) and decides.

What this means in numbers: with one anchor the model *is* the anchor's model, since the trimmed mean cuts off the
others' fifth entirely; from two anchors on, the others move a coefficient by at most a third of the distance
between the anchors' positions, and never beyond the band. The global model stays close to the anchors' model, and
the others add at most a fifth. That is the point. Trust earned by consistency alone is cheap to fake (a station
can send back the published model), and the alternative, letting earned trust define the reference, lets whoever
earns it first define it for everyone. Trust below the anchor level is therefore shown, logged and earned, but
weighs nothing in the combination.

### Trust

Trust that the operator gave never changes by itself: an anchor far from the other anchors is reported in the
log and the event log (`anchor_demoted`, `anchor_far`), and its word decides nothing about publication until a
combination finds it in line again (below), but its trust stays, so that no group of stations can push the
reference aside. Lowering it is the operator's call. Automatic trust:

- falls by `trust_penalty` (0.15) for a contribution far from the reference, **at most once a day and only for a
  contribution that changed** since the last penalty, down to `trust_min` (0.05);
- rises by `trust_step` (0.1) for a consistent contribution, or for a check report that agrees with the decision
  on a model (for stations that contribute), **at most once a day**, up to `trust_auto_max` (0.9), never to the
  full level: that stays the operator's to give;
- is judged on the model over all chain classes only, so that one contribution is not counted twice, and only
  when that model has an anchor;
- is kept for a station that leaves: a key or account that comes back within 30 days starts with the lower of
  `trust_start` and the trust it had, and the key with its age; a key may come back five times a day.

Every change is logged with its reason (`vinyl-mind events`).

## Combining

Per coefficient, over the usable profiles with their final shares:

- **`trimmed_mean`** (default): the weighted mean after cutting off `trim` (20 %) of the weight at each end.
  Weights at the boundary are cut partially, so the result moves smoothly when a weight changes.
- **`median`**: the weighted median. More robust, less precise with few profiles.

With each coefficient comes its **spread**, the weighted interquartile range of the contributions. It travels
with the model (stations can see how much the profiles agree) and is the yardstick for "far" above.

A weight is combined only from the profiles that **learned it**. Where a feature never varied in a profile's
examples (most stations have no second opinion, so `gc` and `gc_missing` are constant there), the station sends the
prior's weight and marks the feature as not `informative` (`spec/README.md`). Averaging those in would drag the
weight back towards the prior, or towards 0 at the start, however clearly the other stations see it. The shares are
worked out again among the profiles that learned the weight, with the same caps per station and for new stations.
If they are fewer than `min_profiles` or come from fewer than `min_stations` stations, the weight is combined from
all profiles, which for the others is the prior again: it moves little until enough stations see it. The intercept
is always learned.

A model is a **candidate** when it is built. A new candidate is only built when the combination itself changed
since the newest model of the group, whatever its status (a hash over the rounded coefficients, spreads and
counts, with the aggregation settings), so a changed contribution that changes nothing, because it was left out
or pulled to the same place, builds nothing, and a candidate the anchors withdrew is not built again from the same
inputs. A candidate is **under review** for `review_hours` (48): it is not replaced before that, and a new
combination that differs from it, or from the published model when no candidate is open, by at most 0.02 on every
coefficient builds nothing either, so that the reports already given keep their meaning and nobody pushes a
candidate out by nudging a contribution every hour. The background job
combines every `combine_interval_s` (1 hour); `vinyl-mind combine` does it at once (`-dry-run` shows the result
without storing anything). Before a model is stored, it is validated against `spec/v1/global-model.schema.json`
like a station would, and signed with the server key. The model records which stations and accounts are behind it.

## Publishing and withdrawing

With weights only, the server has no data to test a model on. The stations test it: each station that uses
global models evaluates a candidate on its own recordings with a master and reports the estimate error of its
local model and of the global one per kind (`PUT /v1/profiles/{id}/checks/{version}`).

A report's **verdict** is *worse* when the global model's error, averaged over the reported kinds, exceeds the
local model's by more than `tolerance` (0.01); otherwise *not worse*. Only reports of active stations count, one
per profile and model.

**Each station has one vote**: the majority of its profiles' verdicts, and no vote on a tie. Counting profiles
instead would let a station that invents profiles decide alone; profiles that only report also count against the
station's profile limit (`max_profiles_per_station`). Only stations that joined at least `min_vote_age_days` (7)
days ago vote. **What decides is the anchors' word**: the votes of all stations are counted and shown, but a
report is only a claim, and claims from stations nobody vouches for can neither publish a model nor take one away.
**A station whose contribution is in the model, or that shares an account with one, cannot vote for it**; its
good word is not even a report. It can vote against it. **An anchor that the last combination that used it found
far from the other anchors** (`anchor_demoted`, `anchor_far`) **decides nothing**: its vote is counted like any
other station's, and until a combination finds it in line again it can neither publish a model alone nor take one
away. The doubt is recorded with the station (`vinyl-mind stations`, *far since*; `status`), so it outlives a
restart, and `vinyl-mind trust <fingerprint> …` lifts it: the operator has looked.

| Situation | Rule (with `auto = true`) |
|---|---|
| the candidate is at least `review_hours` (48) old, reports from at least `min_reports` (3) profiles of at least `min_report_stations` (2) voting stations are in, and the anchors finding it not worse outweigh the anchors finding it worse | published |
| the same number of reports, and the anchors finding a candidate or a published model worse outweigh the anchors finding it not worse | withdrawn |
| equal weight, no anchor has spoken, or not enough reports yet | wait for the next report |

The review window gives every station that wants to a chance to check a candidate before it is published; the
first three reports do not decide. A candidate that has reached the age is decided at the next background run,
not only when a report arrives. **This needs an anchor that can vote**: one that did not contribute to the
candidate and shares no account with one that did. An operator whose every anchor contributes gets candidates
but no automatic publication, not only at the start but for good; the hub says so at start, in the log and the
event log when a group's state changes (`group_no_voter`, likewise `group_no_anchor`, `group_anchors_disagree`,
`group_too_few`, and `group_model` when it forms a model again), each state at most once a day per group, however
often a state at a threshold flaps, a state that is not a model once a day as long as it lasts, and always in
`vinyl-mind status`. The way out is a station of the operator's that only checks: *Contribute* off, *Use the
global model* on, trusted with `trust … 1`.

**A withdrawal does not bring an older model back**: the published model is the one published last, as long as it
is still published; after a withdrawal stations fall back to their local models until the operator publishes
something (`vinyl-mind publish`). Every answer about a model carries the hub's signed statement of its status
(`state_jws`, with a `seq` that counts the model's status changes), so that a status cannot be changed on the way
without the server key.

`publication.auto = false` turns the automatic part off; then only `vinyl-mind publish` and `withdraw` change a
model's status.

The operator can publish or withdraw by hand (`vinyl-mind publish`, `vinyl-mind withdraw`), which matters at the
start, when there are not yet enough stations to report, and after a withdrawal.

A station adopts a published model only if it is not worse on its own master recordings either (principle 4 of
the concept). Together with the band around the anchors, a poisoned model cannot spread silently: it would have
to pass the anchors' band, the anchors' own checks over two days, and then every station's check of its own.

## What the simulation shows

`go run ./tools/simulate` simulates stations as vinyl-check works today: the station's training is rebuilt in Go
(ridge-penalised logistic regression, Newton's method, judgements by hand five times, cross-check by leaving out
one recording with a master), each chain shifts the features differently, and each record has its own mix of
music, crackle and loud passages. Eight honest stations with six recordings of 300 detections each contribute;
half of them have no second opinion, the others miss it for 5 % of the events; two of the eight are the operator's
anchors, the other six have earned the automatic maximum (0.9). Two attackers that also earned 0.9 send a reversed
model claiming ten million examples and a perfect self-check. A new profile without a second opinion and with only
2 × 40 judged detections is then measured on 20 recordings it has not seen. Further runs have twenty attackers
instead of two, and two fresh ones at trust 0.25.

Over 20 random seeds (median):

| | |
|---|---|
| distance of the combination to the truth, honest stations only | 0.203 |
| the same with the two attackers (trust 0.9) | 0.206 |
| the same with twenty attackers | 0.212 |
| the same with two fresh attackers (trust 0.25) | 0.206 |
| the same with a plain weighted mean (no anchors, no band, no caps) | 2.40 |
| new profile, raw count | 216 % off |
| new profile, its own model only | 25 % off |
| new profile, global model as it is | 12 % off |
| new profile, own model with the global model as prior | 12 % off, better than its own model in 19 of 20 seeds |
| new profile, own model with the plain mean as prior | 146 % off |

The attackers no longer move the model: held in the band and limited to an equal part of a fifth, two or twenty of
them, patient or fresh, cost between 0.003 and 0.009. What the model costs is the anchors: it is built around them,
and two stations' models are two stations' models. With more anchors it gets closer to the truth, and the attackers
still get nowhere. One thing more anchors cannot buy on their own is a weight that only half of them learned: in
the simulation half of the stations have no second opinion (`-no-gc 0.5`), so with all eight as anchors the `gc`
weights stay at the prior, and the model gets no closer than with four; with six of the eight having a second
opinion (`-no-gc 0.25`) it does:

| anchors among the eight honest stations | honest only | with two attackers | new profile with the prior |
|---|---|---|---|
| 1 | 0.243 | 0.243 | 16 % off, better in 16 of 20 seeds |
| 2 (the run above) | 0.203 | 0.206 | 12 % off, better in 19 of 20 seeds |
| 4 | 0.176 | 0.177 | 11 % off, better in 19 of 20 seeds |
| 8, four of them with a second opinion | 0.176 | 0.196 | 13 % off, better in 18 of 20 seeds |
| 8, six of them with a second opinion | 0.069 | 0.082 | 11 % off, better in 18 of 20 seeds |

That is the operator's lever: every honest station he marks as an anchor improves the model for everyone, and
nothing a station does on its own can take an anchor's place. Where the prior did not help, the profile's own model
happened to be good already; the station's check on its own recordings is there to catch exactly that.

An anchor that turns (`-bad-anchors`) is the worst case, since it is the operator's own word that fails. Held to
the other anchors, and no anchor in the combination once it is far from them on the coefficients everyone
learned, it changes little, as long as the turned ones are fewer than half of all anchors (medians over twenty
seeds, with the two attackers; "honest" is the same seeds without the turned anchors and the attackers; the
turned anchors send the attackers' model, second opinion included):

| honest anchors | turned anchors | distance to the truth | honest |
|---|---|---|---|
| 2 | 1 | 0.207 | 0.203 |
| 3 | 1 | 0.191 | 0.189 |
| 4 | 1 | 0.179 | 0.176 |
| 4 | 2 | 0.181 | 0.176 |
| 5 | 2 | 0.188 | 0.180 |
| 6 | 3 | 0.186 | 0.173 |
| 7 | 3 | 0.188 | 0.177 |
| 8 | 1, 3 or 4 | 0.196 | 0.176 |
| 12 (of 16 stations) | 4 | 0.169 | 0.162 |
| 13 (of 16 stations) | 4 | 0.171 | 0.162 |
| 5, four with a second opinion | 2 | 0.070 | 0.071 |
| 6, five with a second opinion | 3 | 0.077 | 0.068 |
| 8, six with a second opinion | 3 | 0.082 | 0.069 |

A single turned anchor among two honest ones is found far from the other two and demoted; two honest anchors with
no third are a reference only where they agree, and a coefficient they dispute is kept at the published model.
Turned anchors that learned a second opinion define it neither when honest anchors with one are few (they are no
majority of all anchors, the weight stays the prior) nor when they are many (the honest majority stands behind
its position, the turned ones are outside it).

The scenario tests in `internal/hub` (`TestAnchorMatrix`, `TestTunedOffsetColluders`, `TestMajorityAvailability`)
and the sweeps in `internal/sim` hold the rules to these bounds, over every count of anchors and turned anchors
the review looked at, and run in the CI: a minority of the anchors that is far from the rest, at any offset
beyond the band, is demoted in every case and moves no coefficient by more than 0.05 (0.15 with the median, whose
steps between the stations' values are that wide); a minority inside the band is an agreeing minority and moves a
coefficient by less than the band plus noise (0.9), as does half of the anchors inside the band, which nothing
can tell from honest spread; half of the anchors far from the rest form no model; honest anchors are never found
far or demoted at the heterogeneity the simulation assumes (0.15), and at twice that in a few per cent of the
combinations.

Two findings from the simulation shape the station side (`docs/station-side.md`):

- The prior must pull the **intercept** towards the global model as well, not only the weights. Left free, the
  intercept adapts to the few records the new profile has seen and gives most of the benefit away.
- A prior strength of about 100 (the ridge penalty towards the global coefficients, in the shared scaling) worked
  best for small profiles. The station's own check on its master recordings decides in the end.

The simulation covers the method, not the real features: with feature version 1 the features are not yet
relative to the turntable profile, and how well weights of real, different chains combine can only be measured
with real stations. The tests in `internal/sim` keep the two claims above true for six seeds.
