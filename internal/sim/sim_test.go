package sim

import (
	"strings"
	"testing"
)

// The simulation backs the two claims of docs/aggregation.md, over several seeds so that one lucky draw does not
// carry them.
func TestCombiningHelpsAndResistsAttackers(t *testing.T) {
	var local, prior float64
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	for _, seed := range seeds {
		p := Defaults()
		p.Seed = seed
		r, err := Run(p)
		if err != nil {
			t.Fatal(err)
		}
		if r.DistAttacked > 1.5*r.DistHonest+0.03 {
			t.Errorf("seed %d: two attackers moved the combination from %.3f to %.3f", seed, r.DistHonest,
				r.DistAttacked)
		}
		if r.DistNaive < 10*r.DistAttacked {
			t.Errorf("seed %d: the plain mean (%.3f) should be far worse than Vinyl-Mind (%.3f)", seed,
				r.DistNaive, r.DistAttacked)
		}
		if r.ErrPrior > r.ErrRaw {
			t.Errorf("seed %d: with the prior worse than counting everything", seed)
		}
		local += r.ErrLocal
		prior += r.ErrPrior
		attackers := 0.0
		for _, u := range r.Used {
			if strings.HasPrefix(u.ProfileID, "attacker") {
				attackers += u.Share
				if !u.Far {
					t.Errorf("seed %d: attacker %s not far", seed, u.ProfileID)
				}
			}
		}
		if attackers > 0.2+1e-9 {
			t.Errorf("seed %d: attackers together hold %.3f", seed, attackers)
		}
	}
	if prior > 0.7*local {
		t.Errorf("a new profile should estimate clearly better with the global prior: %.3f vs %.3f",
			prior/float64(len(seeds)), local/float64(len(seeds)))
	}
}

func TestSharedScalingAbsorbsChainOffsets(t *testing.T) {
	// Without attackers and with strong chain offsets, the combination still finds the truth: each station
	// standardises with its own mean and spread, and the conversion into the shared scaling undoes that exactly
	p := Defaults()
	p.Attackers, p.ChainShift = 0, 1.5
	p.Anchors = p.Stations // every station the operator's: this is about the conversion, not about trust
	r, err := Run(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.DistHonest > 0.12 {
		t.Errorf("distance to the truth %.3f with strong chain offsets", r.DistHonest)
	}
}

func TestManyCheapStationsDoNotTakeOver(t *testing.T) {
	// What automatic sign-up makes cheap: forty new stations, each claiming ten million examples
	for _, seed := range []uint64{1, 2, 3} {
		p := Defaults()
		p.Seed, p.Attackers = seed, 40
		r, err := Run(p)
		if err != nil {
			t.Fatal(err)
		}
		if r.DistAttacked > 1.5*r.DistHonest+0.05 {
			t.Errorf("seed %d: forty new stations moved the combination from %.3f to %.3f", seed, r.DistHonest,
				r.DistAttacked)
		}
	}
}
