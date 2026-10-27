package hub

import (
	"testing"

	"github.com/michaelpietzsch/vinyl-mind/internal/config"
)

// The bounds of the anchor rules, over every count of anchors and of turned anchors among them that the review
// looked at. What they promise: fewer than half of the anchors turned move a coefficient by less than the band
// when they stay inside it, and by nothing worth the name when they are far (they are demoted, the honest ones
// never are); half of the anchors turned form no model unless they stay inside the band, where nothing can tell
// them from honest spread; and a majority of them wins, since nothing can tell a majority of the operator's own
// stations from the truth.
const (
	farShift       = 0.05 // the most a far minority may move a coefficient: it is demoted, the newcomers' fifth remains
	farShiftMedian = 0.15 // the same with the median, whose steps between the stations' values are that wide
	insideShift    = 1.0  // the most a minority inside the band may move a coefficient: the band, with noise
)

func TestAnchorMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("takes a while; the CI runs it in the job for the aggregation bounds")
	}
	targeted := append([]float64(nil), truth...)
	targeted[1] = 50
	attacks := []struct {
		name   string
		coef   []float64
		inside bool // within the band of the honest position: not far, not demoted, and allowed its share
	}{
		{"+50", constCoef(50), false}, {"reversed", scaledTruth(-3), false},
		{"truth+0.7", shiftedTruth(0.7), true}, {"truth+0.76", shiftedTruth(0.76), true},
		{"coef1=+50", targeted, true},
	}
	cases := [][2]int{{2, 1}, {3, 1}, {3, 2}, {4, 1}, {4, 2}, {5, 1}, {5, 2}, {5, 3}, {6, 2}, {6, 3}, {7, 3}, {7, 4},
		{8, 1}, {8, 3}, {8, 4}, {12, 5}}
	for _, method := range []string{"trimmed_mean", "median"} {
		cfg := config.Default().Aggregation
		cfg.Method = method
		for _, atk := range attacks {
			for _, c := range cases {
				n, b := c[0], c[1]
				good := n - b
				base := []group{honestGroup("anchor", good, 1, 1, true, honestSet(good, 1, 1, 0.15)),
					honestGroup("honest", 6, 1, cfg.TrustAutoMax, false, honestSet(6, 1, 2, 0.15))}
				ref := combineGroups(cfg, nil, base...)
				out := combineGroups(cfg, nil, append(base, attackGroup("bad", b, 1, 1, true, atk.coef))...)
				name := method + " " + atk.name
				tie := 2*b == n
				switch {
				case tie && (!atk.inside || atk.name == "coef1=+50"):
					// Half of the anchors turned and far, on all coefficients or on one: no majority there, no
					// reference, and nothing published to keep to
					if out.Coef != nil {
						t.Errorf("%s N=%d B=%d: half of the anchors turned, a model was built: %v", name, n, b, out.Coef)
					}
					continue
				case 2*b > n:
					continue // a majority of the operator's stations is the operator's word, nothing stands against it
				case tie && out.Coef == nil:
					continue // two anchors a hair apart on one coefficient: no model either, see the pair rule
				}
				if out.Coef == nil || ref.Coef == nil {
					t.Errorf("%s N=%d B=%d: no model: %q / %q", name, n, b, out.Reason, ref.Reason)
					continue
				}
				d, at := maxShift(out.Coef, ref.Coef)
				if out.far["anchor"] != 0 || out.demoted["anchor"] != 0 {
					t.Errorf("%s N=%d B=%d: honest anchors far %d demoted %d", name, n, b, out.far["anchor"],
						out.demoted["anchor"])
				}
				limit := farShift
				if method == "median" {
					limit = farShiftMedian
				}
				if atk.inside {
					limit = insideShift // half of them inside the band shift it most, and still less than the band
				} else if out.demoted["bad"] != b {
					t.Errorf("%s N=%d B=%d: %d of %d far anchors demoted", name, n, b, out.demoted["bad"], b)
				}
				if d > limit {
					t.Errorf("%s N=%d B=%d: coefficient %d moved by %.3f, limit %.2f", name, n, b, at, d, limit)
				}
			}
		}
	}
}

// Colluders at a tuned offset: just beyond the band, where a minority seen from an honest anchor's point of view
// could pass for the majority if the majority were judged among the others. Beyond the band they are far and
// demoted whatever the offset; at the band's edge they are inside, which the bound for an inside minority covers.
func TestTunedOffsetColluders(t *testing.T) {
	if testing.Short() {
		t.Skip("takes a while; the CI runs it in the job for the aggregation bounds")
	}
	cfg := config.Default().Aggregation
	published := append([]float64(nil), truth...)
	for _, c := range []struct{ n, b int }{{5, 1}, {5, 2}, {7, 2}, {7, 3}, {9, 4}, {11, 5}} {
		for _, off := range []float64{0.8, 1.0, 1.2, 1.4, 1.5, 1.6, 2.0, 5} {
			for seed := uint64(1); seed <= 20; seed++ {
				good := c.n - c.b
				base := []group{honestGroup("anchor", good, 1, 1, true, honestSet(good, 1, seed, 0.15)),
					honestGroup("honest", 6, 1, 0.9, false, honestSet(6, 1, seed+20, 0.15))}
				ref := combineGroups(cfg, published, base...)
				out := combineGroups(cfg, published, append(base, attackGroup("bad", c.b, 1, 1, true, shiftedTruth(off)))...)
				if out.Coef == nil || ref.Coef == nil {
					t.Fatalf("N=%d B=%d offset %.1f seed %d: no model: %q / %q", c.n, c.b, off, seed, out.Reason, ref.Reason)
				}
				if out.far["anchor"] != 0 || out.demoted["anchor"] != 0 {
					t.Errorf("N=%d B=%d offset %.1f seed %d: honest anchors far %d demoted %d", c.n, c.b, off, seed,
						out.far["anchor"], out.demoted["anchor"])
				}
				d, at := maxShift(out.Coef, ref.Coef)
				limit := insideShift
				if off >= 1 {
					limit = farShift
					if out.demoted["bad"] != c.b {
						t.Errorf("N=%d B=%d offset %.1f seed %d: %d of %d colluders demoted", c.n, c.b, off, seed,
							out.demoted["bad"], c.b)
					}
				}
				if d > limit {
					t.Errorf("N=%d B=%d offset %.1f seed %d: coefficient %d moved by %.3f, limit %.2f", c.n, c.b, off,
						seed, at, d, limit)
				}
			}
		}
	}
}
