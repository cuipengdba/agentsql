package parser

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestMySQLDualFilteringPreservesRealTables(t *testing.T) {
	tables := objectSet{}
	tables.add(model.ObjectRef{Table: "dual"})
	tables.add(model.ObjectRef{Table: "DUAL", Alias: "implicit"})
	tables.add(model.ObjectRef{Schema: "app", Table: "dual"})
	tables.add(model.ObjectRef{Table: "users", Alias: "u"})

	tables.removeUnqualifiedFold(stringSet{"dual": {}})

	require.Equal(t, sortedObjects([]model.ObjectRef{
		{Schema: "app", Table: "dual"},
		{Table: "users", Alias: "u"},
	}), sortedObjects(tables.sorted()))
}
