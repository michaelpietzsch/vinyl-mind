package spec

import (
	"math"
	"slices"
)

// Contribution is what one turntable profile sends (contribution.schema.json).
type Contribution struct {
	Spec            string `json:"spec"`
	FeatureVersion  int    `json:"feature_version"`
	DetectorVersion string `json:"detector_version"`
	Client          struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"client"`
	Chain     Chain                       `json:"chain"`
	TrainedAt *int64                      `json:"trained_at,omitempty"`
	Kinds     map[string]KindContribution `json:"kinds"`
}

// Chain is the coarse class of the signal chain.
type Chain struct {
	Source    string `json:"source"`
	Cartridge string `json:"cartridge"`
}

// Class is the chain class key, for example "turntable_interface.mc".
func (c Chain) Class() string { return c.Source + "." + c.Cartridge }

// ClassAll is the model over all chain classes.
const ClassAll = "all"

// KindContribution is the model of one kind with the counts and the cross-check behind it.
type KindContribution struct {
	Coef             []float64 `json:"coef"`
	NReal            int       `json:"n_real"`
	NFalse           int       `json:"n_false"`
	NManual          int       `json:"n_manual"`
	NMaster          int       `json:"n_master"`
	MasterRecordings int       `json:"master_recordings"`
	CVErrEst         float64   `json:"cv_err_est"`
	CVErrRaw         float64   `json:"cv_err_raw"`
	AUC              *float64  `json:"auc,omitempty"`
}

func (c *Contribution) check(v *Version) error {
	for kind, k := range c.Kinds {
		if k.NManual+k.NMaster != k.NReal+k.NFalse {
			return fail("kinds/%s: n_manual + n_master must equal n_real + n_false", kind)
		}
		for _, x := range k.Coef {
			if math.Abs(x) > v.CoefBound {
				return fail("kinds/%s/coef: outside ±%g", kind, v.CoefBound)
			}
		}
	}
	return nil
}

// CheckReport is how a profile reports a global model on its own master recordings (check-report.schema.json).
type CheckReport struct {
	Spec           string               `json:"spec"`
	FeatureVersion int                  `json:"feature_version"`
	Adopted        bool                 `json:"adopted"`
	Kinds          map[string]KindCheck `json:"kinds"`
}

// KindCheck compares the estimate error of the local and the global model for one kind.
type KindCheck struct {
	LocalErr         float64 `json:"local_err"`
	GlobalErr        float64 `json:"global_err"`
	MasterRecordings int     `json:"master_recordings"`
}

// GlobalModel is the payload of a signed model (global-model.schema.json).
type GlobalModel struct {
	Spec            string               `json:"spec"`
	Version         int64                `json:"version"`
	FeatureVersion  int                  `json:"feature_version"`
	DetectorVersion string               `json:"detector_version"`
	ChainClass      string               `json:"chain_class"`
	CreatedAt       int64                `json:"created_at"`
	Server          string               `json:"server"`
	Features        []string             `json:"features"`
	Scaling         Scaling              `json:"scaling"`
	Aggregation     AggregationInfo      `json:"aggregation"`
	Kinds           map[string]KindModel `json:"kinds"`
}

// Scaling is the fixed shared scaling of the features, repeated in every model so that it is self-contained.
type Scaling struct {
	Center []float64 `json:"center"`
	Scale  []float64 `json:"scale"`
}

// AggregationInfo records how the model was combined.
type AggregationInfo struct {
	Method          string  `json:"method"`
	Trim            float64 `json:"trim"`
	ManualWeight    float64 `json:"manual_weight"`
	MaxEstError     float64 `json:"max_est_error"`
	MaxStationShare float64 `json:"max_station_share"`
}

// KindModel is the combined model of one kind.
type KindModel struct {
	Coef     []float64 `json:"coef"`
	Spread   []float64 `json:"spread"`
	Profiles int       `json:"profiles"`
	Stations int       `json:"stations"`
	Examples float64   `json:"examples"`
}

func (m *GlobalModel) check(v *Version) error {
	if !slices.Equal(m.Features, v.Names()) {
		return fail("features: do not match the feature version")
	}
	if !slices.Equal(m.Scaling.Center, v.Center()) || !slices.Equal(m.Scaling.Scale, v.Scale()) {
		return fail("scaling: does not match the feature version")
	}
	return nil
}
