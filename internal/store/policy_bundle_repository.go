package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/policy"
	"github.com/cuipengdba/agentsql/internal/policybundle"
	"github.com/google/uuid"
)

var (
	ErrPolicyBundleStale          = errors.New("policy bundle source changed")
	ErrPolicyBundleInvalid        = errors.New("invalid policy bundle")
	ErrPolicyBundleReviewRequired = errors.New("a different reviewer is required")
	ErrPolicyBundleState          = errors.New("policy bundle proposal state changed")
)

const maxPolicyBundleChanges = 32

type PolicyBundleProposal struct {
	ID             string              `json:"id"`
	TenantID       string              `json:"tenant_id"`
	BaseDigest     string              `json:"base_digest"`
	DesiredDigest  string              `json:"desired_digest"`
	Desired        policybundle.Bundle `json:"desired"`
	AuthorUserID   string              `json:"author_user_id"`
	ReviewerUserID *string             `json:"reviewer_user_id,omitempty"`
	Status         string              `json:"status"`
	CreatedAt      time.Time           `json:"created_at"`
	ReviewedAt     *time.Time          `json:"reviewed_at,omitempty"`
	AppliedAt      *time.Time          `json:"applied_at,omitempty"`
}

type PolicyBundleStatus struct {
	CurrentDigest  string `json:"current_digest"`
	ApprovedDigest string `json:"approved_digest,omitempty"`
	ProposalID     string `json:"proposal_id,omitempty"`
	State          string `json:"state"`
}

type PolicyBundleRepository struct {
	repositoryBase
	policies *PolicyRepository
	outbox   *ManagementAuditOutboxRepository
}

func (repository *PolicyBundleRepository) Current(ctx context.Context) (policybundle.Bundle, string, error) {
	options := &sql.TxOptions{ReadOnly: true}
	if repository.dialect == DialectPostgres {
		options.Isolation = sql.LevelRepeatableRead
	}
	tx, err := repository.db.BeginTx(ctx, options)
	if err != nil {
		return policybundle.Bundle{}, "", err
	}
	defer tx.Rollback()
	bundle, digest, err := repository.snapshot(ctx, tx)
	if err != nil {
		return policybundle.Bundle{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return policybundle.Bundle{}, "", err
	}
	return bundle, digest, nil
}

func (store *Store) PolicyBundles() *PolicyBundleRepository {
	return &PolicyBundleRepository{
		repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver},
		policies:       store.Policies(), outbox: store.Outbox(),
	}
}

func (repository *PolicyBundleRepository) snapshot(ctx context.Context, queryer policyQueryer) (policybundle.Bundle, string, error) {
	tenantID, err := repository.requireTenant(ctx, "policy bundle snapshot")
	if err != nil {
		return policybundle.Bundle{}, "", err
	}
	rows, err := repository.policies.listWith(ctx, queryer, " ORDER BY id ASC")
	if err != nil {
		return policybundle.Bundle{}, "", err
	}
	bundle := policybundle.FromModels(tenantID, rows)
	canonical, err := policybundle.Canonical(bundle)
	if err != nil {
		return policybundle.Bundle{}, "", err
	}
	var normalized policybundle.Bundle
	if err := json.Unmarshal(canonical, &normalized); err != nil {
		return policybundle.Bundle{}, "", err
	}
	bundle = normalized
	digest, err := policybundle.Digest(bundle)
	return bundle, digest, err
}

