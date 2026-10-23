package adminapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/cuipengdba/agentsql/internal/policybundle"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
)

var errPolicyBundlePrincipal = errors.New("policy bundle principal unavailable")

type policyBundleSnapshot struct {
	Bundle policybundle.Bundle `json:"bundle"`
	Digest string              `json:"digest"`
}

type policyBundlePlanInput struct {
	BaseDigest  string                    `json:"base_digest"`
	Desired     policybundle.Bundle       `json:"desired"`
	ReplayCases []policybundle.ReplayCase `json:"replay_cases,omitempty"`
}

type policyBundlePlanView struct {
	BaseDigest    string                      `json:"base_digest"`
	DesiredDigest string                      `json:"desired_digest"`
	Changes       []policybundle.Change       `json:"changes"`
	Affected      []policyBundleAffected      `json:"affected"`
	Replay        []policybundle.ReplayResult `json:"replay"`
	LiveCheck     bool                        `json:"live_check"`
	Notice        string                      `json:"notice"`
}

// Affected is a metadata-only scope. It deliberately does not predict which
// queries will pass the live database binder, masking or audit checks.
type policyBundleAffected struct {
	PolicyID      string   `json:"policy_id"`
	AgentID       string   `json:"agent_id"`
	DatasourceID  string   `json:"datasource_id"`
	BeforeObject  string   `json:"before_object,omitempty"`
	AfterObject   string   `json:"after_object,omitempty"`
	BeforeColumns []string `json:"before_columns"`
	AfterColumns  []string `json:"after_columns"`
	Uncertain     bool     `json:"uncertain"`
}

func policyBundleAffectedScopes(current, desired policybundle.Bundle, changes []policybundle.Change) []policyBundleAffected {
	before := make(map[string]policybundle.Entry, len(current.Policies))
	after := make(map[string]policybundle.Entry, len(desired.Policies))
	for _, entry := range current.Policies {
		before[entry.ID] = entry
	}
	for _, entry := range desired.Policies {
		after[entry.ID] = entry
	}
	result := make([]policyBundleAffected, 0, len(changes))
	for _, change := range changes {
		old, hadOld := before[change.ID]
		next, hasNext := after[change.ID]
		item := policyBundleAffected{PolicyID: change.ID, BeforeColumns: []string{}, AfterColumns: []string{}, Uncertain: true}
		if hadOld {
			item.AgentID, item.DatasourceID = old.AgentID, old.DatasourceID
			item.BeforeObject = old.ObjectName
			if old.Columns != nil {
				item.BeforeColumns = splitPolicyColumns(*old.Columns)
			}
		}
		if hasNext {
			item.AgentID, item.DatasourceID = next.AgentID, next.DatasourceID
			item.AfterObject = next.ObjectName
			if next.Columns != nil {
				item.AfterColumns = splitPolicyColumns(*next.Columns)
			}
		}
		result = append(result, item)
	}
	return result
}

func splitPolicyColumns(value string) []string {
	result := make([]string, 0)
	for _, column := range strings.Split(value, ",") {
		if column = strings.TrimSpace(column); column != "" {
			result = append(result, column)
		}
	}
	return result
}

func (handler *Handler) currentPolicyBundle(request *http.Request) (policyBundleSnapshot, error) {
	principal, ok := requestPrincipal(request)
	if !ok {
		return policyBundleSnapshot{}, errPolicyBundlePrincipal
	}
	bundle, digest, err := handler.deps.Runtime.Store.PolicyBundles().Current(request.Context())
	if err != nil {
		return policyBundleSnapshot{}, err
	}
	if bundle.TenantID != principal.TenantID {
		return policyBundleSnapshot{}, errPolicyBundlePrincipal
	}
	return policyBundleSnapshot{Bundle: bundle, Digest: digest}, nil
}

func (handler *Handler) policyBundlesCurrent(writer http.ResponseWriter, request *http.Request) {
	snapshot, err := handler.currentPolicyBundle(request)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	writer.Header().Set("ETag", `"`+snapshot.Digest+`"`)
	handler.ok(writer, snapshot)
}

