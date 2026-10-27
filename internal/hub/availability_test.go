package hub

import (
	"testing"

	"github.com/michaelpietzsch/vinyl-mind/internal/config"
	"github.com/michaelpietzsch/vinyl-mind/spec"
)

// What the majority rule costs honest anchors that merely differ: how often a kind gets no model while nothing is
// published, how often an honest anchor is found far or demoted. The bounds hold at the heterogeneity the
// simulation assumes (0.15) with room to 0.30; the real spread of stations is not known yet, and the floor of
// the band (consistency_floor) is the setting to widen if it turns out larger.
func TestMajorityAvailability(t *testing.T) {
	if testing.Short() {
		t.Skip("takes a while; the CI runs it in the job for the aggregation bounds")
	}
	cfg := config.Default().Aggregation
	published := append([]float64(nil), truth...)
	const trials = 400
	type limits struct{ noModel, demoted, far float64 } // in percent
	for _, n := range []int{3, 5, 7, 9} {
		for _, sd := range []float64{0.15, 0.25, 0.30} {
			for _, mode := range []string{"honest only", "minority +0.8 on coef1", "minority +50"} {
				b := (n - 1) / 2
				if mode == "honest only" {
					b = 0
				}
				r := rng(uint64(n*1000) + uint64(sd*100) + uint64(len(mode)))
				noModel, honestDemoted, honestFar := 0, 0, 0
				for i := 0; i < trials; i++ {
					var in []Input
					for a := 1; a <= n-b; a++ {
						in = append(in, Input{StationID: int64(a), ProfileID: "p", Trust: 1, Anchor: true, Kind: honestKind(r, sd)})
					}
					for j := 0; j < b; j++ {
						var k spec.KindContribution
						if mode == "minority +50" {
							k = attackKind(constCoef(50))
						} else {
							k = attackKind(truth)
							k.Coef[1] += 0.8
						}
						in = append(in, Input{StationID: int64(90 + j), ProfileID: "p", Trust: 1, Anchor: true, Kind: k})
					}
					for s := 0; s < 3; s++ {
						in = append(in, Input{StationID: int64(10 + s), ProfileID: "p", Trust: 0.9, Kind: honestKind(r, sd)})
					}
					if Combine(in, cfg, 11).Coef == nil {
						noModel++
					}
					for _, u := range CombineWith(in, cfg, 11, published).Used {
						if u.StationID < 10 {
							if u.Demoted {
								honestDemoted++
							}
							if u.Far {
								honestFar++
							}
						} else if u.StationID >= 90 && mode == "minority +50" && !u.Demoted {
							t.Errorf("N=%d sd %.2f: a far minority anchor not demoted", n, sd)
						}
					}
				}
				noModelPct := 100 * float64(noModel) / trials
				demotedPct := 100 * float64(honestDemoted) / float64(trials*(n-b))
				farPct := 100 * float64(honestFar) / float64(trials*(n-b))
				// At 0.15 nothing happens; at 0.30 an honest anchor is found far now and then, and the two anchors
				// that remain after a far minority of three is demoted dispute a coefficient in a sixth of the runs
				lim := limits{0, 0, 0}
				switch {
				case sd >= 0.30 && mode == "minority +50":
					lim = limits{20, 1, 20} // the honest anchors that remain are few, and spread
				case sd >= 0.30:
					lim = limits{1.5, 6, 8}
				case sd >= 0.25:
					lim = limits{2, 1, 7}
				}
				if noModelPct > lim.noModel || demotedPct > lim.demoted || farPct > lim.far {
					t.Errorf("N=%d sd %.2f %s: no model %.1f %% (limit %g), honest demoted %.1f %% (limit %g), honest "+
						"far %.1f %% (limit %g)", n, sd, mode, noModelPct, lim.noModel, demotedPct, lim.demoted, farPct, lim.far)
				}
			}
		}
	}
}