// Create stores the full intended snapshot while holding the same global
// policy fence as ordinary policy mutations. No business SQL is executed.
func (repository *PolicyBundleRepository) Create(ctx context.Context, desired policybundle.Bundle, baseDigest, authorUserID string) (PolicyBundleProposal, error) {
	tenantID, err := repository.requireTenant(ctx, "create policy bundle proposal")
	if err != nil {
		return PolicyBundleProposal{}, err
	}
	if authorUserID == "" || desired.TenantID != tenantID {
		return PolicyBundleProposal{}, ErrPolicyBundleInvalid
	}
	canonical, err := policybundle.Canonical(desired)
	if err != nil {
		return PolicyBundleProposal{}, errors.Join(ErrPolicyBundleInvalid, err)
	}
	var normalized policybundle.Bundle
	if err := json.Unmarshal(canonical, &normalized); err != nil {
		return PolicyBundleProposal{}, err
	}
	desired = normalized
	tx, err := repository.policies.beginMutation(ctx)
	if err != nil {
		return PolicyBundleProposal{}, err
	}
	defer tx.Rollback()
	current, currentDigest, err := repository.snapshot(ctx, tx)
	if err != nil {
		return PolicyBundleProposal{}, err
	}
	if currentDigest != baseDigest {
		return PolicyBundleProposal{}, ErrPolicyBundleStale
	}
	changes, err := policybundle.Diff(current, desired)
	if err != nil || len(changes) > maxPolicyBundleChanges {
		return PolicyBundleProposal{}, ErrPolicyBundleInvalid
	}
	if err := repository.validateChanges(ctx, tx, current, desired, changes); err != nil {
		return PolicyBundleProposal{}, err
	}
	desiredDigest, err := policybundle.Digest(desired)
	if err != nil {
		return PolicyBundleProposal{}, err
	}
	id := uuid.NewString()
	_, err = tx.ExecContext(ctx, repository.bind(`INSERT INTO policy_bundle_proposals(id,tenant_id,base_digest,desired_digest,desired_json,author_user_id,status) VALUES(?,?,?,?,?,?,'pending')`),
		id, tenantID, baseDigest, desiredDigest, string(canonical), authorUserID)
	if err != nil {
		return PolicyBundleProposal{}, fmt.Errorf("create policy bundle proposal: %w", err)
	}
	if err := repository.appendAudit(ctx, tx, id, "policy_bundle.propose", authorUserID, baseDigest, desiredDigest, len(changes)); err != nil {
		return PolicyBundleProposal{}, err
	}
	if err := tx.Commit(); err != nil {
		return PolicyBundleProposal{}, err
	}
	return repository.Get(ctx, id)
}

func (repository *PolicyBundleRepository) Get(ctx context.Context, id string) (PolicyBundleProposal, error) {
	tenantID, err := repository.requireTenant(ctx, "get policy bundle proposal")
	if err != nil {
		return PolicyBundleProposal{}, err
	}
	return repository.get(ctx, repository.db, tenantID, id, false)
}

func (repository *PolicyBundleRepository) get(ctx context.Context, queryer sqlExecutor, tenantID, id string, lock bool) (PolicyBundleProposal, error) {
	query := `SELECT id,tenant_id,base_digest,desired_digest,desired_json,author_user_id,reviewer_user_id,status,created_at,reviewed_at,applied_at FROM policy_bundle_proposals WHERE id=? AND tenant_id=?`
	if lock && repository.dialect == DialectPostgres {
		query += ` FOR UPDATE`
	}
	var proposal PolicyBundleProposal
	var desired string
	var reviewer sql.NullString
	var reviewed, applied sql.NullTime
	err := queryer.QueryRowContext(ctx, repository.bind(query), id, tenantID).Scan(
		&proposal.ID, &proposal.TenantID, &proposal.BaseDigest, &proposal.DesiredDigest,
		&desired, &proposal.AuthorUserID, &reviewer, &proposal.Status,
		&proposal.CreatedAt, &reviewed, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		return PolicyBundleProposal{}, ErrNotFound
	}
	if err != nil {
		return PolicyBundleProposal{}, err
	}
	if err := json.Unmarshal([]byte(desired), &proposal.Desired); err != nil {
		return PolicyBundleProposal{}, err
	}
	if reviewer.Valid {
		proposal.ReviewerUserID = &reviewer.String
	}
	if reviewed.Valid {
		proposal.ReviewedAt = &reviewed.Time
	}
	if applied.Valid {
		proposal.AppliedAt = &applied.Time
	}
	return proposal, nil
}

