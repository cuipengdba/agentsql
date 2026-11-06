package chatbi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const maxAssistantImageBytes = 10 << 20

type assistantImage struct {
	MIME string `json:"mime"`
	Data []byte `json:"data"`
}

type assistantImageUpload struct {
	ID string
	assistantImage
}

func newAssistantImageID(mime string) (string, error) {
	extension := ".png"
	if mime == "image/jpeg" {
		extension = ".jpg"
	} else if mime != "image/png" {
		return "", ErrInvalidAssistant
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", ErrUnavailable
	}
	return hex.EncodeToString(entropy[:]) + extension, nil
}

func validAssistantImageID(id string) bool {
	if len(id) != 36 || (!strings.HasSuffix(id, ".png") && !strings.HasSuffix(id, ".jpg")) {
		return false
	}
	_, err := hex.DecodeString(id[:32])
	return err == nil && strings.ToLower(id[:32]) == id[:32]
}

func removedAssistantImageIDs(previous, next string) ([]string, error) {
	decode := func(raw string) (struct {
		Logo      string `json:"logo"`
		FloatIcon string `json:"float_icon"`
	}, error) {
		var refs struct {
			Logo      string `json:"logo"`
			FloatIcon string `json:"float_icon"`
		}
		if raw != "" && json.Unmarshal([]byte(raw), &refs) != nil {
			return refs, ErrUnavailable
		}
		return refs, nil
	}
	old, err := decode(previous)
	if err != nil {
		return nil, err
	}
	current, err := decode(next)
	if err != nil {
		return nil, err
	}
	removed := make([]string, 0, 2)
	seen := map[string]bool{}
	for _, id := range []string{old.Logo, old.FloatIcon} {
		if validAssistantImageID(id) && id != current.Logo && id != current.FloatIcon && !seen[id] {
			removed = append(removed, id)
			seen[id] = true
		}
	}
	return removed, nil
}

// Save UI assets and the application revision in the same transaction as the
// management audit. An image is addressed by a random, app-owned capability ID;
// no filesystem path from the original multipart request is ever used.
func (service *Service) saveAssistantUI(ctx context.Context, p Principal, app AssistantApplication, configuration, action string, uploads []assistantImageUpload, obsolete []string) (AssistantApplication, error) {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return AssistantApplication{}, err
	}
	if (action != "assistant.ui.save" && action != "assistant.save") || app.ID == "" || app.Workspace != p.Workspace || app.Revision < 1 ||
		(app.Type != 0 && app.Type != 1 && app.Type != 4) || len(configuration) > 8192 ||
		len(uploads) > 2 || len(obsolete) > 2 {
		return AssistantApplication{}, ErrInvalidAssistant
	}
	var parsed map[string]json.RawMessage
	if configuration == "" && action == "assistant.ui.save" ||
		configuration != "" && (json.Unmarshal([]byte(configuration), &parsed) != nil || parsed == nil) {
		return AssistantApplication{}, ErrInvalidAssistant
	}
	app.Configuration = configuration
	app.Updated = time.Now().UTC()
	key := assistantKey(p.Workspace, app.ID)
	appBody, err := service.Store.seal(key, app)
	if err != nil {
		return AssistantApplication{}, err
	}
	imageBodies := make([][]byte, len(uploads))
	seen := map[string]bool{}
	for index, upload := range uploads {
		if !validAssistantImageID(upload.ID) || seen[upload.ID] || len(upload.Data) == 0 || len(upload.Data) > maxAssistantImageBytes ||
			(upload.MIME != "image/png" && upload.MIME != "image/jpeg") {
			return AssistantApplication{}, ErrInvalidAssistant
		}
		seen[upload.ID] = true
		imageBodies[index], err = service.Store.seal(objectKey{"assistant-image", string(p.Workspace), string(app.ID), upload.ID}, upload.assistantImage)
		if err != nil {
			return AssistantApplication{}, err
		}
	}
	for _, id := range obsolete {
		if !validAssistantImageID(id) || seen[id] {
			return AssistantApplication{}, ErrInvalidAssistant
		}
		seen[id] = true
	}
	auditID, err := newID()
	if err != nil {
		return AssistantApplication{}, err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := service.Store.seal(auditKey, map[string]any{
		"action": action, "workspace": p.Workspace, "id": app.ID, "time": time.Now().UTC(),
	})
	if err != nil {
		return AssistantApplication{}, err
	}
	tx, err := service.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return AssistantApplication{}, ErrUnavailable
	}
	defer tx.Rollback()
	for index, upload := range uploads {
		result, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES('assistant-image',?,?,?,1,?) ON CONFLICT DO NOTHING`, string(p.Workspace), string(app.ID), upload.ID, imageBodies[index])
		if err != nil {
			return AssistantApplication{}, ErrUnavailable
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return AssistantApplication{}, ErrConflict
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind='assistant' AND scope=? AND owner='workspace' AND id=? AND revision=?`, appBody, string(p.Workspace), string(app.ID), app.Revision)
	if err != nil {
		return AssistantApplication{}, ErrUnavailable
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return AssistantApplication{}, ErrConflict
	}
	for _, id := range obsolete {
		result, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind='assistant-image' AND scope=? AND owner=? AND id=?`, string(p.Workspace), string(app.ID), id)
		if err != nil {
			return AssistantApplication{}, ErrUnavailable
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return AssistantApplication{}, ErrUnavailable
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`, auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return AssistantApplication{}, ErrUnavailable
	}
	if tx.Commit() != nil {
		return AssistantApplication{}, ErrUnavailable
	}
	app.Revision++
	return app, nil
}

