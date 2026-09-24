package businessdb

import "sort"

// PoolStat is one instantaneous managed datasource pool snapshot.
type PoolStat struct {
	DatasourceID string
	Dialect      string
	MaxOpen      int
	InUse        int
	Idle         int
}

type poolStatser interface {
	poolSnapshot() PoolStat
}

// SnapshotPools returns deterministic snapshots for executors that expose pool statistics.
func (manager *Manager) SnapshotPools() []PoolStat {
	stats := make([]PoolStat, 0)
	if manager == nil {
		return stats
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	for datasourceID, managed := range manager.executors {
		if isNilExecutor(managed) {
			continue
		}
		statser, ok := managed.(poolStatser)
		if !ok || isNilValue(statser) {
			continue
		}
		stat := statser.poolSnapshot()
		stat.DatasourceID = datasourceID
		stat.Dialect = managed.Dialect()
		stats = append(stats, stat)
	}
	sort.Slice(stats, func(left, right int) bool {
		return stats[left].DatasourceID < stats[right].DatasourceID
	})
	return stats
}
