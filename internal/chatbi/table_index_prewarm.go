package chatbi

import (
	"context"
	"errors"
	"sync"
	"time"
)

type tableIndexPrewarmKey struct {
	workspace, user, app, datasource ID
	visitor                          string
	authRevision, appRevision        int64
}

type tableIndexPrewarmStatus struct {
	State   string    `json:"state"`
	Updated time.Time `json:"updated_at"`
}

type tableIndexPrewarmJob struct {
	key       tableIndexPrewarmKey
	principal Principal
	check     func(context.Context) error
}

type tableIndexPrewarmer struct {
	service *Service
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	jobs    chan tableIndexPrewarmJob
	mu      sync.Mutex
	status  map[tableIndexPrewarmKey]tableIndexPrewarmStatus
}

func tableIndexJobKey(p Principal, datasource ID) tableIndexPrewarmKey {
	return tableIndexPrewarmKey{workspace: p.Workspace, user: p.User.ID, app: p.EmbeddedApp,
		visitor: p.VisitorScope, datasource: datasource, authRevision: p.User.AuthRevision,
		appRevision: p.EmbeddedRevision}
}

// EnableTableIndexPrewarm starts one bounded, cancellable worker. Call the
// returned stop function before closing the model or encrypted Store.
func (service *Service) EnableTableIndexPrewarm(ctx context.Context) func() {
	if service == nil || service.Embedding == nil || service.Store == nil {
		return func() {}
	}
	identity, cacheable := service.Embedding.(interface{ persistentEmbeddingIdentity() string })
	if !cacheable || identity.persistentEmbeddingIdentity() == "" {
		return func() {}
	}
	workerCtx, cancel := context.WithCancel(ctx)
	worker := &tableIndexPrewarmer{service: service, ctx: workerCtx, cancel: cancel,
		done: make(chan struct{}), jobs: make(chan tableIndexPrewarmJob, 16),
		status: make(map[tableIndexPrewarmKey]tableIndexPrewarmStatus)}
	service.indexPrewarmer = worker
	go worker.run()
	return func() {
		cancel()
		<-worker.done
	}
}

// QueueTableIndexPrewarm accepts a request only after its caller verified the
// identity. The worker repeats that verification and all schema/policy checks.
// Duplicate work is coalesced; the queue and retained status are bounded.
func (service *Service) QueueTableIndexPrewarm(p Principal, datasource ID,
	check func(context.Context) error) bool {
	worker := service.indexPrewarmer
	if worker == nil || !datasource.Valid() || !p.User.ID.Valid() || !p.Workspace.Valid() || check == nil || worker.ctx.Err() != nil {
		return false
	}
	key := tableIndexJobKey(p, datasource)
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if current, found := worker.status[key]; found {
		if current.State == "queued" || current.State == "running" ||
			(current.State == "ready" && time.Since(current.Updated) < time.Minute) {
			return true
		}
	}
	if len(worker.status) >= 128 {
		var oldestKey tableIndexPrewarmKey
		var oldest time.Time
		found := false
		for candidate, status := range worker.status {
			if status.State == "queued" || status.State == "running" {
				continue
			}
			if !found || status.Updated.Before(oldest) {
				oldestKey, oldest, found = candidate, status.Updated, true
			}
		}
		if !found {
			return false
		}
		delete(worker.status, oldestKey)
	}
	select {
	case worker.jobs <- tableIndexPrewarmJob{key: key, principal: p, check: check}:
		worker.status[key] = tableIndexPrewarmStatus{State: "queued", Updated: time.Now().UTC()}
		return true
	default:
		worker.status[key] = tableIndexPrewarmStatus{State: "failed", Updated: time.Now().UTC()}
		return false
	}
}

