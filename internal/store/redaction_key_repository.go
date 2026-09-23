package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"

	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	RedactionActivationFirst      = "first_activated"
	RedactionActivationSwitched   = "switched"
	RedactionActivationRolledBack = "rolled_back"
)

var (
	ErrInvalidRedactionKeyID          = errors.New("redaction key version ID must be canonical decimal text in range 1..9999")
	ErrInvalidKeyCommitment           = errors.New("redaction key commitment must be 64 lowercase hexadecimal characters")
	ErrRedactionKeyCommitmentConflict = errors.New("redaction key commitment conflicts with the registered value")
	ErrRedactionKeyTransition         = errors.New("redaction key state transition is not allowed")
	ErrRedactionKeyIntegrity          = errors.New("redaction key registry integrity violation")
	canonicalRedactionKeyID           = regexp.MustCompile(`^[1-9][0-9]{0,3}$`)
	lowerHexCommitment                = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// RedactionKeyRepository persists key-version metadata without key material.
type RedactionKeyRepository struct {
	repositoryBase
	mu *sync.Mutex
}

// List returns registered versions in numeric ID order.
func (repository *RedactionKeyRepository) List(ctx context.Context) ([]model.RedactionKeyVersion, error) {
	if err := repository.validate(ctx, "list"); err != nil {
		return nil, err
	}
	order := "CAST(id AS INTEGER)"
	if repository.dialect == DialectPostgres {
		order = "id::int"
	}
	rows, err := repository.db.QueryContext(ctx, `SELECT id,state,commitment,label,config_revision,created_at,updated_at,activated_at,retired_at FROM redaction_key_versions ORDER BY `+order)
	if err != nil {
		return nil, fmt.Errorf("list redaction key versions: %w", err)
	}
	defer rows.Close()
	versions := make([]model.RedactionKeyVersion, 0)
	for rows.Next() {
		version, scanErr := scanRedactionKeyVersion(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list redaction key versions: %w", scanErr)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list redaction key versions: %w", err)
	}
	return versions, nil
}

// Get returns one registered key version.
func (repository *RedactionKeyRepository) Get(ctx context.Context, id string) (model.RedactionKeyVersion, error) {
	if err := repository.validate(ctx, "get"); err != nil {
		return model.RedactionKeyVersion{}, err
	}
	if err := validateRedactionKeyID(id); err != nil {
		return model.RedactionKeyVersion{}, fmt.Errorf("get redaction key version: %w", err)
	}
	return repository.getWith(ctx, repository.db, id)
}

// RegisterStandby inserts a standby version or reconciles its non-state metadata.
func (repository *RedactionKeyRepository) RegisterStandby(ctx context.Context, id, commitment, label, configRevision string, tx *sql.Tx) error {
	if err := repository.validate(ctx, "register"); err != nil {
		return err
	}
	if err := validateRedactionKeyID(id); err != nil {
		return fmt.Errorf("register redaction key version: %w", err)
	}
	if !lowerHexCommitment.MatchString(commitment) {
		return fmt.Errorf("register redaction key version: %w", ErrInvalidKeyCommitment)
	}
	executor := sqlExecutor(repository.db)
	if tx != nil {
		executor = tx
	}
	existing, err := repository.getWith(ctx, executor, id)
	if errors.Is(err, ErrNotFound) {
		_, err = executor.ExecContext(ctx, repository.bind(`INSERT INTO redaction_key_versions (id,state,commitment,label,config_revision,created_at,updated_at) VALUES (?,'standby',?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`), id, commitment, optionalText(label), optionalText(configRevision))
		if err != nil {
			return fmt.Errorf("register redaction key version: %w", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if existing.State == model.RedactionKeyStateRetired {
		return fmt.Errorf("register redaction key version: retired is terminal: %w", ErrRedactionKeyTransition)
	}
	if existing.Commitment != "" && existing.Commitment != commitment {
		return fmt.Errorf("register redaction key version: %w", ErrRedactionKeyCommitmentConflict)
	}
	_, err = executor.ExecContext(ctx, repository.bind(`UPDATE redaction_key_versions SET commitment=?,label=?,config_revision=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state IN ('standby','legacy','active')`), commitment, optionalText(label), optionalText(configRevision), id)
	if err != nil {
		return fmt.Errorf("register redaction key version: %w", err)
	}
	return nil
}

// MarkActiveCAS atomically performs the first activation or switches active versions.
func (repository *RedactionKeyRepository) MarkActiveCAS(ctx context.Context, id string) (before, after model.RedactionKeyVersion, outcome string, err error) {
	outcome = RedactionActivationRolledBack
	if validateErr := repository.validate(ctx, "mark active"); validateErr != nil {
		err = validateErr
		return
	}
	if validateErr := validateRedactionKeyID(id); validateErr != nil {
		err = fmt.Errorf("mark redaction key active: %w", validateErr)
		return
	}
	if repository.mu != nil {
		repository.mu.Lock()
		defer repository.mu.Unlock()
	}
	tx, beginErr := repository.db.BeginTx(ctx, nil)
	if beginErr != nil {
		err = fmt.Errorf("mark redaction key active: begin transaction: %w", beginErr)
		return
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	before, err = repository.getWith(ctx, tx, id)
	if err != nil {
		return before, after, outcome, fmt.Errorf("mark redaction key active: %w", err)
	}
	query := `SELECT id FROM redaction_key_versions WHERE state='active' ORDER BY id`
	if repository.dialect == DialectPostgres {
		query += ` FOR UPDATE`
	}
	rows, queryErr := tx.QueryContext(ctx, query)
	if queryErr != nil {
		err = fmt.Errorf("mark redaction key active: inspect active versions: %w", queryErr)
		return
	}
	activeIDs := make([]string, 0, 2)
	for rows.Next() {
		var activeID string
		if scanErr := rows.Scan(&activeID); scanErr != nil {
			_ = rows.Close()
			err = fmt.Errorf("mark redaction key active: inspect active versions: %w", scanErr)
			return
		}
		activeIDs = append(activeIDs, activeID)
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil || closeErr != nil {
		err = fmt.Errorf("mark redaction key active: inspect active versions: %w", errors.Join(iterationErr, closeErr))
		return
	}
	switch len(activeIDs) {
	case 0:
		if before.State != model.RedactionKeyStateStandby {
			err = fmt.Errorf("mark redaction key active: first activation requires standby: %w", ErrRedactionKeyTransition)
			return
		}
		if err = expectOneAffected(tx.ExecContext(ctx, repository.bind(`UPDATE redaction_key_versions SET state='active',activated_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='standby'`), id)); err != nil {
			err = fmt.Errorf("mark redaction key active: %w", err)
			return
		}
		outcome = RedactionActivationFirst
	case 1:
		if activeIDs[0] == id {
			err = fmt.Errorf("mark redaction key active: version is already active: %w", ErrRedactionKeyTransition)
			return
		}
		if before.State != model.RedactionKeyStateStandby && before.State != model.RedactionKeyStateLegacy {
			err = fmt.Errorf("mark redaction key active: target must be standby or legacy: %w", ErrRedactionKeyTransition)
			return
		}
		if err = expectOneAffected(tx.ExecContext(ctx, repository.bind(`UPDATE redaction_key_versions SET state='legacy',updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='active'`), activeIDs[0])); err != nil {
			err = fmt.Errorf("mark redaction key active: demote current version: %w", err)
			return
		}
		if err = expectOneAffected(tx.ExecContext(ctx, repository.bind(`UPDATE redaction_key_versions SET state='active',activated_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state IN ('standby','legacy')`), id)); err != nil {
			err = fmt.Errorf("mark redaction key active: promote target version: %w", err)
			return
		}
		outcome = RedactionActivationSwitched
	default:
		err = fmt.Errorf("mark redaction key active: found %d active versions: %w", len(activeIDs), ErrRedactionKeyIntegrity)
		return
	}
	after, err = repository.getWith(ctx, tx, id)
	if err != nil {
		outcome = RedactionActivationRolledBack
		err = fmt.Errorf("mark redaction key active: read result: %w", err)
		return
	}
	if commitErr := tx.Commit(); commitErr != nil {
		outcome = RedactionActivationRolledBack
		err = fmt.Errorf("mark redaction key active: commit transaction: %w", commitErr)
		return
	}
	return
}

// MarkRetired retires a standby or legacy version. Active versions must be switched first.
func (repository *RedactionKeyRepository) MarkRetired(ctx context.Context, id string) error {
	if err := repository.validate(ctx, "mark retired"); err != nil {
		return err
	}
	if err := validateRedactionKeyID(id); err != nil {
		return fmt.Errorf("retire redaction key version: %w", err)
	}
	current, err := repository.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("retire redaction key version: %w", err)
	}
	switch current.State {
	case model.RedactionKeyStateActive:
		return fmt.Errorf("retire redaction key version: switch active version first: %w", ErrRedactionKeyTransition)
	case model.RedactionKeyStateRetired:
		return fmt.Errorf("retire redaction key version: retired is terminal: %w", ErrRedactionKeyTransition)
	case model.RedactionKeyStateStandby, model.RedactionKeyStateLegacy:
	default:
		return fmt.Errorf("retire redaction key version: unknown state: %w", ErrRedactionKeyIntegrity)
	}
	result, err := repository.db.ExecContext(ctx, repository.bind(`UPDATE redaction_key_versions SET state='retired',retired_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state IN ('standby','legacy')`), id)
	if err != nil {
		return fmt.Errorf("retire redaction key version: %w", err)
	}
	if err := expectOneAffected(result, nil); err != nil {
		return fmt.Errorf("retire redaction key version: concurrent state change: %w", ErrRedactionKeyTransition)
	}
	return nil
}

func (repository *RedactionKeyRepository) getWith(ctx context.Context, executor sqlExecutor, id string) (model.RedactionKeyVersion, error) {
	version, err := scanRedactionKeyVersion(executor.QueryRowContext(ctx, repository.bind(`SELECT id,state,commitment,label,config_revision,created_at,updated_at,activated_at,retired_at FROM redaction_key_versions WHERE id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.RedactionKeyVersion{}, fmt.Errorf("redaction key version not found: %w", ErrNotFound)
	}
	if err != nil {
		return model.RedactionKeyVersion{}, fmt.Errorf("get redaction key version: %w", err)
	}
	return version, nil
}

func scanRedactionKeyVersion(scanner rowScanner) (model.RedactionKeyVersion, error) {
	var version model.RedactionKeyVersion
	var commitment, label, revision sql.NullString
	var createdAt, updatedAt, activatedAt, retiredAt databaseTimestamp
	if err := scanner.Scan(&version.ID, &version.State, &commitment, &label, &revision, &createdAt, &updatedAt, &activatedAt, &retiredAt); err != nil {
		return version, err
	}
	version.Commitment, version.Label, version.ConfigRevision = commitment.String, label.String, revision.String
	var err error
	if version.CreatedAt, err = createdAt.required("redaction_key_versions.created_at"); err != nil {
		return model.RedactionKeyVersion{}, err
	}
	if version.UpdatedAt, err = updatedAt.required("redaction_key_versions.updated_at"); err != nil {
		return model.RedactionKeyVersion{}, err
	}
	version.ActivatedAt, version.RetiredAt = activatedAt.pointer(), retiredAt.pointer()
	return version, nil
}

func (repository *RedactionKeyRepository) validate(ctx context.Context, operation string) error {
	if ctx == nil {
		return fmt.Errorf("%s redaction key versions: %w", operation, ErrNilContext)
	}
	if repository == nil || repository.db == nil {
		return fmt.Errorf("%s redaction key versions: repository is not initialized", operation)
	}
	return nil
}

func validateRedactionKeyID(id string) error {
	if !canonicalRedactionKeyID.MatchString(id) {
		return ErrInvalidRedactionKeyID
	}
	value, err := strconv.Atoi(id)
	if err != nil || value < 1 || value > 9999 || strconv.Itoa(value) != id {
		return ErrInvalidRedactionKeyID
	}
	return nil
}

func optionalText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func expectOneAffected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("conditional update affected %d rows", count)
	}
	return nil
}
