package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cuipengdba/agentsql/internal/audit"
	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	"github.com/cuipengdba/agentsql/internal/store"
)

type columnAuthorizationController struct {
	fence      *store.FenceRepository
	redactors  *redactorBuilder
	instanceID string
	runtime    *b2Runtime
}

func (controller *columnAuthorizationController) Begin(ctx context.Context, agent model.Agent, datasource model.Datasource) (pipeline.ColumnAuthorizationSnapshot, error) {
	if controller == nil || controller.fence == nil || controller.redactors == nil || controller.instanceID == "" ||
		controller.runtime == nil || !controller.runtime.allow(datasource) {
		return nil, fmt.Errorf("column authorization controller is unavailable")
	}
	snapshot, err := controller.fence.BeginRead(ctx, 3, controller.instanceID, time.Now())
	if err != nil {
		return nil, err
	}
	state, err := snapshot.ColumnAuthorizationState(ctx, agent.ID, datasource.ID)
	if err != nil {
		_ = snapshot.Close()
		return nil, err
	}
	redactor, err := controller.redactors.compile(state.MaskRules, datasource.ID)
	if err != nil {
		_ = snapshot.Close()
		return nil, err
	}
	return &columnAuthorizationSnapshot{snapshot: snapshot, policies: state.Policies, redactor: redactor,
		revisionDigest: state.RevisionDigest, runtime: controller.runtime, datasource: datasource}, nil
}

func (controller *columnAuthorizationController) ColumnAuthorizationEnabled(datasource model.Datasource) bool {
	return controller != nil && controller.runtime != nil && controller.runtime.route(datasource)
}

type columnAuthorizationSnapshot struct {
	snapshot       *store.ControlSnapshot
	policies       []model.Policy
	redactor       mask.Redactor
	revisionDigest string
	runtime        *b2Runtime
	datasource     model.Datasource
}

func (snapshot *columnAuthorizationSnapshot) Policies() []model.Policy {
	return append([]model.Policy(nil), snapshot.policies...)
}
func (snapshot *columnAuthorizationSnapshot) Redactor() mask.Redactor { return snapshot.redactor }
func (snapshot *columnAuthorizationSnapshot) RevisionDigest() string  { return snapshot.revisionDigest }
func (snapshot *columnAuthorizationSnapshot) FinalCheck(ctx context.Context) error {
	if snapshot.runtime == nil || !snapshot.runtime.allow(snapshot.datasource) {
		return store.ErrFenceLost
	}
	return snapshot.snapshot.FinalCheck(ctx, time.Now())
}
func (snapshot *columnAuthorizationSnapshot) Close() error { return snapshot.snapshot.Close() }

type controlledReadColumnAuthorizationProvider struct {
	controller *columnAuthorizationController
	recorder   audit.Recorder
}

func (provider *controlledReadColumnAuthorizationProvider) ColumnAuthorizationEnabled(datasource model.Datasource) bool {
	return provider != nil && provider.controller != nil && provider.controller.ColumnAuthorizationEnabled(datasource)
}

func (provider *controlledReadColumnAuthorizationProvider) BeginColumnAuthorization(ctx context.Context, actorID string, datasource model.Datasource) (executor.ColumnAuthorizationRequest, func() error, error) {
	if provider == nil || provider.controller == nil || provider.recorder == nil || actorID == "" {
		return executor.ColumnAuthorizationRequest{}, nil, fmt.Errorf("controlledread column authorization is unavailable")
	}
	agent := model.Agent{ID: actorID, Name: actorID, Status: "active", Level: "readonly"}
	snapshot, err := provider.controller.Begin(ctx, agent, datasource)
	if err != nil {
		return executor.ColumnAuthorizationRequest{}, nil, err
	}
	request := executor.ColumnAuthorizationRequest{
		Agent: agent, Policies: snapshot.Policies(), Redactor: snapshot.Redactor(),
		RowLimit: datasource.RowLimit, PreliminaryAllowed: true, ControlRevisionDigest: snapshot.RevisionDigest(),
		DurableAudit: func(auditContext context.Context, detail executor.ColumnAuthorizationAudit, candidate *model.QueryResult, _ mask.RedactReport) error {
			encoded, marshalErr := json.Marshal(struct {
				ColumnAuthorization executor.ColumnAuthorizationAudit `json:"column_auth"`
			}{ColumnAuthorization: detail})
			if marshalErr != nil {
				return marshalErr
			}
			decision := detail.Decision
			action, actorType, statementType, databaseType := "controlledread_column_authorization", "admin", "SELECT", datasource.DBType
			datasourceID, actor := datasource.ID, actorID
			text := string(encoded)
			log := model.AuditLog{AgentID: &actor, DatasourceID: &datasourceID, DBType: &databaseType,
				StmtType: &statementType, Decision: decision, Action: &action, ActorType: &actorType, ActorID: &actor, DetailsJSON: &text}
			if candidate != nil {
				rows := len(candidate.Rows)
				log.RowsReturned = &rows
			}
			_, recordErr := provider.recorder.Record(auditContext, log)
			return recordErr
		},
		FinalFence: snapshot.FinalCheck,
	}
	return request, snapshot.Close, nil
}

var _ executor.ColumnAuthorizationProvider = (*controlledReadColumnAuthorizationProvider)(nil)
