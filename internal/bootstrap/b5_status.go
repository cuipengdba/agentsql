package bootstrap

import "context"

// B5Status is the non-secret S9 projection linked into health/readiness only
// when an explicit feature-on assembly installs a provider. Feature-off keeps
// the pre-S9 probe representation unchanged.
type B5Status struct {
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
	Reason  string `json:"reason"`
	Ready   bool   `json:"ready"`
}

// SetB5StatusProvider does not activate B5 execution. It links an already
// explicitly assembled S9 runtime to the existing health/readiness surface.
func (runtime *Runtime) SetB5StatusProvider(provider func(context.Context) (B5Status, error)) {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	runtime.b5Probe = provider
	runtime.mu.Unlock()
}

func (runtime *Runtime) B5Status(ctx context.Context) (B5Status, bool, error) {
	if runtime == nil {
		return B5Status{}, false, nil
	}
	runtime.mu.Lock()
	provider := runtime.b5Probe
	runtime.mu.Unlock()
	if provider == nil {
		return B5Status{}, false, nil
	}
	status, err := provider(ctx)
	return status, true, err
}
