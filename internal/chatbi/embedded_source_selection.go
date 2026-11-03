package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
)

type embeddedSourceCandidate struct {
	entry    assistantCatalogEntry
	binding  Binding
	revision int64
	digest   [32]byte
}

// selectEmbeddedSource sends only current, gateway-visible sources to the
// selection model. The chosen ID remains untrusted until BindEmbeddedSource
// repeats the authorization and schema checks and atomically binds the chat.
func (service *Service) selectEmbeddedSource(ctx context.Context, p Principal, question string,
	requestedModel *ID) (ID, ModelConfig, modelLease, error) {
	if p.EmbeddedApp == "" {
		return "", ModelConfig{}, modelLease{}, ErrUnauthenticated
	}
	if err := service.live(ctx, p); err != nil {
		return "", ModelConfig{}, modelLease{}, err
	}
	var app AssistantApplication
	appRevision, err := service.Store.get(ctx, assistantKey(p.Workspace, p.EmbeddedApp), &app)
	if err != nil || appRevision != p.EmbeddedRevision || app.ID != p.EmbeddedApp || !app.Enabled {
		return "", ModelConfig{}, modelLease{}, ErrUnauthenticated
	}
	if app.UseCustomModel {
		if app.CustomModel == nil || !app.CustomModel.Valid() ||
			requestedModel != nil && *requestedModel != *app.CustomModel {
			return "", ModelConfig{}, modelLease{}, ErrModel
		}
		requestedModel = app.CustomModel
	} else if requestedModel != nil {
		return "", ModelConfig{}, modelLease{}, ErrModel
	}
	raw, err := service.Store.list(ctx, "binding", string(p.Workspace), string(p.User.ID))
	if err != nil {
		return "", ModelConfig{}, modelLease{}, err
	}
	candidates := make([]embeddedSourceCandidate, 0, len(raw))
	for _, item := range raw {
		var b Binding
		if json.Unmarshal(item, &b) != nil || b.Workspace != p.Workspace || b.User != p.User.ID || !b.Datasource.Valid() {
			return "", ModelConfig{}, modelLease{}, ErrUnavailable
		}
		if err := service.embeddedSourceAllowed(ctx, p, b.Datasource); errors.Is(err, ErrNotFound) {
			continue
		} else if err != nil {
			return "", ModelConfig{}, modelLease{}, err
		}
		current, revision, digest, err := service.bindingSnapshot(ctx, p, b.Datasource)
		if err != nil {
			return "", ModelConfig{}, modelLease{}, err
		}
		if _, err := service.schema(ctx, p, current); err != nil {
			return "", ModelConfig{}, modelLease{}, err
		}
		candidates = append(candidates, embeddedSourceCandidate{
			entry:   assistantCatalogEntry{CatalogID: b.Datasource, Datasource: b.Datasource, Name: current.Name},
			binding: current, revision: revision, digest: digest,
		})
		if len(candidates) > 32 {
			return "", ModelConfig{}, modelLease{}, ErrUnavailable
		}
	}
	if len(candidates) == 0 {
		return "", ModelConfig{}, modelLease{}, ErrUnavailable
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].entry.CatalogID < candidates[j].entry.CatalogID })
	selected := candidates[0].entry.CatalogID
	var model ModelConfig
	var lease modelLease
	if len(candidates) > 1 {
		model, lease, err = service.Store.modelForWorkspaceSelection(ctx, p.Workspace, requestedModel)
		if err != nil {
			return "", ModelConfig{}, modelLease{}, err
		}
		settings, err := service.Store.ChatSettings(ctx)
		if err != nil {
			return "", ModelConfig{}, modelLease{}, err
		}
		entries := make([]assistantCatalogEntry, 0, len(candidates))
		for _, candidate := range candidates {
			entries = append(entries, candidate.entry)
		}
		if settings.Embedding.TableEnabled {
			entries, err = selectAssistantCatalogSources(ctx, service.Embedding, question, entries,
				settings.Embedding.DatasourceCount)
			if err != nil {
				return "", ModelConfig{}, modelLease{}, err
			}
		}
		if len(entries) == 1 {
			selected = entries[0].CatalogID
		} else {
			selector, ok := service.Model.(assistantCatalogSelector)
			if !ok {
				return "", ModelConfig{}, modelLease{}, ErrModel
			}
			selected, err = selector.SelectAssistantCatalogSource(ctx, model, question, entries)
			if err != nil {
				return "", ModelConfig{}, modelLease{}, err
			}
			found := false
			for _, entry := range entries {
				if entry.CatalogID == selected {
					found = true
					break
				}
			}
			if !found {
				return "", ModelConfig{}, modelLease{}, ErrModel
			}
		}
		if err := service.Store.modelLeaseUnchanged(ctx, p.Workspace, model, lease); err != nil {
			return "", ModelConfig{}, modelLease{}, err
		}
	}
	// A source removed from the assistant or a binding changed during local
	// inference prevents any generated choice from reaching a query.
	for _, candidate := range candidates {
		if err := service.bindingUnchanged(ctx, p, candidate.binding, candidate.revision, candidate.digest); err != nil {
			return "", ModelConfig{}, modelLease{}, err
		}
	}
	return selected, model, lease, nil
}
