package bootstrap

import (
	"context"
	"sync"

	"github.com/cuipengdba/agentsql/internal/store"
)

// notificationNameResolver keeps database access out of notification workers.
// Refresh builds a complete replacement snapshot and only swaps it after both
// repository reads succeed, so readers never observe a partially refreshed map.
type notificationNameResolver struct {
	store       *store.Store
	mu          sync.RWMutex
	agents      map[string]string
	datasources map[string]string
}

func newNotificationNameResolver(metadata *store.Store) *notificationNameResolver {
	return &notificationNameResolver{
		store:       metadata,
		agents:      make(map[string]string),
		datasources: make(map[string]string),
	}
}

func (resolver *notificationNameResolver) Refresh(ctx context.Context) error {
	if resolver == nil || resolver.store == nil || ctx == nil {
		return nil
	}
	agents, err := resolver.store.Agents().List(ctx)
	if err != nil {
		return err
	}
	datasources, err := resolver.store.Datasources().List(ctx)
	if err != nil {
		return err
	}
	nextAgents := make(map[string]string, len(agents))
	for _, agent := range agents {
		nextAgents[agent.ID] = agent.Name
	}
	nextDatasources := make(map[string]string, len(datasources))
	for _, datasource := range datasources {
		nextDatasources[datasource.ID] = datasource.Name
	}
	resolver.mu.Lock()
	resolver.agents = nextAgents
	resolver.datasources = nextDatasources
	resolver.mu.Unlock()
	return nil
}

func (resolver *notificationNameResolver) AgentName(_ context.Context, id string) string {
	if resolver == nil {
		return ""
	}
	resolver.mu.RLock()
	name := resolver.agents[id]
	resolver.mu.RUnlock()
	return name
}

func (resolver *notificationNameResolver) DatasourceName(_ context.Context, id string) string {
	if resolver == nil {
		return ""
	}
	resolver.mu.RLock()
	name := resolver.datasources[id]
	resolver.mu.RUnlock()
	return name
}
