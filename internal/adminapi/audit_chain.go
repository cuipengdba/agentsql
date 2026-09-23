package adminapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/cuipengdba/agentsql/internal/store"
)

const (
	chainDomainAuto       = "auto"
	chainDomainManagement = "management"
	chainDomainTraffic    = "traffic"

	chainVerifyInterval = 5 * time.Minute
)

// ChainManifestProvider resolves trusted, database-external chain authority.
type ChainManifestProvider = store.ChainManifestProvider

type keylessChainManifest = store.KeylessChainManifest

// NewKeylessChainManifestProvider returns the explicit version-zero manifest
// used by installations that have not configured an HMAC chain key.
func NewKeylessChainManifestProvider() ChainManifestProvider {
	return store.NewKeylessChainManifestProvider()
}

type auditChainVerificationView struct {
	Result              *string    `json:"result"`
	LastVerifiedAt      *time.Time `json:"last_verified_at"`
	LastVerifiedHeadSeq *int64     `json:"last_verified_head_seq"`
	BreakSeq            *int64     `json:"break_seq"`
	BreakID             *int64     `json:"break_id"`
	BreakReason         *string    `json:"break_reason"`
}

type auditChainStatusView struct {
	ChainID          string                     `json:"chain_id"`
	Status           string                     `json:"status"`
	Mode             *string                    `json:"mode"`
	HeadSeq          int64                      `json:"head_seq"`
	HeadID           *int64                     `json:"head_id"`
	ProtectedSince   *int64                     `json:"protected_since"`
	GenesisAt        *time.Time                 `json:"genesis_at"`
	ObservedInstance *string                    `json:"observed_instance"`
	Verification     auditChainVerificationView `json:"verification"`
}

type auditChainBreakView struct {
	Seq    int64  `json:"seq"`
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
}

type auditChainVerifyView struct {
	Result    string               `json:"result"`
	HeadSeq   int64                `json:"head_seq"`
	Total     int64                `json:"total"`
	Unchained int64                `json:"unchained"`
	Break     *auditChainBreakView `json:"break"`
}

type auditChainErrorView struct {
	ErrorCode         string `json:"error_code"`
	RetryAfterSeconds int64  `json:"retry_after_seconds,omitempty"`
}

func (handler *Handler) auditChainStatus(writer http.ResponseWriter, request *http.Request) {
	domain, access, ok := handler.auditChainAccess(writer, request)
	if !ok {
		return
	}
	state, err := access.State().Get(request.Context(), domain)
	if errors.Is(err, store.ErrNotFound) {
		handler.auditChainFailure(writer, http.StatusNotFound, "CHAIN_STATE_NOT_FOUND", "chain_state for domain \""+domain+"\" was not found", 0)
		return
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	verification, err := access.State().GetVerification(request.Context(), domain)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, auditChainStatusView{
		ChainID: state.ChainID, Status: state.Status, Mode: state.Mode, HeadSeq: state.HeadSeq,
		HeadID: state.HeadID, ProtectedSince: state.ProtectedSinceID, GenesisAt: state.GenesisAt,
		ObservedInstance: verification.ObservedInstanceID,
		Verification: auditChainVerificationView{
			Result: verification.Result, LastVerifiedAt: verification.LastVerifiedAt,
			LastVerifiedHeadSeq: verification.LastVerifiedHeadSeq, BreakSeq: verification.BreakSeq,
			BreakID: verification.BreakID, BreakReason: verification.BreakReason,
		},
	})
}

