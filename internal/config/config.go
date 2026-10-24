// Package config reads the configuration: an optional TOML file for behaviour, environment variables for what
// differs per deployment (paths, database URL, admin token). The container image stays the same for every operator,
// and nothing here names a host or a domain: anyone can run an instance.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Aggregation controls how contributions are combined (docs/aggregation.md).
type Aggregation struct {
	Method           string  `toml:"method"`             // "trimmed_mean" or "median"
	Trim             float64 `toml:"trim"`               // share of the weight cut off at each end, per coefficient
	ManualWeight     float64 `toml:"manual_weight"`      // as on the station: a judgement by hand counts five times
	MaxEstError      float64 `toml:"max_est_error"`      // the station's MAX_EST_ERROR: worse self-checks are left out
	MaxStationShare  float64 `toml:"max_station_share"`  // cap of a fully trusted station's share in the mean
	MinProfiles      int     `toml:"min_profiles"`       // per kind, before a model is built at all
	MinStations      int     `toml:"min_stations"`       // so that one station with several profiles cannot publish
	MinClassProfiles int     `toml:"min_class_profiles"` // before a chain class gets a model of its own
	TrustStart       float64 `toml:"trust_start"`
	TrustMin         float64 `toml:"trust_min"`
	TrustStep        float64 `toml:"trust_step"`    // gained per day with consistent contributions or reports
	TrustPenalty     float64 `toml:"trust_penalty"` // lost per contribution far from the consensus
	ConsistencyK     float64 `toml:"consistency_k"` // "far" = more than k spreads from the combined coefficient
}

// Publication controls when candidates are published and models withdrawn.
type Publication struct {
	Auto              bool    `toml:"auto"`                // by check reports; otherwise only by the operator
	MinReports        int     `toml:"min_reports"`         // reporting profiles before a decision
	MinReportStations int     `toml:"min_report_stations"` // distinct stations among them before publishing
	Tolerance         float64 `toml:"tolerance"`           // how much worse than local still counts as not worse
	CombineIntervalS  int     `toml:"combine_interval_s"`  // background combining; 0 = only by command
}

// Config is the whole configuration.
type Config struct {
	DataDir               string   `toml:"-"`
	DatabaseURL           string   `toml:"database_url"` // default: SQLite file in the data directory
	AdminToken            string   `toml:"-"`            // only from the environment; no token, no overview
	Listen                string   `toml:"listen"`
	JoinMode              string   `toml:"join_mode"` // "approval": without invitation, wait; "invitation": required
	ClockSkewS            int      `toml:"clock_skew_s"`
	StationRequestsPerMin int      `toml:"station_requests_per_min"`
	AddressRequestsPerMin int      `toml:"address_requests_per_min"`
	JoinsPerHour          int      `toml:"joins_per_hour"` // per address, against many fake stations
	AccessLogDays         int      `toml:"access_log_days"`
	MaxProfilesPerStation int      `toml:"max_profiles_per_station"`
	FeatureVersions       []int    `toml:"feature_versions"`
	TrustedProxies        []string `toml:"trusted_proxies"` // whose X-Forwarded-For is believed

	Aggregation Aggregation `toml:"aggregation"`
	Publication Publication `toml:"publication"`

	proxies []netip.Prefix
}

// Default is the configuration without file and environment.
func Default() Config {
	return Config{
		DataDir: "data", Listen: ":8080", JoinMode: "approval", ClockSkewS: 300,
		StationRequestsPerMin: 60, AddressRequestsPerMin: 120, JoinsPerHour: 10, AccessLogDays: 14,
		MaxProfilesPerStation: 16, FeatureVersions: []int{1},
		Aggregation: Aggregation{
			Method: "trimmed_mean", Trim: 0.2, ManualWeight: 5, MaxEstError: 0.30, MaxStationShare: 0.25,
			MinProfiles: 3, MinStations: 2, MinClassProfiles: 5,
			TrustStart: 0.25, TrustMin: 0.05, TrustStep: 0.1, TrustPenalty: 0.15, ConsistencyK: 3,
		},
		Publication: Publication{Auto: true, MinReports: 3, MinReportStations: 2, Tolerance: 0.01,
			CombineIntervalS: 3600},
	}
}

