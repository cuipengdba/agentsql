package chatbi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// These are workspace-owned business descriptions. An example is never sent
// directly to the gateway or treated as permission to execute its SQL.
type Terminology struct {
	ID                      int64     `json:"id"`
	Created                 time.Time `json:"create_time"`
	Word                    string    `json:"word"`
	Description             string    `json:"description"`
	OtherWords              []string  `json:"other_words"`
	SpecificDS              bool      `json:"specific_ds"`
	DatasourceIDs           []int64   `json:"datasource_ids"`
	DatasourceNames         []string  `json:"datasource_names"`
	Enabled                 bool      `json:"enabled"`
	AdvancedApplication     *int64    `json:"advanced_application"`
	AdvancedApplicationName *string   `json:"advanced_application_name"`
}

type SemanticHint struct {
	Word        string   `json:"word"`
	Description string   `json:"description"`
	OtherWords  []string `json:"other_words"`
}

type SQLExample struct {
	ID                      int64     `json:"id"`
	Workspace               int64     `json:"oid"`
	Datasource              int64     `json:"datasource"`
	DatasourceName          string    `json:"datasource_name"`
	Created                 time.Time `json:"create_time"`
	Question                string    `json:"question"`
	Description             string    `json:"description"`
	Enabled                 bool      `json:"enabled"`
	AdvancedApplication     *int64    `json:"advanced_application"`
	AdvancedApplicationName *string   `json:"advanced_application_name"`
}

var ErrInvalidSemantic = errors.New("invalid semantic metadata")

func semanticKey(kind string, workspace ID, id int64) objectKey {
	return objectKey{kind, string(workspace), "workspace", strconv.FormatInt(id, 10)}
}

func (service *Service) semanticAdmin(ctx context.Context, p Principal) error {
	user, _, err := service.Store.User(ctx, p.User.ID)
	if err != nil || user.AuthRevision != p.User.AuthRevision || !user.Admin || !user.canUse(p.Workspace) {
		return ErrUnauthenticated
	}
	return nil
}

