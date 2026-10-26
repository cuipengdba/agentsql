package config

import (
	"errors"
	"strings"
)

// ManagedFileConfig selects one dedicated PostgreSQL import target. The
// provisioner login is never stored as an ordinary Agent datasource.
type ManagedFileConfig struct {
	Enabled                bool   `yaml:"enabled"`
	TenantID               string `yaml:"tenant_id"`
	ReadDatasourceID       string `yaml:"read_datasource_id"`
	ProvisionerUsername    string `yaml:"provisioner_username"`
	ProvisionerPasswordEnv string `yaml:"provisioner_password_env"`
}

func (value ManagedFileConfig) Validate(root Config) error {
	if !value.Enabled {
		if value.TenantID != "" || value.ReadDatasourceID != "" || value.ProvisionerUsername != "" || value.ProvisionerPasswordEnv != "" {
			return errors.New("disabled managed files must not carry a partial target")
		}
		return nil
	}
	if !root.Server.ConsoleEnabled || !root.ColumnAuthorization.Enabled ||
		value.TenantID == "" || len(value.TenantID) > 128 || strings.TrimSpace(value.TenantID) != value.TenantID ||
		value.ReadDatasourceID == "" || len(value.ReadDatasourceID) > 128 || strings.TrimSpace(value.ReadDatasourceID) != value.ReadDatasourceID ||
		value.ProvisionerUsername == "" || len(value.ProvisionerUsername) > 63 || strings.TrimSpace(value.ProvisionerUsername) != value.ProvisionerUsername ||
		!validEnvironmentVariableName(value.ProvisionerPasswordEnv) {
		return errors.New("enabled managed files require console, column authorization, tenant, read datasource and a dedicated provisioner credential")
	}
	return nil
}