// Removing an application also removes its encrypted images. The audit and
// compare-and-swap delete must commit together with the image cleanup.
func (service *Service) deleteAssistantWithImages(ctx context.Context, workspace, id ID, revision int64) error {
	key := assistantKey(workspace, id)
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := service.Store.seal(auditKey, map[string]any{"action": "assistant.delete", "workspace": key.Scope, "id": key.ID, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	tx, err := service.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`, key.Kind, key.Scope, key.Owner, key.ID, revision)
	if err != nil {
		return ErrUnavailable
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind='assistant-image' AND scope=? AND owner=?`, string(workspace), string(id)); err != nil {
		return ErrUnavailable
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`, auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return ErrUnavailable
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}

func (h *Handler) assistantPicture(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	id := r.PathValue("file_id")
	if !validAssistantImageID(id) {
		writeJSON(w, http.StatusNotFound, "Image not found")
		return
	}
	if !h.appLookupLimit.Allow() {
		writeJSON(w, http.StatusTooManyRequests, "Too many image lookups")
		return
	}
	rows, err := h.service.Store.db.QueryContext(r.Context(), `SELECT scope,owner,body FROM chatbi_objects WHERE kind='assistant-image' AND id=? LIMIT 2`, id)
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	var scope, owner string
	var body []byte
	count := 0
	for rows.Next() {
		count++
		if rows.Scan(&scope, &owner, &body) != nil {
			_ = rows.Close()
			failure(w, ErrUnavailable)
			return
		}
	}
	readErr := rows.Err()
	_ = rows.Close()
	if readErr != nil || count > 1 {
		failure(w, ErrUnavailable)
		return
	}
	if count == 0 || !ID(scope).Valid() || !ID(owner).Valid() {
		writeJSON(w, http.StatusNotFound, "Image not found")
		return
	}
	var stored assistantImage
	if h.service.Store.decode(objectKey{"assistant-image", scope, owner, id}, body, &stored) != nil ||
		len(stored.Data) == 0 || len(stored.Data) > maxAssistantImageBytes ||
		(stored.MIME != "image/png" && stored.MIME != "image/jpeg") {
		failure(w, ErrUnavailable)
		return
	}
	var app AssistantApplication
	if _, err := h.service.Store.get(r.Context(), assistantKey(ID(scope), ID(owner)), &app); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusNotFound, "Image not found")
		} else {
			failure(w, err)
		}
		return
	}
	var config struct {
		Logo      string `json:"logo"`
		FloatIcon string `json:"float_icon"`
	}
	if app.ID != ID(owner) || app.Workspace != ID(scope) || !app.Enabled ||
		json.Unmarshal([]byte(app.Configuration), &config) != nil ||
		(config.Logo != id && config.FloatIcon != id) {
		writeJSON(w, http.StatusNotFound, "Image not found")
		return
	}
	w.Header().Set("Content-Type", stored.MIME)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(stored.Data)
}
