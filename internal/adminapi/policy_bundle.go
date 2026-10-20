package adminapi

import (
	"errors"
	"net/http"

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
	Replay        []policybundle.ReplayResult `json:"replay"`
	LiveCheck     bool                        `json:"live_check"`
	Notice        string                      `json:"notice"`
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
		BaseDigest: snapshot.Digest, DesiredDigest: digest, Changes: changes, Replay: replay,
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