func (service *Service) TableIndexPrewarmStatus(p Principal, datasource ID) tableIndexPrewarmStatus {
	worker := service.indexPrewarmer
	if worker == nil || !datasource.Valid() {
		return tableIndexPrewarmStatus{State: "unavailable"}
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if current, found := worker.status[tableIndexJobKey(p, datasource)]; found {
		return current
	}
	return tableIndexPrewarmStatus{State: "idle"}
}

func (worker *tableIndexPrewarmer) setStatus(key tableIndexPrewarmKey, state string) {
	worker.mu.Lock()
	worker.status[key] = tableIndexPrewarmStatus{State: state, Updated: time.Now().UTC()}
	worker.mu.Unlock()
}

func (worker *tableIndexPrewarmer) run() {
	defer close(worker.done)
	for {
		select {
		case <-worker.ctx.Done():
			worker.mu.Lock()
			for key, status := range worker.status {
				if status.State == "queued" || status.State == "running" {
					worker.status[key] = tableIndexPrewarmStatus{State: "cancelled", Updated: time.Now().UTC()}
				}
			}
			worker.mu.Unlock()
			return
		case job := <-worker.jobs:
			if worker.ctx.Err() != nil {
				worker.setStatus(job.key, "cancelled")
				continue
			}
			worker.setStatus(job.key, "running")
			ctx, cancel := context.WithTimeout(worker.ctx, 5*time.Minute)
			err := worker.service.prewarmAuthorizedTableIndex(ctx, job.principal, job.key.datasource, job.check)
			cancel()
			state := "ready"
			if err != nil {
				state = "failed"
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					state = "cancelled"
				}
			}
			worker.setStatus(job.key, state)
		}
	}
}

func (service *Service) prewarmAuthorizedTableIndex(ctx context.Context, p Principal, datasource ID,
	check func(context.Context) error) error {
	if service.Embedding == nil || check == nil {
		return ErrUnavailable
	}
	if err := check(ctx); err != nil {
		return err
	}
	settings, err := service.Store.ChatSettings(ctx)
	if err != nil || settings.Embedding == nil || !settings.Embedding.TableEnabled {
		return ErrUnavailable
	}
	binding, revision, digest, err := service.bindingSnapshot(ctx, p, datasource)
	if err != nil {
		return err
	}
	var selected *ID
	if p.EmbeddedApp != "" {
		var app AssistantApplication
		appRevision, appErr := service.Store.get(ctx, assistantKey(p.Workspace, p.EmbeddedApp), &app)
		if appErr != nil || appRevision != p.EmbeddedRevision || !app.Enabled {
			return ErrUnauthenticated
		}
		if app.UseCustomModel {
			if app.CustomModel == nil || !app.CustomModel.Valid() {
				return ErrModel
			}
			selected = app.CustomModel
		}
	}
	model, lease, err := service.Store.modelForWorkspaceSelection(ctx, p.Workspace, selected)
	if err != nil {
		return err
	}
	columns, rules, err := service.semanticSchemaWithPermissionSnapshot(ctx, p, binding)
	if err != nil {
		return err
	}
	columns = append([]Column(nil), columns...)
	remarks, err := managedFileRemarkHints(binding, columns)
	if databaseBinding(binding) {
		remarks, err = databaseRemarkHints(binding, columns)
	}
	if err != nil {
		return err
	}
	fingerprint, err := service.modelContextFingerprint(ctx, p, digest, rules)
	if err != nil {
		return err
	}
	tables, _, ok := authorizedTableCandidates(columns, remarks, 0)
	if !ok {
		return ErrUnavailable
	}
	// Foreground selection skips schemas within the configured candidate
	// limit, so prewarming those vectors would consume CPU without benefit.
	if len(tables) <= settings.Embedding.TableCount {
		return nil
	}
	validateSnapshot := service.tableIndexValidator(p, binding, revision, digest, rules, columns, remarks,
		model, lease, fingerprint, check)
	validate := func(checkCtx context.Context) error {
		current, err := service.Store.ChatSettings(checkCtx)
		if err != nil || current.Embedding == nil || *current.Embedding != *settings.Embedding {
			return ErrGateway
		}
		return validateSnapshot(checkCtx)
	}
	_, err = service.Store.indexedTableVectors(ctx, p, datasource, service.Embedding, tables, validate)
	return err
}
