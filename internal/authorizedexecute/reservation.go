package authorizedexecute

import (
	"context"
	"sync"
)

type ReservationLimits struct {
	PerAgent, PerTenant, PerDatasource int
	GlobalMemoryBytes                  int64
}

// DefaultReservationLimits permits a datasource's full concurrency envelope
// (16 requests at 52 MiB each) with 192 MiB of global headroom. The memory
// counter tracks only explicit request buffer reservations; it must never be
// initialized from process/runtime memory, which is outside the request quota.
var DefaultReservationLimits = ReservationLimits{PerAgent: 4, PerTenant: 32, PerDatasource: 16, GlobalMemoryBytes: 1 << 30}

type ReservationPool struct {
	mu                           sync.Mutex
	limits                       ReservationLimits
	agents, tenants, datasources map[string]int
	memory                       int64
}

type Reservation struct {
	pool                      *ReservationPool
	agent, tenant, datasource string
	memory                    int64
	once                      sync.Once
}

type reservationScope struct{ agent, tenant string }
type reservationScopeKey struct{}

// WithReservationScope attaches stable quota identities without exposing a
// mutable reservation or any database capability to the caller.
func WithReservationScope(ctx context.Context, agent, tenant string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, reservationScopeKey{}, reservationScope{agent: agent, tenant: tenant})
}

func scopeFromContext(ctx context.Context) reservationScope {
	if ctx != nil {
		if scope, ok := ctx.Value(reservationScopeKey{}).(reservationScope); ok && scope.agent != "" && scope.tenant != "" {
			return scope
		}
	}
	return reservationScope{agent: "process", tenant: "process"}
}

func NewReservationPool(limits ReservationLimits) *ReservationPool {
	defaults := DefaultReservationLimits
	if limits.PerAgent <= 0 || limits.PerAgent > defaults.PerAgent {
		limits.PerAgent = defaults.PerAgent
	}
	if limits.PerTenant <= 0 || limits.PerTenant > defaults.PerTenant {
		limits.PerTenant = defaults.PerTenant
	}
	if limits.PerDatasource <= 0 || limits.PerDatasource > defaults.PerDatasource {
		limits.PerDatasource = defaults.PerDatasource
	}
	if limits.GlobalMemoryBytes <= 0 || limits.GlobalMemoryBytes > defaults.GlobalMemoryBytes {
		limits.GlobalMemoryBytes = defaults.GlobalMemoryBytes
	}
	return &ReservationPool{limits: limits, agents: map[string]int{}, tenants: map[string]int{}, datasources: map[string]int{}}
}

func (pool *ReservationPool) Reserve(agent, tenant, datasource string, memory int64) (*Reservation, error) {
	if pool == nil || agent == "" || tenant == "" || datasource == "" || memory < 0 {
		return nil, limitError(ReasonConcurrencyLimit)
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.agents[agent] >= pool.limits.PerAgent || pool.tenants[tenant] >= pool.limits.PerTenant || pool.datasources[datasource] >= pool.limits.PerDatasource {
		return nil, limitError(ReasonConcurrencyLimit)
	}
	if memory > pool.limits.GlobalMemoryBytes || pool.memory > pool.limits.GlobalMemoryBytes-memory {
		return nil, limitError(ReasonMemoryLimit)
	}
	pool.agents[agent]++
	pool.tenants[tenant]++
	pool.datasources[datasource]++
	pool.memory += memory
	return &Reservation{pool: pool, agent: agent, tenant: tenant, datasource: datasource, memory: memory}, nil
}

func (reservation *Reservation) Release() {
	if reservation == nil || reservation.pool == nil {
		return
	}
	reservation.once.Do(func() {
		pool := reservation.pool
		pool.mu.Lock()
		defer pool.mu.Unlock()
		decrement := func(values map[string]int, key string) {
			if values[key] <= 1 {
				delete(values, key)
			} else {
				values[key]--
			}
		}
		decrement(pool.agents, reservation.agent)
		decrement(pool.tenants, reservation.tenant)
		decrement(pool.datasources, reservation.datasource)
		pool.memory -= reservation.memory
		if pool.memory < 0 {
			pool.memory = 0
		}
	})
}