func (handler *Handler) auditChainVerify(writer http.ResponseWriter, request *http.Request) {
	domain, access, ok := handler.auditChainAccess(writer, request)
	if !ok {
		return
	}
	if !handler.beginChainVerification(writer, domain) {
		return
	}
	defer handler.endChainVerification(domain)

	provider := handler.deps.ChainManifests
	if provider == nil {
		handler.auditChainFailure(writer, http.StatusServiceUnavailable, "CHAIN_VERIFIER_UNAVAILABLE", "chain verifier is unavailable", 0)
		return
	}
	manifest, err := provider.ChainManifestForDomain(request.Context(), domain)
	if err != nil || manifest == nil {
		handler.auditChainFailure(writer, http.StatusServiceUnavailable, "CHAIN_VERIFIER_UNAVAILABLE", "chain verifier is unavailable", 0)
		return
	}
	if retryAfter := handler.startChainVerification(domain, time.Now()); retryAfter > 0 {
		handler.auditChainFailure(writer, http.StatusTooManyRequests, "CHAIN_VERIFY_RATE_LIMITED", "chain verification is rate limited", retryAfter)
		return
	}

	outcome, verifyErr := access.Verifier(manifest).VerifyAndPersist(request.Context())
	if verifyErr != nil && !auditChainReasonError(outcome, verifyErr) {
		handler.logger.Error().Str("error_type", "audit_chain_verification").Msg("admin audit chain verification failed")
		handler.auditChainFailure(writer, http.StatusInternalServerError, "CHAIN_VERIFY_FAILED", "chain verification failed", 0)
		return
	}
	view := auditChainVerifyView{
		Result: outcome.Result, HeadSeq: outcome.HeadSeq, Total: outcome.TotalRows, Unchained: outcome.Unchained,
	}
	if outcome.BreakReason != "" {
		view.Break = &auditChainBreakView{Seq: outcome.BreakSeq, ID: outcome.BreakID, Reason: outcome.BreakReason}
	}
	handler.ok(writer, view)
}

func (handler *Handler) auditChainAccess(writer http.ResponseWriter, request *http.Request) (string, *store.ChainAccess, bool) {
	domain := request.URL.Query().Get("domain")
	if domain == "" {
		domain = chainDomainAuto
	}
	if domain != chainDomainAuto && domain != chainDomainManagement && domain != chainDomainTraffic {
		handler.auditChainFailure(writer, http.StatusBadRequest, "CHAIN_DOMAIN_INVALID", "domain must be auto, management, or traffic", 0)
		return "", nil, false
	}
	if domain == chainDomainAuto {
		domain = chainDomainManagement
		if handler.deps.Config.Store.Audit != nil && handler.deps.Config.Store.Audit.Separate {
			domain = chainDomainTraffic
		}
	}
	access, err := handler.deps.Runtime.Store.Chain(domain)
	if err != nil {
		handler.internal(writer, err)
		return "", nil, false
	}
	return domain, access, true
}

func (handler *Handler) beginChainVerification(writer http.ResponseWriter, domain string) bool {
	handler.chainVerifyMu.Lock()
	if handler.chainVerifying[domain] {
		handler.chainVerifyMu.Unlock()
		handler.auditChainFailure(writer, http.StatusConflict, "CHAIN_VERIFY_IN_PROGRESS", "chain verification is already in progress", 0)
		return false
	}
	handler.chainVerifying[domain] = true
	handler.chainVerifyMu.Unlock()
	return true
}

func (handler *Handler) startChainVerification(domain string, now time.Time) int64 {
	handler.chainVerifyMu.Lock()
	defer handler.chainVerifyMu.Unlock()
	last := handler.chainLastStart[domain]
	if !last.IsZero() && now.Sub(last) < chainVerifyInterval {
		remaining := chainVerifyInterval - now.Sub(last)
		seconds := int64((remaining + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		return seconds
	}
	handler.chainLastStart[domain] = now
	return 0
}

func (handler *Handler) endChainVerification(domain string) {
	handler.chainVerifyMu.Lock()
	delete(handler.chainVerifying, domain)
	handler.chainVerifyMu.Unlock()
}

func auditChainReasonError(outcome store.ChainVerificationOutcome, verifyErr error) bool {
	if verifyErr == nil || outcome.Valid || outcome.BreakReason == "" || outcome.BreakReason == "runtime_error" {
		return false
	}
	exitCode := store.ExitCodeForVerification(outcome, verifyErr)
	return exitCode == 2 || exitCode == 3
}

func (handler *Handler) auditChainFailure(writer http.ResponseWriter, status int, code, message string, retryAfter int64) {
	handler.write(writer, status, status, message, auditChainErrorView{ErrorCode: code, RetryAfterSeconds: retryAfter})
}