func (repository *PolicyBundleRepository) Review(ctx context.Context, id, reviewerUserID string, approve bool) (PolicyBundleProposal, error) {
	tenantID, err := repository.requireTenant(ctx, "review policy bundle proposal")
	if err != nil {
		return PolicyBundleProposal{}, err
	}
	if reviewerUserID == "" {
		return PolicyBundleProposal{}, ErrPolicyBundleReviewRequired
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return PolicyBundleProposal{}, err
	}
	defer tx.Rollback()
	proposal, err := repository.get(ctx, tx, tenantID, id, true)
	if err != nil {
		return PolicyBundleProposal{}, err
	}
	if proposal.AuthorUserID == reviewerUserID {
		return PolicyBundleProposal{}, ErrPolicyBundleReviewRequired
	}
	if proposal.Status != "pending" {
		return PolicyBundleProposal{}, ErrPolicyBundleState
	}
	status, action := "rejected", "policy_bundle.reject"
	if approve {
		status, action = "approved", "policy_bundle.approve"
	}
	result, err := tx.ExecContext(ctx, repository.bind(`UPDATE policy_bundle_proposals SET status=?,reviewer_user_id=?,reviewed_at=CURRENT_TIMESTAMP WHERE id=? AND tenant_id=? AND status='pending'`), status, reviewerUserID, id, tenantID)
	if err != nil {
		return PolicyBundleProposal{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return PolicyBundleProposal{}, ErrPolicyBundleState
	}
	if err := repository.appendAudit(ctx, tx, id, action, reviewerUserID, proposal.BaseDigest, proposal.DesiredDigest, 0); err != nil {
		return PolicyBundleProposal{}, err
	}
	if err := tx.Commit(); err != nil {
		return PolicyBundleProposal{}, err
	}
	return repository.Get(ctx, id)
}

func (repository *PolicyBundleRepository) Apply(ctx context.Context, id, operatorUserID string) (PolicyBundleStatus, error) {
	tenantID, err := repository.requireTenant(ctx, "apply policy bundle proposal")
	if err != nil {
		return PolicyBundleStatus{}, err
	}
	tx, err := repository.policies.beginMutation(ctx)
	if err != nil {
		return PolicyBundleStatus{}, err
	}
	defer tx.Rollback()
	proposal, err := repository.get(ctx, tx, tenantID, id, true)
	if err != nil {
		return PolicyBundleStatus{}, err
	}
	verifiedDigest, err := policybundle.Digest(proposal.Desired)
	if err != nil || verifiedDigest != proposal.DesiredDigest {
		return PolicyBundleStatus{}, ErrPolicyBundleInvalid
	}
	if proposal.Status != "approved" || proposal.ReviewerUserID == nil || operatorUserID != *proposal.ReviewerUserID || operatorUserID == proposal.AuthorUserID {
		return PolicyBundleStatus{}, ErrPolicyBundleReviewRequired
	}
	current, currentDigest, err := repository.snapshot(ctx, tx)
	if err != nil {
		return PolicyBundleStatus{}, err
	}
	if currentDigest != proposal.BaseDigest {
		return PolicyBundleStatus{}, ErrPolicyBundleStale
	}
	changes, err := policybundle.Diff(current, proposal.Desired)
	if err != nil || len(changes) > maxPolicyBundleChanges {
		return PolicyBundleStatus{}, ErrPolicyBundleInvalid
	}
	if err := repository.validateChanges(ctx, tx, current, proposal.Desired, changes); err != nil {
		return PolicyBundleStatus{}, err
	}
	if err := repository.applyChanges(ctx, tx, current, proposal.Desired, changes); err != nil {
		return PolicyBundleStatus{}, err
	}
	_, approvedDigest, err := repository.snapshot(ctx, tx)
	if err != nil {
		return PolicyBundleStatus{}, err
	}
	_, err = tx.ExecContext(ctx, repository.bind(`INSERT INTO policy_bundle_baselines(tenant_id,digest,proposal_id,updated_at) VALUES(?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(tenant_id) DO UPDATE SET digest=excluded.digest,proposal_id=excluded.proposal_id,updated_at=CURRENT_TIMESTAMP`), tenantID, approvedDigest, id)
	if err != nil {
		return PolicyBundleStatus{}, err
	}
	result, err := tx.ExecContext(ctx, repository.bind(`UPDATE policy_bundle_proposals SET status='applied',applied_at=CURRENT_TIMESTAMP WHERE id=? AND tenant_id=? AND status='approved'`), id, tenantID)
	if err != nil {
		return PolicyBundleStatus{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return PolicyBundleStatus{}, ErrPolicyBundleState
	}
	if err := repository.appendAudit(ctx, tx, id, "policy_bundle.apply", operatorUserID, currentDigest, approvedDigest, len(changes)); err != nil {
		return PolicyBundleStatus{}, err
	}
	if err := tx.Commit(); err != nil {
		return PolicyBundleStatus{}, err
	}
	return PolicyBundleStatus{CurrentDigest: approvedDigest, ApprovedDigest: approvedDigest, ProposalID: id, State: "approved"}, nil
}

func (repository *PolicyBundleRepository) Status(ctx context.Context) (PolicyBundleStatus, error) {
	tenantID, err := repository.requireTenant(ctx, "policy bundle status")
	if err != nil {
		return PolicyBundleStatus{}, err
	}
	_, currentDigest, err := repository.Current(ctx)
	if err != nil {
		return PolicyBundleStatus{}, err
	}
	status := PolicyBundleStatus{CurrentDigest: currentDigest, State: "uninitialized"}
	err = repository.db.QueryRowContext(ctx, repository.bind(`SELECT digest,proposal_id FROM policy_bundle_baselines WHERE tenant_id=?`), tenantID).Scan(&status.ApprovedDigest, &status.ProposalID)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return PolicyBundleStatus{}, err
	}
	status.State = "approved"
	if status.CurrentDigest != status.ApprovedDigest {
		status.State = "drifted"
	}
	return status, nil
}

func (repository *PolicyBundleRepository) validateChanges(ctx context.Context, tx *sql.Tx, current, desired policybundle.Bundle, changes []policybundle.Change) error {
	before, after := make(map[string]policybundle.Entry, len(current.Policies)), make(map[string]policybundle.Entry, len(desired.Policies))
	for _, v := range current.Policies {
		before[v.ID] = v
	}
	for _, v := range desired.Policies {
		after[v.ID] = v
	}
	tenantID := current.TenantID
	for _, change := range changes {
		if old, ok := before[change.ID]; ok && (old.RelationBindingID != nil || old.RelationBinding != nil || len(old.ColumnPermissions) != 0 || len(old.ColumnStaging) != 0 || old.ObjectType == "column" || old.Columns != nil || old.RowFilter != nil || old.LegacyUnrepresentable) {
			return ErrPolicyBundleInvalid
		}
		if change.Operation == "delete" {
			continue
		}
		v := after[change.ID]
		if v.ObjectType != "table" || v.RelationBindingID != nil || v.RelationBinding != nil || len(v.ColumnPermissions) != 0 || len(v.ColumnStaging) != 0 || v.Columns != nil || v.RowFilter != nil || v.LegacyUnrepresentable || (v.Action != "allow" && v.Action != "deny") {
			return ErrPolicyBundleInvalid
		}
		if _, err := policy.NewResolver().Resolve([]model.Policy{{ObjectType: v.ObjectType, ObjectName: v.ObjectName, Action: v.Action}}, "readonly"); err != nil {
			return ErrPolicyBundleInvalid
		}
		if change.Operation == "create" && v.Revision != 0 || change.Operation == "update" && v.Revision != before[v.ID].Revision {
			return ErrPolicyBundleStale
		}
		for _, tableAndID := range [][2]string{{"agents", v.AgentID}, {"datasources", v.DatasourceID}} {
			var count int
			query := `SELECT COUNT(*) FROM ` + tableAndID[0] + ` WHERE id=? AND tenant_id=?`
			if err := tx.QueryRowContext(ctx, repository.bind(query), tableAndID[1], tenantID).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return ErrPolicyBundleInvalid
			}
		}
	}
	return nil
}