func validTerm(term Terminology) bool {
	if len(strings.TrimSpace(term.Word)) == 0 || len(term.Word) > 200 || len(term.Description) > 4000 || len(term.OtherWords) > 20 || len(term.DatasourceIDs) > 100 || term.AdvancedApplication != nil || term.AdvancedApplicationName != nil {
		return false
	}
	for _, word := range term.OtherWords {
		if len(strings.TrimSpace(word)) == 0 || len(word) > 200 {
			return false
		}
	}
	if term.SpecificDS && len(term.DatasourceIDs) == 0 || !term.SpecificDS && len(term.DatasourceIDs) != 0 {
		return false
	}
	seen := map[int64]bool{}
	for _, id := range term.DatasourceIDs {
		if id <= 0 || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func validExample(example SQLExample, workspace ID) bool {
	wid, err := strconv.ParseInt(string(workspace), 10, 64)
	return err == nil && example.Workspace == wid && example.Datasource > 0 && len(strings.TrimSpace(example.Question)) > 0 && len(example.Question) <= 1000 && len(strings.TrimSpace(example.Description)) > 0 && len(example.Description) <= 65536 && example.AdvancedApplication == nil && example.AdvancedApplicationName == nil
}

// writeSemantic and deleteSemantic commit the object and management audit in
// one SQLite transaction. A failed audit must also roll back the object change.
func (s *Store) writeSemantic(ctx context.Context, key objectKey, value any, expected int64, action string) error {
	return s.writeSemanticWithDetails(ctx, key, value, expected, action, nil)
}

func (s *Store) writeSemanticWithDetails(ctx context.Context, key objectKey, value any, expected int64, action string, details map[string]any) error {
	sealed, err := s.seal(key, value)
	if err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	auditBody := map[string]any{"action": action, "workspace": key.Scope, "owner": key.Owner, "id": key.ID, "time": time.Now().UTC()}
	for name, value := range details {
		if name == "action" || name == "workspace" || name == "owner" || name == "id" || name == "time" {
			return ErrUnavailable
		}
		auditBody[name] = value
	}
	audit, err := s.seal(auditKey, auditBody)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	var result sql.Result
	if expected == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`, key.Kind, key.Scope, key.Owner, key.ID, sealed)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`, sealed, key.Kind, key.Scope, key.Owner, key.ID, expected)
	}
	if err != nil {
		return ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ErrUnavailable
	}
	if count != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`, auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return ErrUnavailable
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Store) deleteSemantic(ctx context.Context, key objectKey, expected int64, action string) error {
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": action, "workspace": key.Scope, "id": key.ID, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`, key.Kind, key.Scope, key.Owner, key.ID, expected)
	if err != nil {
		return ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ErrUnavailable
	}
	if count != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`, auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return ErrUnavailable
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}

func (service *Service) SaveTerminology(ctx context.Context, p Principal, term Terminology) (int64, error) {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return 0, err
	}
	if !validTerm(term) {
		return 0, ErrInvalidSemantic
	}
	names := make([]string, 0, len(term.DatasourceIDs))
	for _, id := range term.DatasourceIDs {
		binding, err := service.binding(ctx, p, ID(strconv.FormatInt(id, 10)))
		if err != nil {
			return 0, err
		}
		names = append(names, binding.Name)
	}
	var revision int64
	if term.ID == 0 {
		id, err := newID()
		if err != nil {
			return 0, err
		}
		term.ID, _ = strconv.ParseInt(string(id), 10, 64)
		term.Created = time.Now().UTC()
	} else {
		var previous Terminology
		var err error
		revision, err = service.Store.get(ctx, semanticKey("terminology", p.Workspace, term.ID), &previous)
		if err != nil {
			return 0, err
		}
		term.Created = previous.Created
	}
	if term.OtherWords == nil {
		term.OtherWords = []string{}
	}
	if term.DatasourceIDs == nil {
		term.DatasourceIDs = []int64{}
	}
	term.DatasourceNames = names
	return term.ID, service.Store.writeSemantic(ctx, semanticKey("terminology", p.Workspace, term.ID), term, revision, "terminology.save")
}

func (service *Service) SaveExample(ctx context.Context, p Principal, example SQLExample) (int64, error) {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return 0, err
	}
	if !validExample(example, p.Workspace) {
		return 0, ErrInvalidSemantic
	}
	binding, err := service.binding(ctx, p, ID(strconv.FormatInt(example.Datasource, 10)))
	if err != nil {
		return 0, err
	}
	var revision int64
	if example.ID == 0 {
		id, err := newID()
		if err != nil {
			return 0, err
		}
		example.ID, _ = strconv.ParseInt(string(id), 10, 64)
		example.Created = time.Now().UTC()
	} else {
		var previous SQLExample
		revision, err = service.Store.get(ctx, semanticKey("sql-example", p.Workspace, example.ID), &previous)
		if err != nil {
			return 0, err
		}
		example.Created = previous.Created
	}
	example.DatasourceName = binding.Name
	return example.ID, service.Store.writeSemantic(ctx, semanticKey("sql-example", p.Workspace, example.ID), example, revision, "sql-example.save")
}

func (service *Service) semanticList(ctx context.Context, p Principal, kind string) ([]json.RawMessage, error) {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return nil, err
	}
	return service.Store.list(ctx, kind, string(p.Workspace), "workspace")
}

func (service *Service) Terminologies(ctx context.Context, p Principal) ([]Terminology, error) {
	raw, err := service.semanticList(ctx, p, "terminology")
	if err != nil {
		return nil, err
	}
	result := make([]Terminology, 0, len(raw))
	for _, item := range raw {
		var term Terminology
		if json.Unmarshal(item, &term) != nil || !validTerm(term) || term.ID <= 0 {
			return nil, ErrUnavailable
		}
		result = append(result, term)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID > result[j].ID })
	return result, nil
}

func (service *Service) Examples(ctx context.Context, p Principal) ([]SQLExample, error) {
	raw, err := service.semanticList(ctx, p, "sql-example")
	if err != nil {
		return nil, err
	}
	result := make([]SQLExample, 0, len(raw))
	for _, item := range raw {
		var example SQLExample
		if json.Unmarshal(item, &example) != nil || !validExample(example, p.Workspace) || example.ID <= 0 {
			return nil, ErrUnavailable
		}
		result = append(result, example)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID > result[j].ID })
	return result, nil
}

// Vocabulary selects only enabled terms from the current workspace and the
// bound datasource. Matching is deterministic and bounded before model use.
// Terms carry no SQL and never replace the gateway's live schema decision.
func (service *Service) Vocabulary(ctx context.Context, p Principal, datasource ID, question string) ([]SemanticHint, error) {
	if _, err := service.binding(ctx, p, datasource); err != nil {
		return nil, err
	}
	raw, err := service.Store.list(ctx, "terminology", string(p.Workspace), "workspace")
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(question)
	hints := make([]SemanticHint, 0, 8)
	bytesUsed := 2
	for _, item := range raw {
		var term Terminology
		if json.Unmarshal(item, &term) != nil || !validTerm(term) || term.ID <= 0 {
			return nil, ErrUnavailable
		}
		if !term.Enabled {
			continue
		}
		if term.SpecificDS {
			matched := false
			for _, id := range term.DatasourceIDs {
				if ID(strconv.FormatInt(id, 10)) == datasource {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		found := strings.Contains(query, strings.ToLower(term.Word))
		for _, synonym := range term.OtherWords {
			if strings.Contains(query, strings.ToLower(synonym)) {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		hint := SemanticHint{Word: term.Word, Description: term.Description, OtherWords: term.OtherWords}
		encoded, err := json.Marshal(hint)
		if err != nil {
			return nil, ErrUnavailable
		}
		if bytesUsed+len(encoded)+1 > 4096 {
			continue
		}
		bytesUsed += len(encoded) + 1
		hints = append(hints, hint)
		if len(hints) == 8 {
			break
		}
	}
	return hints, nil
}

func (service *Service) SetSemanticEnabled(ctx context.Context, p Principal, kind string, id int64, enabled bool) error {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return err
	}
	if id <= 0 {
		return ErrNotFound
	}
	key := semanticKey(kind, p.Workspace, id)
	if kind == "terminology" {
		var term Terminology
		rev, err := service.Store.get(ctx, key, &term)
		if err != nil {
			return err
		}
		term.Enabled = enabled
		return service.Store.writeSemantic(ctx, key, term, rev, kind+".enable")
	}
	if kind != "sql-example" {
		return ErrNotFound
	}
	var example SQLExample
	rev, err := service.Store.get(ctx, key, &example)
	if err != nil {
		return err
	}
	example.Enabled = enabled
	return service.Store.writeSemantic(ctx, key, example, rev, kind+".enable")
}

func (service *Service) DeleteSemantic(ctx context.Context, p Principal, kind string, id int64) error {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return err
	}
	if id <= 0 || kind != "terminology" && kind != "sql-example" {
		return ErrNotFound
	}
	key := semanticKey(kind, p.Workspace, id)
	var value any = new(Terminology)
	if kind == "sql-example" {
		value = new(SQLExample)
	}
	rev, err := service.Store.get(ctx, key, value)
	if err != nil {
		return err
	}
	return service.Store.deleteSemantic(ctx, key, rev, kind+".delete")
}

// DeleteSemanticBatch matches the source API's ID-array request while keeping
// the entire batch and one management audit all-or-nothing.
func (service *Service) DeleteSemanticBatch(ctx context.Context, p Principal, kind string, ids []int64) error {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return err
	}
	if (kind != "terminology" && kind != "sql-example") || len(ids) == 0 || len(ids) > 100 {
		return ErrInvalidSemantic
	}
	type target struct {
		key      objectKey
		revision int64
	}
	targets := make([]target, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return ErrInvalidSemantic
		}
		seen[id] = true
		key := semanticKey(kind, p.Workspace, id)
		var value any = new(Terminology)
		if kind == "sql-example" {
			value = new(SQLExample)
		}
		revision, err := service.Store.get(ctx, key, value)
		if err != nil {
			return err
		}
		targets = append(targets, target{key, revision})
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := service.Store.seal(auditKey, map[string]any{"action": kind + ".delete", "workspace": p.Workspace, "ids": ids, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	tx, err := service.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	for _, item := range targets {
		result, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`, item.key.Kind, item.key.Scope, item.key.Owner, item.key.ID, item.revision)
		if err != nil {
			return ErrUnavailable
		}
		count, err := result.RowsAffected()
		if err != nil {
			return ErrUnavailable
		}
		if count != 1 {
			return ErrConflict
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`, auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return ErrUnavailable
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}
