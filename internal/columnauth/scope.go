// Package columnauth contains the feature-off B2 control-plane contract.
// The execution gate is intentionally deferred to S3/S4.
package columnauth

import "github.com/cuipengdba/agentsql/internal/model"

const (
	MetadataProtocol  = 2
	ExecutionProtocol = 3
)

// InScope is deliberately exact: B2 column authorization applies only to a
// SELECT. DML continues through the pre-existing table/profile path and must
// never be labelled as B2 column protected.
func InScope(statement model.StmtType) bool {
	return statement == model.StmtType("SELECT")
}
