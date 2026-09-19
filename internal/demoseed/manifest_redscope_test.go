package demoseed

import (
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/stretchr/testify/require"
)

func TestValidateMaskRulesAllowsSameColumnsAcrossDatasources(t *testing.T) {
	require.NoError(t, validateMaskRules(fixedDemoMaskRules()))
}

func TestValidateMaskRulesRejectsDuplicateColumnWithinDatasource(t *testing.T) {
	rules := fixedDemoMaskRules()
	rules[2].ID = "mask-demo-pg-phone-duplicate"
	rules[2].DatasourceID = config.DemoDatasourcePG

	err := validateMaskRules(rules)
	require.Error(t, err)
	require.True(t, errors.Is(err, mask.ErrDuplicateMaskColumn))
}

func fixedDemoMaskRules() []MaskRuleManifest {
	return []MaskRuleManifest{
		{ID: "mask-demo-pg-phone", DatasourceID: config.DemoDatasourcePG, ColumnName: "phone", SensitiveType: "phone", Algo: "mask"},
		{ID: "mask-demo-pg-email", DatasourceID: config.DemoDatasourcePG, ColumnName: "email", SensitiveType: "email", Algo: "mask"},
		{ID: "mask-demo-mysql-phone", DatasourceID: config.DemoDatasourceMySQL, ColumnName: "phone", SensitiveType: "phone", Algo: "mask"},
		{ID: "mask-demo-mysql-email", DatasourceID: config.DemoDatasourceMySQL, ColumnName: "email", SensitiveType: "email", Algo: "mask"},
	}
}