// Load reads the TOML file (path, or VINYL_MIND_CONFIG, or config.toml in the data directory if it exists) and the
// environment. Unknown keys are an error, so a typo does not silently fall back to a default.
func Load(path string) (Config, error) {
	c := Default()
	if d := os.Getenv("VINYL_MIND_DATA_DIR"); d != "" {
		c.DataDir = d
	}
	explicit := path != "" || os.Getenv("VINYL_MIND_CONFIG") != ""
	if path == "" {
		path = os.Getenv("VINYL_MIND_CONFIG")
	}
	if path == "" {
		path = filepath.Join(c.DataDir, "config.toml")
	}
	if _, err := os.Stat(path); err == nil {
		md, err := toml.DecodeFile(path, &c)
		if err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
		if und := md.Undecoded(); len(und) > 0 {
			keys := make([]string, len(und))
			for i, k := range und {
				keys[i] = k.String()
			}
			return c, fmt.Errorf("%s: unknown setting(s): %s", path, strings.Join(keys, ", "))
		}
	} else if explicit {
		return c, fmt.Errorf("configuration file: %w", err)
	}
	if v := os.Getenv("VINYL_MIND_DATABASE_URL"); v != "" {
		c.DatabaseURL = v
	}
	if v := os.Getenv("VINYL_MIND_LISTEN"); v != "" {
		c.Listen = v
	}
	c.AdminToken = os.Getenv("VINYL_MIND_ADMIN_TOKEN")
	return c, c.Check()
}

// Check validates the values and prepares derived ones.
func (c *Config) Check() error {
	a, p := c.Aggregation, c.Publication
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if c.JoinMode != "approval" && c.JoinMode != "invitation" {
		bad("join_mode must be \"approval\" or \"invitation\", not %q", c.JoinMode)
	}
	if a.Method != "trimmed_mean" && a.Method != "median" {
		bad("aggregation.method must be \"trimmed_mean\" or \"median\", not %q", a.Method)
	}
	if a.Trim < 0 || a.Trim >= 0.5 {
		bad("aggregation.trim must be in [0, 0.5)")
	}
	if a.MaxStationShare <= 0 || a.MaxStationShare > 1 {
		bad("aggregation.max_station_share must be in (0, 1]")
	}
	if a.TrustMin <= 0 || a.TrustStart < a.TrustMin || a.TrustStart > 1 {
		bad("aggregation: need 0 < trust_min <= trust_start <= 1")
	}
	if a.MinProfiles < 1 || a.MinStations < 1 || a.MinClassProfiles < a.MinProfiles {
		bad("aggregation: need min_profiles, min_stations >= 1 and min_class_profiles >= min_profiles")
	}
	if p.MinReports < 1 || p.MinReportStations < 1 {
		bad("publication: min_reports and min_report_stations must be at least 1")
	}
	if c.ClockSkewS < 30 {
		bad("clock_skew_s must be at least 30")
	}
	c.proxies = nil
	for _, s := range c.TrustedProxies {
		pfx, err := netip.ParsePrefix(s)
		if err != nil {
			if addr, err2 := netip.ParseAddr(s); err2 == nil {
				pfx = netip.PrefixFrom(addr, addr.BitLen())
			} else {
				bad("trusted_proxies: %q is neither an address nor a prefix", s)
				continue
			}
		}
		c.proxies = append(c.proxies, pfx.Masked())
	}
	return errors.Join(errs...)
}

// DBURL is the database URL, by default a SQLite file in the data directory.
func (c *Config) DBURL() string {
	if c.DatabaseURL != "" {
		return c.DatabaseURL
	}
	return "sqlite://" + filepath.Join(c.DataDir, "vinyl-mind.sqlite")
}

// ServerKeyPath is where the server's signing key lives.
func (c *Config) ServerKeyPath() string { return filepath.Join(c.DataDir, "server-key.pem") }

// ClockSkew is the accepted difference between a request's timestamp and the server clock.
func (c *Config) ClockSkew() time.Duration { return time.Duration(c.ClockSkewS) * time.Second }

// TrustedProxy reports whether a peer address belongs to a configured reverse proxy.
func (c *Config) TrustedProxy(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range c.proxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
