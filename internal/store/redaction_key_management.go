package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
)

// RedactionKeyRegistration is safe registry metadata; it never contains key material.
type RedactionKeyRegistration struct {
	ID, Commitment, Label, ConfigRevision string
}

// ManagementAuditWrite selects exactly one atomic audit destination.
// Combined stores use Audit; separated stores use Outbox.
type ManagementAuditWrite struct {
	Audit  *model.AuditLog
	Outbox *model.ManagementAuditOutbox
}

// RegisterStandbysWithAudit atomically registers all supplied versions and
// persists the management audit in the same metadata transaction.
func (repository *RedactionKeyRepository) RegisterStandbysWithAudit(ctx context.Context, registrations []RedactionKeyRegistration, write ManagementAuditWrite) error {
	return repository.withRegistryTransaction(ctx, "register redaction key versions", func(tx *sql.Tx) error {
		for _, registration := range registrations {
			if err := repository.RegisterStandby(ctx, registration.ID, registration.Commitment, registration.Label, registration.ConfigRevision, tx); err != nil {
				return err
			}
		}
		return repository.writeManagementAudit(ctx, tx, write)
	})
}

// MarkActiveCASWithAudit atomically changes registry state and persists its audit.
func (repository *RedactionKeyRepository) MarkActiveCASWithAudit(ctx context.Context, id string, write func(before, after model.RedactionKeyVersion, outcome string) (ManagementAuditWrite, error)) (before, after model.RedactionKeyVersion, outcome string, err error) {
	outcome = RedactionActivationRolledBack
	if err = repository.validate(ctx, "mark active"); err != nil {
		return
	}
	if err = validateRedactionKeyID(id); err != nil {
		err = fmt.Errorf("mark redaction key active: %w", err)
		return
	}
	if repository.mu != nil {
		repository.mu.Lock()
		defer repository.mu.Unlock()
	}
	err = repository.withRegistryTransaction(ctx, "mark redaction key active", func(tx *sql.Tx) error {
		var mutationErr error
		before, after, outcome, mutationErr = repository.markActiveCASTx(ctx, tx, id)
		if mutationErr != nil {
			return mutationErr
		}
		auditWrite, buildErr := write(before, after, outcome)
		if buildErr != nil {
			return buildErr
		}
		return repository.writeManagementAudit(ctx, tx, auditWrite)
	})
	if err != nil {
		outcome = RedactionActivationRolledBack
	}
	return
}

// MarkRetiredWithAudit atomically retires a version and persists its audit.
func (repository *RedactionKeyRepository) MarkRetiredWithAudit(ctx context.Context, id string, write func(before, after model.RedactionKeyVersion) (ManagementAuditWrite, error)) (before, after model.RedactionKeyVersion, err error) {
	if err = repository.validate(ctx, "mark retired"); err != nil {
		return
	}
	if err = validateRedactionKeyID(id); err != nil {
		err = fmt.Errorf("retire redaction key version: %w", err)
		return
	}
	if repository.mu != nil {
		repository.mu.Lock()
		defer repository.mu.Unlock()
	}
	err = repository.withRegistryTransaction(ctx, "retire redaction key version", func(tx *sql.Tx) error {
		var mutationErr error
		before, after, mutationErr = repository.markRetiredTx(ctx, tx, id)
		if mutationErr != nil {
			return mutationErr
		}
		auditWrite, buildErr := write(before, after)
		if buildErr != nil {
			return buildErr
		}
		return repository.writeManagementAudit(ctx, tx, auditWrite)
	})
	return
}

func (repository *RedactionKeyRepository) withRegistryTransaction(ctx context.Context, operation string, apply func(*sql.Tx) error) (err error) {
	if err = repository.validate(ctx, operation); err != nil {
		return err
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: begin transaction: %w", operation, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = apply(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("%s: commit transaction: %w", operation, err)
	}
	return nil
}

func (repository *RedactionKeyRepository) writeManagementAudit(ctx context.Context, tx *sql.Tx, write ManagementAuditWrite) error {
	if (write.Audit == nil) == (write.Outbox == nil) {
		return errors.New("management audit write requires exactly one destination")
	}
	if write.Outbox != nil {
		outbox := &ManagementAuditOutboxRepository{repositoryBase: repository.repositoryBase}
		return outbox.Append(ctx, tx, *write.Outbox)
	}
	if err := validateAuditLogInsert(ctx, *write.Audit); err != nil {
		return err
	}
	if write.Audit.DetailsJSON != nil && unsafeManagementAuditDetails(*write.Audit.DetailsJSON) {
		return fmt.Errorf("write management audit: %w", ErrUnsafeManagementAuditDetails)
	}
	_, err := insertAuditLog(ctx, tx, repository.dialect, *write.Audit)
	return err
}

