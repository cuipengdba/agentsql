package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
)

// ErrAgentNotFound indicates that an Agent lookup returned no record.
var ErrAgentNotFound = errors.New("agent not found")

// AgentRepository provides CRUD operations for agents.
type AgentRepository struct {
	db *sql.DB
}

// Create inserts an agent and returns the stored record.
func (repository *AgentRepository) Create(ctx context.Context, agent model.Agent) (model.Agent, error) {
	if err := validateAPIKeyHash(agent.APIKeyHash); err != nil {
		return model.Agent{}, fmt.Errorf("create agent %q: %w", agent.ID, err)
	}
	_, err := repository.db.ExecContext(ctx, `
INSERT INTO agents (id, name, owner, status, api_key_hash, level, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		agent.ID,
		agent.Name,
		optionalString(agent.Owner),
		agent.Status,
		agent.APIKeyHash,
		agent.Level,
		optionalTime(agent.ExpiresAt),
	)
	if err != nil {
		return model.Agent{}, fmt.Errorf("create agent %q: %w", agent.ID, err)
	}
	created, err := repository.Get(ctx, agent.ID)
	if err != nil {
		return model.Agent{}, fmt.Errorf("read created agent %q: %w", agent.ID, err)
	}
	return created, nil
}

// Get returns an agent by ID.
func (repository *AgentRepository) Get(ctx context.Context, id string) (model.Agent, error) {
	agent, err := scanAgent(repository.db.QueryRowContext(ctx, `
SELECT id, name, owner, status, api_key_hash, level, expires_at, created_at, updated_at
FROM agents
WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Agent{}, fmt.Errorf("get agent %q: %w", id, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return model.Agent{}, fmt.Errorf("get agent %q: %w", id, err)
	}
	return agent, nil
}

// List returns all agents ordered by creation time and ID.
func (repository *AgentRepository) List(ctx context.Context) ([]model.Agent, error) {
	if ctx == nil {
		return nil, fmt.Errorf("list agents: %w", ErrNilContext)
	}
	rows, err := repository.db.QueryContext(ctx, `
SELECT id, name, owner, status, api_key_hash, level, expires_at,
       created_at, updated_at
FROM agents
ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	agents := make([]model.Agent, 0)
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agents: %w", closeRowsAfterError(rows, err))
		}
		agents = append(agents, agent)
	}
	iterationError := rows.Err()
	closeError := rows.Close()
	if iterationError != nil || closeError != nil {
		return nil, fmt.Errorf("finish agents: %w", errors.Join(iterationError, closeError))
	}
	return agents, nil
}

// GetByAPIKeyHash returns an Agent by an exact SHA-256 API key digest.
func (repository *AgentRepository) GetByAPIKeyHash(
	ctx context.Context,
	hash string,
) (model.Agent, error) {
	if err := validateAPIKeyHash(hash); err != nil {
		return model.Agent{}, fmt.Errorf("get agent by API key hash: %w", err)
	}
	agent, err := scanAgent(repository.db.QueryRowContext(ctx, `
SELECT id, name, owner, status, api_key_hash, level, expires_at, created_at, updated_at
FROM agents INDEXED BY idx_agents_keyhash
WHERE api_key_hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Agent{}, fmt.Errorf(
			"get agent by API key hash: %w",
			errors.Join(ErrAgentNotFound, ErrNotFound, err),
		)
	}
	if err != nil {
		return model.Agent{}, fmt.Errorf("get agent by API key hash: %w", err)
	}
	return agent, nil
}

// Update replaces mutable agent fields and returns the stored record.
func (repository *AgentRepository) Update(ctx context.Context, agent model.Agent) (model.Agent, error) {
	if err := validateAPIKeyHash(agent.APIKeyHash); err != nil {
		return model.Agent{}, fmt.Errorf("update agent %q: %w", agent.ID, err)
	}
	result, err := repository.db.ExecContext(ctx, `
UPDATE agents
SET name = ?, owner = ?, status = ?, api_key_hash = ?, level = ?, expires_at = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?`,
		agent.Name,
		optionalString(agent.Owner),
		agent.Status,
		agent.APIKeyHash,
		agent.Level,
		optionalTime(agent.ExpiresAt),
		agent.ID,
	)
	if err != nil {
		return model.Agent{}, fmt.Errorf("update agent %q: %w", agent.ID, err)
	}
	if err := checkRowsAffected(result, "agent", agent.ID); err != nil {
		return model.Agent{}, fmt.Errorf("update agent %q: %w", agent.ID, err)
	}
	updated, err := repository.Get(ctx, agent.ID)
	if err != nil {
		return model.Agent{}, fmt.Errorf("read updated agent %q: %w", agent.ID, err)
	}
	return updated, nil
}

// Delete removes an agent by ID.
func (repository *AgentRepository) Delete(ctx context.Context, id string) error {
	result, err := repository.db.ExecContext(ctx, "DELETE FROM agents WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete agent %q: %w", id, err)
	}
	if err := checkRowsAffected(result, "agent", id); err != nil {
		return fmt.Errorf("delete agent %q: %w", id, err)
	}
	return nil
}

func scanAgent(scanner rowScanner) (model.Agent, error) {
	var agent model.Agent
	var owner sql.NullString
	var expiresAt, createdAt, updatedAt databaseTimestamp
	if err := scanner.Scan(
		&agent.ID,
		&agent.Name,
		&owner,
		&agent.Status,
		&agent.APIKeyHash,
		&agent.Level,
		&expiresAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		return model.Agent{}, fmt.Errorf("scan agent: %w", err)
	}

	agent.Owner = stringPointer(owner)
	agent.ExpiresAt = expiresAt.pointer()
	var err error
	agent.CreatedAt, err = createdAt.required("agents.created_at")
	if err != nil {
		return model.Agent{}, fmt.Errorf("scan agent: %w", err)
	}
	agent.UpdatedAt, err = updatedAt.required("agents.updated_at")
	if err != nil {
		return model.Agent{}, fmt.Errorf("scan agent: %w", err)
	}
	return agent, nil
}
