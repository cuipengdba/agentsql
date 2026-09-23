package adminapi

import (
	"net/http"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/redaction"
)

type redactionKeyRegistryResponse struct {
	Registered []redactionKeyVersionDTO `json:"registered"`
	Observed   redactionKeyObservedDTO  `json:"observed"`
}

type redactionKeyVersionDTO struct {
	ID             string     `json:"id"`
	State          string     `json:"state"`
	Commitment     string     `json:"commitment"`
	Label          string     `json:"label"`
	ConfigRevision string     `json:"config_revision"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	ActivatedAt    *time.Time `json:"activated_at"`
	RetiredAt      *time.Time `json:"retired_at"`
}

type redactionKeyObservedDTO struct {
	Status        string                    `json:"status"`
	Mode          string                    `json:"mode,omitempty"`
	ActiveVersion int                       `json:"active_version,omitempty"`
	Keys          []redactionKeyObservedKey `json:"keys,omitempty"`
	Revision      string                    `json:"revision,omitempty"`
	Ready         *bool                     `json:"ready,omitempty"`
	Drift         *redactionKeyDriftDTO     `json:"drift,omitempty"`
}

type redactionKeyObservedKey struct {
	ID         int    `json:"id"`
	Commitment string `json:"commitment"`
}

type redactionKeyDriftDTO struct {
	Unsatisfied []int                        `json:"unsatisfied"`
	Warnings    []redactionKeyDriftDetailDTO `json:"warnings"`
	Information []redactionKeyDriftDetailDTO `json:"information"`
}

type redactionKeyDriftDetailDTO struct {
	Number  int    `json:"number"`
	Kind    string `json:"kind"`
	Version int    `json:"version,omitempty"`
	Message string `json:"message"`
}

func (handler *Handler) redactionKeysList(writer http.ResponseWriter, request *http.Request) {
	registered, err := handler.deps.Runtime.Store.RedactionKeys().List(request.Context())
	if err != nil {
		handler.internal(writer, err)
		return
	}
	response := redactionKeyRegistryResponse{
		Registered: make([]redactionKeyVersionDTO, 0, len(registered)),
		Observed:   redactionKeyObservedDTO{Status: "unavailable"},
	}
	if reconciliation, available := handler.deps.Runtime.RedactionReconciliation(); available {
		response.Observed = redactionKeyObservedView(reconciliation)
	}
	for _, version := range registered {
		response.Registered = append(response.Registered, redactionKeyVersionView(version))
	}
	handler.ok(writer, response)
}

func redactionKeyObservedView(result redaction.Result) redactionKeyObservedDTO {
	ready := result.Ready
	view := redactionKeyObservedDTO{
		Status: result.Observed.Status, Mode: result.Observed.Mode,
		ActiveVersion: result.Observed.ActiveVersion, Revision: result.Observed.Revision,
		Ready: &ready,
		Drift: &redactionKeyDriftDTO{
			Unsatisfied: append([]int{}, result.Unsatisfied...),
			Warnings:    make([]redactionKeyDriftDetailDTO, 0, len(result.Warnings)),
			Information: make([]redactionKeyDriftDetailDTO, 0, len(result.Information)),
		},
	}
	view.Keys = make([]redactionKeyObservedKey, 0, len(result.Observed.Keys))
	for _, key := range result.Observed.Keys {
		view.Keys = append(view.Keys, redactionKeyObservedKey{ID: key.ID, Commitment: key.Commitment})
	}
	for _, detail := range result.Warnings {
		view.Drift.Warnings = append(view.Drift.Warnings, redactionKeyDriftDetailView(detail))
	}
	for _, detail := range result.Information {
		view.Drift.Information = append(view.Drift.Information, redactionKeyDriftDetailView(detail))
	}
	return view
}

func redactionKeyDriftDetailView(detail redaction.Detail) redactionKeyDriftDetailDTO {
	return redactionKeyDriftDetailDTO{
		Number: detail.Number, Kind: detail.Kind, Version: detail.Version, Message: detail.Message,
	}
}

func redactionKeyVersionView(version model.RedactionKeyVersion) redactionKeyVersionDTO {
	return redactionKeyVersionDTO{
		ID: version.ID, State: version.State, Commitment: version.Commitment,
		Label: version.Label, ConfigRevision: version.ConfigRevision,
		CreatedAt: version.CreatedAt, UpdatedAt: version.UpdatedAt,
		ActivatedAt: version.ActivatedAt, RetiredAt: version.RetiredAt,
	}
}