func (repository *PolicyBundleRepository) applyChanges(ctx context.Context, tx *sql.Tx, current, desired policybundle.Bundle, changes []policybundle.Change) error {
	before, after := make(map[string]policybundle.Entry, len(current.Policies)), make(map[string]policybundle.Entry, len(desired.Policies))
	for _, v := range current.Policies {
		before[v.ID] = v
	}
	for _, v := range desired.Policies {
		after[v.ID] = v
	}
	for _, change := range changes {
		v := after[change.ID]
		switch change.Operation {
		case "create":
			_, err := tx.ExecContext(ctx, repository.bind(`INSERT INTO policies(id,tenant_id,agent_id,datasource_id,object_type,object_name,action,revision,legacy_unrepresentable) VALUES(?,?,?,?,?,?,?,1,FALSE)`), v.ID, current.TenantID, v.AgentID, v.DatasourceID, v.ObjectType, v.ObjectName, v.Action)
			if err != nil {
				return err
			}
		case "update":
			old := before[change.ID]
			result, err := tx.ExecContext(ctx, repository.bind(`UPDATE policies SET agent_id=?,datasource_id=?,object_type=?,object_name=?,action=?,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=? AND tenant_id=? AND revision=?`), v.AgentID, v.DatasourceID, v.ObjectType, v.ObjectName, v.Action, v.ID, current.TenantID, old.Revision)
			if err != nil {
				return err
			}
			if affected, _ := result.RowsAffected(); affected != 1 {
				return ErrPolicyBundleStale
			}
		case "delete":
			old := before[change.ID]
			result, err := tx.ExecContext(ctx, repository.bind(`DELETE FROM policies WHERE id=? AND tenant_id=? AND revision=?`), old.ID, current.TenantID, old.Revision)
			if err != nil {
				return err
			}
			if affected, _ := result.RowsAffected(); affected != 1 {
				return ErrPolicyBundleStale
			}
		default:
			return ErrPolicyBundleInvalid
		}
	}
	return nil
}

func (repository *PolicyBundleRepository) appendAudit(ctx context.Context, tx *sql.Tx, id, action, actorID, before, after string, count int) error {
	details, err := json.Marshal(struct {
		ProposalID string `json:"proposal_id"`
		Before     string `json:"before_digest"`
		After      string `json:"after_digest"`
		Changes    int    `json:"change_count"`
	}{id, before, after, count})
	if err != nil {
		return err
	}
	return repository.outbox.Append(ctx, tx, model.ManagementAuditOutbox{
		EventUUID: uuid.NewString(), Action: action, ActorType: "admin", ActorID: actorID, DetailsJSON: string(details),
	})
}
