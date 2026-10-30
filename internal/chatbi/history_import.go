package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"
)

// HistorySnapshot deliberately excludes SQL, result rows, charts, prompts and
// credentials. Imported records are never used as live model history or query
// replay. Their original decimal IDs remain available to client lookups.
type HistorySnapshot struct {
	FormatVersion int              `json:"format_version"`
	SourceVersion string           `json:"source_version"`
	SourceCommit  string           `json:"source_commit"`
	Chats         []HistoricalChat `json:"chats"`
}

type HistoricalChat struct {
	ID         ID                 `json:"id"`
	Workspace  ID                 `json:"oid"`
	Creator    ID                 `json:"create_by"`
	Created    time.Time          `json:"create_time"`
	Brief      string             `json:"brief"`
	Datasource ID                 `json:"datasource"`
	Origin     int                `json:"origin"`
	Records    []HistoricalRecord `json:"records"`
}

type HistoricalRecord struct {
	ID       ID        `json:"id"`
	Question string    `json:"question"`
	Created  time.Time `json:"create_time"`
}

type HistoryReport struct {
	ImportedChats      int  `json:"imported_chats"`
	ImportedQuestions  int  `json:"imported_questions"`
	CompleteMigration  bool `json:"complete_migration"`
	HistoricalRowsUsed bool `json:"historical_rows_used"`
}

func ReadHistory(path string) (HistorySnapshot, error) {
	var snapshot HistorySnapshot
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 || info.Size() == 0 {
		return snapshot, errors.New("invalid private history snapshot")
	}
	file, err := os.Open(path)
	if err != nil {
		return snapshot, errors.New("cannot read private history snapshot")
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, (16<<20)+1))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(new(any)) != io.EOF || validateHistory(snapshot) != nil {
		return HistorySnapshot{}, errors.New("invalid or unsupported history snapshot")
	}
	return snapshot, nil
}

func validateHistory(snapshot HistorySnapshot) error {
	if snapshot.FormatVersion != 1 || !pinnedSQLBotSnapshot(snapshot.SourceVersion, snapshot.SourceCommit) || len(snapshot.Chats) == 0 || len(snapshot.Chats) > 1000 {
		return ErrInvalidSemantic
	}
	chatIDs := map[string]bool{}
	recordIDs := map[string]bool{}
	for _, item := range snapshot.Chats {
		if !item.ID.Valid() || !item.Workspace.Valid() || !item.Creator.Valid() || !item.Datasource.Valid() || item.Created.IsZero() || len(item.Brief) > 256 || item.Origin < 0 || item.Origin > 2 || len(item.Records) > 200 {
			return ErrInvalidSemantic
		}
		key := string(item.Workspace) + "/" + string(item.Creator) + "/" + string(item.ID)
		if chatIDs[key] {
			return ErrInvalidSemantic
		}
		chatIDs[key] = true
		for _, record := range item.Records {
			if !record.ID.Valid() || strings.TrimSpace(record.Question) == "" || len(record.Question) > 16000 || record.Created.IsZero() {
				return ErrInvalidSemantic
			}
			key := string(item.Workspace) + "/" + string(item.Creator) + "/" + string(record.ID)
			if recordIDs[key] {
				return ErrInvalidSemantic
			}
			recordIDs[key] = true
		}
	}
	return nil
}

func (store *Store) ImportHistory(ctx context.Context, snapshot HistorySnapshot) (HistoryReport, error) {
	if err := validateHistory(snapshot); err != nil {
		return HistoryReport{}, err
	}
	type sealedEntry struct {
		key  objectKey
		body []byte
	}
	entries := make([]sealedEntry, 0, len(snapshot.Chats)*2)
	questionCount := 0
	for _, source := range snapshot.Chats {
		user, _, err := store.User(ctx, source.Creator)
		if err != nil {
			return HistoryReport{}, err
		}
		p := Principal{User: user, Workspace: source.Workspace}
		if !user.canUse(source.Workspace) {
			return HistoryReport{}, ErrUnauthenticated
		}
		var binding Binding
		if _, err := store.get(ctx, bindingKey(p, source.Datasource), &binding); err != nil {
			return HistoryReport{}, err
		}
		if binding.User != source.Creator || binding.Workspace != source.Workspace || binding.Datasource != source.Datasource || binding.Name == "" || (binding.Dialect != "pg" && binding.Dialect != "mysql") {
			return HistoryReport{}, ErrUnavailable
		}
		chat := Chat{ID: source.ID, Workspace: source.Workspace, Creator: source.Creator, Created: source.Created.UTC(), Latest: source.Created.UTC(), Brief: source.Brief, Type: "chat", Datasource: source.Datasource, Engine: binding.Dialect, DSType: binding.Dialect, DatasourceName: binding.Name, DatasourceExists: true, Origin: source.Origin, Records: make([]Record, 0, len(source.Records))}
		for _, item := range source.Records {
			created := item.Created.UTC()
			record := Record{ID: item.ID, Chat: source.ID, Datasource: source.Datasource, Question: item.Question, Created: created, FinishedAt: &created, Finish: true, Error: "Imported history has no live result", ErrorStage: "history_import"}
			chat.Records = append(chat.Records, record)
			if created.After(chat.Latest) {
				chat.Latest = created
			}
			key := recordKey(p, item.ID)
			body, err := store.seal(key, source.ID)
			if err != nil {
				return HistoryReport{}, err
			}
			entries = append(entries, sealedEntry{key, body})
			questionCount++
		}
		key := chatKey(p, source.ID)
		body, err := store.seal(key, chat)
		if err != nil {
			return HistoryReport{}, err
		}
		entries = append(entries, sealedEntry{key, body})
	}
	auditID, err := newID()
	if err != nil {
		return HistoryReport{}, err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := store.seal(auditKey, map[string]any{"action": "history.import", "source_version": snapshot.SourceVersion, "source_commit": snapshot.SourceCommit, "chats": len(snapshot.Chats), "questions": questionCount, "time": time.Now().UTC()})
	if err != nil {
		return HistoryReport{}, err
	}
	entries = append(entries, sealedEntry{auditKey, audit})
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return HistoryReport{}, ErrUnavailable
	}
	defer tx.Rollback()
	for _, entry := range entries {
		result, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`, entry.key.Kind, entry.key.Scope, entry.key.Owner, entry.key.ID, entry.body)
		if err != nil {
			return HistoryReport{}, ErrUnavailable
		}
		count, err := result.RowsAffected()
		if err != nil {
			return HistoryReport{}, ErrUnavailable
		}
		if count != 1 {
			return HistoryReport{}, ErrConflict
		}
	}
	if tx.Commit() != nil {
		return HistoryReport{}, ErrUnavailable
	}
	return HistoryReport{ImportedChats: len(snapshot.Chats), ImportedQuestions: questionCount, CompleteMigration: false, HistoricalRowsUsed: false}, nil
}