func (handler *Handler) policyBundlesPlan(writer http.ResponseWriter, request *http.Request) {
	var input policyBundlePlanInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid policy bundle plan")
		return
	}
	snapshot, err := handler.currentPolicyBundle(request)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if input.Desired.TenantID != snapshot.Bundle.TenantID {
		handler.fail(writer, http.StatusForbidden, "policy bundle tenant mismatch")
		return
	}
	if input.BaseDigest != snapshot.Digest {
		handler.fail(writer, http.StatusPreconditionFailed, "policy bundle source changed")
		return
	}
	changes, err := policybundle.Diff(snapshot.Bundle, input.Desired)
	if err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid policy bundle")
		return
	}
	digest, err := policybundle.Digest(input.Desired)
	if err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid policy bundle")
		return
	}
	replay, err := policybundle.Replay(snapshot.Bundle, input.Desired, input.ReplayCases)
	if err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid policy replay cases")
		return
	}
	handler.ok(writer, policyBundlePlanView{
		BaseDigest: snapshot.Digest, DesiredDigest: digest, Changes: changes,
		Affected: policyBundleAffectedScopes(snapshot.Bundle, input.Desired, changes), Replay: replay,
		LiveCheck: true, Notice: "metadata-only plan; database permission, SQL parsing, audit and execution must be verified through the gateway",
	})
}

func (handler *Handler) policyBundlesStatus(writer http.ResponseWriter, request *http.Request) {
	status, err := handler.deps.Runtime.Store.PolicyBundles().Status(request.Context())
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, status)
}

func (handler *Handler) policyBundlesPropose(writer http.ResponseWriter, request *http.Request) {
	principal, ok := requestPrincipal(request)
	if !ok {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input policyBundlePlanInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid policy bundle proposal")
		return
	}
	proposal, err := handler.deps.Runtime.Store.PolicyBundles().Create(request.Context(), input.Desired, input.BaseDigest, principal.UserID)
	if err != nil {
		handler.policyBundleError(writer, err)
		return
	}
	handler.ok(writer, proposal)
}

func (handler *Handler) policyBundlesProposalGet(writer http.ResponseWriter, request *http.Request) {
	proposal, err := handler.deps.Runtime.Store.PolicyBundles().Get(request.Context(), request.PathValue("id"))
	if err != nil {
		handler.policyBundleError(writer, err)
		return
	}
	handler.ok(writer, proposal)
}

func (handler *Handler) policyBundlesApprove(writer http.ResponseWriter, request *http.Request) {
	handler.policyBundlesReview(writer, request, true)
}

func (handler *Handler) policyBundlesReject(writer http.ResponseWriter, request *http.Request) {
	handler.policyBundlesReview(writer, request, false)
}

func (handler *Handler) policyBundlesReview(writer http.ResponseWriter, request *http.Request, approve bool) {
	principal, ok := requestPrincipal(request)
	if !ok {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !principal.Has(rbac.PermissionAuditView) {
		handler.fail(writer, http.StatusForbidden, "forbidden")
		return
	}
	proposal, err := handler.deps.Runtime.Store.PolicyBundles().Review(request.Context(), request.PathValue("id"), principal.UserID, approve)
	if err != nil {
		handler.policyBundleError(writer, err)
		return
	}
	handler.ok(writer, proposal)
}

func (handler *Handler) policyBundlesApply(writer http.ResponseWriter, request *http.Request) {
	principal, ok := requestPrincipal(request)
	if !ok {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	status, err := handler.deps.Runtime.Store.PolicyBundles().Apply(request.Context(), request.PathValue("id"), principal.UserID)
	if err != nil {
		handler.policyBundleError(writer, err)
		return
	}
	handler.ok(writer, status)
}

func (handler *Handler) policyBundleError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		handler.notFound(writer)
	case errors.Is(err, store.ErrPolicyBundleStale):
		handler.fail(writer, http.StatusPreconditionFailed, "policy bundle source changed")
	case errors.Is(err, store.ErrPolicyBundleInvalid):
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid policy bundle")
	case errors.Is(err, store.ErrPolicyBundleReviewRequired):
		handler.fail(writer, http.StatusForbidden, "separate reviewer required")
	case errors.Is(err, store.ErrPolicyBundleState):
		handler.fail(writer, http.StatusConflict, "policy bundle proposal state changed")
	default:
		handler.internal(writer, err)
	}
}
