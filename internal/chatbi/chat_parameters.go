package chatbi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type chatSettings struct {
	Name                    string `json:"name"`
	InitialPasswordDisabled bool   `json:"initial_password_disabled"`
	HideThinking            bool   `json:"hide_thinking"`
	ExpandThinking          bool   `json:"expand_thinking"`
	LimitRows               bool   `json:"limit_rows"`
	ContextRecords          int    `json:"context_records"`
	ShowSQL                 bool   `json:"show_sql"`
	ShowLog                 bool   `json:"show_log"`
}

func defaultChatSettings() chatSettings {
	return chatSettings{Name: "AgentSQL", LimitRows: true, ContextRecords: 3, ShowSQL: true, ShowLog: true}
}

func chatSettingsKey() objectKey { return objectKey{"chat-parameters", "system", "system", "default"} }

func validChatSettings(value chatSettings) bool {
	if value.Name == "" || len([]rune(value.Name)) > 80 || strings.TrimSpace(value.Name) != value.Name ||
		strings.ContainsAny(value.Name, "<>\r\n\x00") || !value.LimitRows || value.ContextRecords < 0 || value.ContextRecords > 3 {
		return false
	}
	return true
}

func (s *Store) ChatSettings(ctx context.Context) (chatSettings, error) {
	var value chatSettings
	_, err := s.get(ctx, chatSettingsKey(), &value)
	if errors.Is(err, ErrNotFound) {
		return defaultChatSettings(), nil
	}
	if err != nil || !validChatSettings(value) {
		return chatSettings{}, ErrUnavailable
	}
	return value, nil
}

