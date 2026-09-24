package businessdb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagerSnapshotPoolsCollectsAndSkipsOptionalStats(t *testing.T) {
	manager := &Manager{executors: map[string]Executor{
		"ds-z": &poolAwareExecutor{
			Executor: nil,
			dialect:  "mysql",
			stat:     PoolStat{MaxOpen: 9, InUse: 4, Idle: 5},
		},
		"ds-a": &poolAwareExecutor{
			Executor: nil,
			dialect:  "postgres",
			stat:     PoolStat{MaxOpen: 5, InUse: 2, Idle: 3},
		},
		"ds-skip": &poolBlindExecutor{Executor: nil, dialect: "postgres"},
	}}

	require.Equal(t, []PoolStat{
		{DatasourceID: "ds-a", Dialect: "postgres", MaxOpen: 5, InUse: 2, Idle: 3},
		{DatasourceID: "ds-z", Dialect: "mysql", MaxOpen: 9, InUse: 4, Idle: 5},
	}, manager.SnapshotPools())
	var nilManager *Manager
	require.Empty(t, nilManager.SnapshotPools())
}

func TestDialectExecutorsExposeConfiguredPoolMaximum(t *testing.T) {
	postgres := (&PostgresExecutor{maxConns: 7}).poolSnapshot()
	require.Equal(t, PoolStat{MaxOpen: 7}, postgres)
	mysql := (&MySQLExecutor{maxConns: 11}).poolSnapshot()
	require.Equal(t, PoolStat{MaxOpen: 11}, mysql)

	var nilPostgres *PostgresExecutor
	var nilMySQL *MySQLExecutor
	require.Equal(t, PoolStat{}, nilPostgres.poolSnapshot())
	require.Equal(t, PoolStat{}, nilMySQL.poolSnapshot())
}

type poolAwareExecutor struct {
	Executor
	dialect string
	stat    PoolStat
}

func (executor *poolAwareExecutor) Dialect() string { return executor.dialect }
func (executor *poolAwareExecutor) poolSnapshot() PoolStat {
	return executor.stat
}

type poolBlindExecutor struct {
	Executor
	dialect string
}

func (executor *poolBlindExecutor) Dialect() string { return executor.dialect }

var _ Executor = (*poolAwareExecutor)(nil)
var _ Executor = (*poolBlindExecutor)(nil)
var _ poolStatser = (*poolAwareExecutor)(nil)
var _ poolStatser = (*PostgresExecutor)(nil)
var _ poolStatser = (*MySQLExecutor)(nil)
