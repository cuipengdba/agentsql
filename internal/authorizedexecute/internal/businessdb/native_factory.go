package businessdb

import (
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
)

func openNativeExecutor(datasource model.Datasource, password string, readOnly bool) (NativeExecutor, error) {
	switch datasource.DBType {
	case "redis", "valkey":
		return NewRedisExecutor(datasource, password, readOnly)
	case "memcached":
		if password != "" || datasource.Username != "" {
			return nil, fmt.Errorf("Memcached authentication is unsupported")
		}
		return NewMemcachedExecutor(datasource, readOnly)
	default:
		category, ok := model.CategoryOf(datasource.DBType)
		if !ok {
			return nil, fmt.Errorf("unknown native datasource type %q", datasource.DBType)
		}
		if category == model.CategoryRelational {
			return nil, fmt.Errorf("datasource type %q is relational", datasource.DBType)
		}
		return nil, fmt.Errorf("native adapter for %q is not implemented in this release", datasource.DBType)
	}
}