func (s *Store) UpdateChatSettings(ctx context.Context, p Principal, updates map[string]string) error {
	if !p.User.ID.Valid() || !p.Workspace.Valid() || p.EmbeddedApp != "" || len(updates) == 0 || len(updates) > 7 {
		return ErrInvalidAccountChange
	}
	key := chatSettingsKey()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	var body []byte
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&revision, &body)
	value := defaultChatSettings()
	if err != nil && !controlNoRows(err) {
		return ErrUnavailable
	}
	if err == nil && (s.decode(key, body, &value) != nil || !validChatSettings(value)) {
		return ErrUnavailable
	}
	for name, raw := range updates {
		switch name {
		case "login.initial_pwd_disabled":
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return ErrInvalidAccountChange
			}
			value.InitialPasswordDisabled = parsed
		case "chat.sqlbot_name":
			value.Name = raw
		case "chat.hide_thinking_block":
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return ErrInvalidAccountChange
			}
			value.HideThinking = parsed
		case "chat.expand_thinking_block":
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return ErrInvalidAccountChange
			}
			value.ExpandThinking = parsed
		case "chat.limit_rows":
			parsed, err := strconv.ParseBool(raw)
			if err != nil || !parsed {
				return ErrInvalidAccountChange
			}
			value.LimitRows = true
		case "chat.context_record_count":
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 || parsed > 3 {
				return ErrInvalidAccountChange
			}
			value.ContextRecords = parsed
		case "chat.show_sql":
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return ErrInvalidAccountChange
			}
			value.ShowSQL = parsed
		case "chat.show_log":
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return ErrInvalidAccountChange
			}
			value.ShowLog = parsed
		default:
			return ErrInvalidAccountChange
		}
	}
	if !validChatSettings(value) {
		return ErrInvalidAccountChange
	}
	sealed, err := s.seal(key, value)
	if err != nil {
		return err
	}
	var result sql.Result
	if revision == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
			key.Kind, key.Scope, key.Owner, key.ID, sealed)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
			sealed, key.Kind, key.Scope, key.Owner, key.ID, revision)
	}
	if err != nil {
		return ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrConflict
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	keys := make([]string, 0, len(updates))
	for name := range updates {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	// Audit key names, not values: settings can contain an operator's chosen label.
	audit, err := s.seal(auditKey, map[string]any{"action": "chat-parameters.update", "actor": p.User.ID,
		"workspace": p.Workspace, "keys": keys, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
		auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return ErrUnavailable
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}

func chatParameterList(value chatSettings, includeName bool) []sqlbotChatParameter {
	result := []sqlbotChatParameter{
		{Key: "chat.hide_thinking_block", Value: strconv.FormatBool(value.HideThinking), ID: "7520382585415929856", Type: "str"},
		{Key: "chat.expand_thinking_block", Value: strconv.FormatBool(value.ExpandThinking), ID: "7520382585415929857", Type: "str"},
		{Key: "chat.limit_rows", Value: "true", ID: "7520382585415929858", Type: "str"},
		{Key: "chat.context_record_count", Value: strconv.Itoa(value.ContextRecords), ID: "7520382585415929859", Type: "str"},
		{Key: "chat.show_sql", Value: strconv.FormatBool(value.ShowSQL), ID: "7520382585415929860", Type: "str"},
		{Key: "chat.show_log", Value: strconv.FormatBool(value.ShowLog), ID: "7520382585415929861", Type: "str"},
	}
	if includeName {
		result = append([]sqlbotChatParameter{{Key: "chat.sqlbot_name", Value: value.Name, ID: "7520382585415929855", Type: "str"}}, result...)
	}
	return result
}

func (h *Handler) sqlbotAllParameters(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	value, err := h.service.Store.ChatSettings(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, append(chatParameterList(value, true), loginParameterList(value)...))
}

func (h *Handler) nativeChatParameters(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	value, err := h.service.Store.ChatSettings(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, value)
}

func (h *Handler) nativeSaveChatParameters(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var value chatSettings
	if !readBody(w, r, &value) {
		return
	}
	if !validChatSettings(value) {
		failure(w, ErrInvalidAccountChange)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	updates := map[string]string{
		"chat.sqlbot_name":           value.Name,
		"chat.hide_thinking_block":   strconv.FormatBool(value.HideThinking),
		"chat.expand_thinking_block": strconv.FormatBool(value.ExpandThinking),
		"chat.limit_rows":            "true",
		"chat.context_record_count":  strconv.Itoa(value.ContextRecords),
		"chat.show_sql":              strconv.FormatBool(value.ShowSQL),
		"chat.show_log":              strconv.FormatBool(value.ShowLog),
	}
	if err := h.service.Store.UpdateChatSettings(r.Context(), p, updates); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}

func (h *Handler) sqlbotSaveParameters(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseMultipartForm(16 << 10); err != nil || r.MultipartForm == nil || len(r.MultipartForm.File) != 0 ||
		len(r.MultipartForm.Value) != 1 || len(r.MultipartForm.Value["data"]) != 1 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	defer r.MultipartForm.RemoveAll()
	var submitted []struct {
		Key   string          `json:"pkey"`
		Value json.RawMessage `json:"pval"`
	}
	if json.Unmarshal([]byte(r.MultipartForm.Value["data"][0]), &submitted) != nil || len(submitted) == 0 || len(submitted) > 7 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	updates := make(map[string]string, len(submitted))
	for _, item := range submitted {
		if item.Key == "" || len(item.Value) == 0 || len(item.Value) > 512 {
			failure(w, ErrInvalidAccountChange)
			return
		}
		if _, exists := updates[item.Key]; exists {
			failure(w, ErrInvalidAccountChange)
			return
		}
		var value any
		if json.Unmarshal(item.Value, &value) != nil {
			failure(w, ErrInvalidAccountChange)
			return
		}
		switch typed := value.(type) {
		case string:
			updates[item.Key] = typed
		case bool:
			updates[item.Key] = strconv.FormatBool(typed)
		case float64:
			if typed != float64(int64(typed)) {
				failure(w, ErrInvalidAccountChange)
				return
			}
			updates[item.Key] = strconv.FormatInt(int64(typed), 10)
		default:
			failure(w, ErrInvalidAccountChange)
			return
		}
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.UpdateChatSettings(r.Context(), p, updates); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}
