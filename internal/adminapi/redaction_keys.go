package adminapi

import (
	"net/http"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
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
	Status string `json:"status"`
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
	for _, version := range registered {
		response.Registered = append(response.Registered, redactionKeyVersionView(version))
	}
	handler.ok(writer, response)
}

func redactionKeyVersionView(version model.RedactionKeyVersion) redactionKeyVersionDTO {
	return redactionKeyVersionDTO{
		ID: version.ID, State: version.State, Commitment: version.Commitment,
		Label: version.Label, ConfigRevision: version.ConfigRevision,
		CreatedAt: version.CreatedAt, UpdatedAt: version.UpdatedAt,
		ActivatedAt: version.ActivatedAt, RetiredAt: version.RetiredAt,
	}
}