func (repository *RedactionKeyRepository) markActiveCASTx(ctx context.Context, tx *sql.Tx, id string) (before, after model.RedactionKeyVersion, outcome string, err error) {
	outcome = RedactionActivationRolledBack
	before, err = repository.getWith(ctx, tx, id)
	if err != nil {
		return before, after, outcome, fmt.Errorf("mark redaction key active: %w", err)
	}
	query := `SELECT id FROM redaction_key_versions WHERE state='active' ORDER BY id`
	if repository.dialect == DialectPostgres {
		query += ` FOR UPDATE`
	}
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return before, after, outcome, fmt.Errorf("mark redaction key active: inspect active versions: %w", err)
	}
	var activeIDs []string
	for rows.Next() {
		var activeID string
		if err = rows.Scan(&activeID); err != nil {
			_ = rows.Close()
			return before, after, outcome, err
		}
		activeIDs = append(activeIDs, activeID)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return before, after, outcome, err
	}
	switch len(activeIDs) {
	case 0:
		if before.State != model.RedactionKeyStateStandby {
			return before, after, outcome, fmt.Errorf("mark redaction key active: first activation requires standby: %w", ErrRedactionKeyTransition)
		}
		err = expectOneAffected(tx.ExecContext(ctx, repository.bind(`UPDATE redaction_key_versions SET state='active',activated_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='standby'`), id))
		outcome = RedactionActivationFirst
	case 1:
		if activeIDs[0] == id {
			return before, after, outcome, fmt.Errorf("mark redaction key active: version is already active: %w", ErrRedactionKeyTransition)
		}
		if before.State != model.RedactionKeyStateStandby && before.State != model.RedactionKeyStateLegacy {
			return before, after, outcome, fmt.Errorf("mark redaction key active: target must be standby or legacy: %w", ErrRedactionKeyTransition)
		}
		if err = expectOneAffected(tx.ExecContext(ctx, repository.bind(`UPDATE redaction_key_versions SET state='legacy',updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='active'`), activeIDs[0])); err == nil {
			err = expectOneAffected(tx.ExecContext(ctx, repository.bind(`UPDATE redaction_key_versions SET state='active',activated_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state IN ('standby','legacy')`), id))
		}
		outcome = RedactionActivationSwitched
	default:
		return before, after, outcome, fmt.Errorf("mark redaction key active: found %d active versions: %w", len(activeIDs), ErrRedactionKeyIntegrity)
	}
	if err != nil {
		return before, after, RedactionActivationRolledBack, fmt.Errorf("mark redaction key active: %w", err)
	}
	after, err = repository.getWith(ctx, tx, id)
	return
}

func (repository *RedactionKeyRepository) markRetiredTx(ctx context.Context, tx *sql.Tx, id string) (before, after model.RedactionKeyVersion, err error) {
	before, err = repository.getWith(ctx, tx, id)
	if err != nil {
		return before, after, fmt.Errorf("retire redaction key version: %w", err)
	}
	switch before.State {
	case model.RedactionKeyStateActive:
		return before, after, fmt.Errorf("retire redaction key version: switch active version first: %w", ErrRedactionKeyTransition)
	case model.RedactionKeyStateRetired:
		return before, after, fmt.Errorf("retire redaction key version: retired is terminal: %w", ErrRedactionKeyTransition)
	case model.RedactionKeyStateStandby, model.RedactionKeyStateLegacy:
	default:
		return before, after, fmt.Errorf("retire redaction key version: unknown state: %w", ErrRedactionKeyIntegrity)
	}
	if err = expectOneAffected(tx.ExecContext(ctx, repository.bind(`UPDATE redaction_key_versions SET state='retired',retired_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state IN ('standby','legacy')`), id)); err != nil {
		return before, after, fmt.Errorf("retire redaction key version: concurrent state change: %w", ErrRedactionKeyTransition)
	}
	after, err = repository.getWith(ctx, tx, id)
	return
}
