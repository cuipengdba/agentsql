package businessdb

import (
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
)

// StarRocksExecutor uses the FE MySQL protocol and the shared OLAP policy.
type StarRocksExecutor struct{ *olapMySQLExecutor }

func NewStarRocksExecutor(ds model.Datasource, password string, readOnly bool) (*StarRocksExecutor, error) {
	if ds.DBType != "starrocks" {
		return nil, fmt.Errorf("invalid StarRocks dialect")
	}
	e, err := newOLAPMySQLExecutor(ds, password, readOnly)
	if err != nil {
		return nil, err
	}
	return &StarRocksExecutor{e}, nil
}
