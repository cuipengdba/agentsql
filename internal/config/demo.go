package config

import (
	"fmt"
	"time"
)

const (
	DemoDatasourcePG        = "ds-demo-pg"
	DemoDatasourceMySQL     = "ds-demo-mysql"
	DemoAgentRO             = "agent-demo-ro"
	DemoAgentDML            = "agent-demo-dml"
	DemoDefaultQPSPerAgent  = 2
	DemoDefaultBanner       = "演示环境·数据每日重置·禁止接入真实数据与真实数据库"
	demoEnvironmentVariable = "AGENTSQL_DEMO"
	demoAnchorDateLayout    = "2006-01-02"
)

// DemoConfig controls the bounded Live Demo surface.
type DemoConfig struct {
	Enabled              bool     `yaml:"enabled"`
	Banner               string   `yaml:"banner"`
	AllowedDatasourceIDs []string `yaml:"allowed_datasource_ids"`
	QPSPerAgent          int      `yaml:"qps_per_agent"`
	AnchorDate           string   `yaml:"anchor_date"`
}

// DemoEnabled reports whether the Live Demo mode is explicitly enabled.
func (config Config) DemoEnabled() bool {
	return config.Demo.Enabled
}

// AllowsDatasource reports whether an ID belongs to the enabled demo's fixed
// datasource allowlist.
func (demo DemoConfig) AllowsDatasource(id string) bool {
	if !demo.Enabled {
		return false
	}
	for _, allowedID := range demo.AllowedDatasourceIDs {
		if id == allowedID {
			return true
		}
	}
	return false
}

// EffectiveQPS returns the configured demo rate or its default.
func (demo DemoConfig) EffectiveQPS() int {
	if demo.QPSPerAgent == 0 {
		return DemoDefaultQPSPerAgent
	}
	return demo.QPSPerAgent
}

func applyDemoEnvironment(config *Config, lookup func(string) (string, bool)) error {
	value, present := lookup(demoEnvironmentVariable)
	if !present || value == "" {
		return nil
	}
	if value != "1" {
		return fmt.Errorf(`%s must be unset, empty, or exactly "1"`, demoEnvironmentVariable)
	}
	config.Demo.Enabled = true
	return nil
}

func defaultAndValidateDemo(demo *DemoConfig) error {
	if demo == nil || !demo.Enabled {
		return nil
	}

	if len(demo.AllowedDatasourceIDs) == 0 {
		demo.AllowedDatasourceIDs = []string{DemoDatasourcePG, DemoDatasourceMySQL}
	}
	if err := validateDemoDatasourceIDs(demo.AllowedDatasourceIDs); err != nil {
		return err
	}

	if demo.QPSPerAgent == 0 {
		demo.QPSPerAgent = DemoDefaultQPSPerAgent
	}
	if demo.QPSPerAgent < 1 || demo.QPSPerAgent > 5 {
		return fmt.Errorf("demo.qps_per_agent must be between 1 and 5")
	}

	if demo.Banner == "" {
		demo.Banner = DemoDefaultBanner
	}
	if demo.AnchorDate != "" {
		if _, err := time.Parse(demoAnchorDateLayout, demo.AnchorDate); err != nil {
			return fmt.Errorf("demo.anchor_date must be a real date in YYYY-MM-DD format: %w", err)
		}
	}
	return nil
}

func validateDemoDatasourceIDs(ids []string) error {
	if len(ids) != 2 {
		return fmt.Errorf("demo.allowed_datasource_ids must contain exactly %q and %q", DemoDatasourcePG, DemoDatasourceMySQL)
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != DemoDatasourcePG && id != DemoDatasourceMySQL {
			return fmt.Errorf("demo.allowed_datasource_ids contains unsupported datasource %q", id)
		}
		if seen[id] {
			return fmt.Errorf("demo.allowed_datasource_ids contains duplicate datasource %q", id)
		}
		seen[id] = true
	}
	return nil
}
