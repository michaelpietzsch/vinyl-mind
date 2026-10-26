// Command simulate runs the simulation of internal/sim and prints what combining does: how close the global model
// stays to the truth with and without bad contributions, and how much it helps a new profile.
//
//	go run ./tools/simulate [-seed 1] [-stations 8] [-attackers 2] ...
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/michaelpietzsch/vinyl-mind/internal/sim"
)

func main() {
	p := sim.Defaults()
	flag.Uint64Var(&p.Seed, "seed", p.Seed, "random seed")
	flag.IntVar(&p.Stations, "stations", p.Stations, "honest stations (one profile each)")
	flag.IntVar(&p.Recordings, "recordings", p.Recordings, "recordings with a master per honest profile")
	flag.IntVar(&p.Events, "events", p.Events, "detections per recording")
	flag.IntVar(&p.Anchors, "anchors", p.Anchors, "honest stations the operator trusts")
	flag.IntVar(&p.BadAnchors, "bad-anchors", p.BadAnchors, "anchors that turned and send the attackers' model")
	flag.IntVar(&p.Attackers, "attackers", p.Attackers, "stations sending a reversed model with inflated claims")
	flag.Float64Var(&p.AttackerTrust, "attacker-trust", p.AttackerTrust, "the attackers' trust (0.9: the automatic maximum)")
	flag.Float64Var(&p.NoGC, "no-gc", p.NoGC, "share of the honest stations without a second opinion")
	flag.Float64Var(&p.Heterogeneity, "heterogeneity", p.Heterogeneity, "spread of the profiles' true coefficients")
	flag.Float64Var(&p.ChainShift, "chain-shift", p.ChainShift, "spread of the chains' feature offsets")
	flag.Float64Var(&p.RecordShift, "record-shift", p.RecordShift, "spread of the records' feature offsets")
	flag.IntVar(&p.NewRecordings, "new-recordings", p.NewRecordings, "labelled recordings of the new profile")
	flag.IntVar(&p.NewEvents, "new-events", p.NewEvents, "judged detections per recording of the new profile")
	flag.Float64Var(&p.PriorStrength, "prior", p.PriorStrength, "ridge penalty towards the global model")
	flag.Parse()

	r, err := sim.Run(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%d honest stations (%d trusted by the operator), %d attackers at trust %.2f, %d recordings x %d detections per honest profile\n\n",
		p.Stations, p.Anchors, p.Attackers, p.AttackerTrust, p.Recordings, p.Events)
	fmt.Println("Distance of the combined coefficients to the truth (mean absolute difference)")
	fmt.Printf("  honest stations only                 %6.3f\n", r.DistHonest)
	fmt.Printf("  with attackers, Vinyl-Mind           %6.3f\n", r.DistAttacked)
	fmt.Printf("  with attackers, plain weighted mean  %6.3f\n", r.DistNaive)
	fmt.Printf("  honest, constant features averaged   %6.3f\n\n", r.DistUnmasked)
	fmt.Printf("New profile with %d x %d judged detections: estimate error per recording on %d held-out ones\n",
		p.NewRecordings, p.NewEvents, p.TestRecordings)
	fmt.Printf("  raw count                            %5.1f %%\n", 100*r.ErrRaw)
	fmt.Printf("  own model only                       %5.1f %%\n", 100*r.ErrLocal)
	fmt.Printf("  global model as it is                %5.1f %%\n", 100*r.ErrGlobal)
	fmt.Printf("  own model, global model as prior     %5.1f %%\n", 100*r.ErrPrior)
	fmt.Printf("  own model, plain mean as prior       %5.1f %%\n\n", 100*r.ErrPriorNaive)
	fmt.Println("Shares in the combination with attackers")
	for _, u := range r.Used {
		fmt.Printf("  %-12s trust %.2f  share %5.1f %%  self-check error %.3f\n", u.ProfileID, u.Trust,
			100*u.Share, u.Kind.CVErrEst)
	}
	for _, e := range r.Excluded {
		fmt.Printf("  %-12s left out: %s\n", e.ProfileID, e.Reason)
	}
}
