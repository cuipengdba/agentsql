package chatbi

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode"
)

func userImportHeaders(language string) []string {
	if strings.HasPrefix(strings.ToLower(language), "en") {
		return []string{"Account", "Name", "Email", "Workspace", "Role", "User Status", "User Source", "External User Unique Identifier"}
	}
	return []string{"账号", "姓名", "邮箱", "工作空间", "角色", "用户状态", "用户来源", "外部用户唯一标识"}
}

func (h *Handler) legacyUserImportTemplate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	options, err := h.service.allWorkspaceOptions(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	workspace := ""
	for _, option := range options {
		if option.ID == p.Workspace {
			workspace = option.Name
			break
		}
	}
	if workspace == "" {
		failure(w, ErrUnavailable)
		return
	}
	rows := [][]string{userImportHeaders(p.User.Language)}
	if strings.HasPrefix(strings.ToLower(p.User.Language), "en") {
		rows = append(rows, []string{"example_member", "Example Member", "member@example.invalid", workspace, "Member", "Enabled", "Local", ""})
	} else {
		rows = append(rows, []string{"example_member", "示例成员", "member@example.invalid", workspace, "普通成员", "已启用", "本地创建", ""})
	}
	data, err := semanticWorkbook(rows)
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="agentsql-users.xlsx"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func userImportWorkspaceOptions(options []workspaceOption) map[string]ID {
	result := make(map[string]ID, len(options))
	for _, option := range options {
		name := strings.TrimSpace(option.Name)
		if previous, exists := result[name]; exists && previous != option.ID {
			result[name] = ""
		} else if !exists {
			result[name] = option.ID
		}
	}
	return result
}

func userImportCandidate(row []string, workspaces map[string]ID, id ID) (User, bool) {
	if len(row) != 8 || !id.Valid() {
		return User{}, false
	}
	account, name, email := strings.TrimSpace(row[0]), strings.TrimSpace(row[1]), strings.TrimSpace(row[2])
	workspace := workspaces[strings.TrimSpace(row[3])]
	if account == "" || len(account) > 100 || name == "" || len(name) > 100 || len(email) > 100 || !workspace.Valid() ||
		strings.IndexFunc(account, unicode.IsSpace) >= 0 || strings.ContainsAny(account+name+email, "\r\n\x00") ||
		strings.TrimSpace(row[7]) != "" {
		return User{}, false
	}
	if email != "" {
		address, err := mail.ParseAddress(email)
		if err != nil || address.Address != email {
			return User{}, false
		}
	}
	switch strings.TrimSpace(row[4]) {
	case "管理员", "Administrator":
		// Global administrators cannot yet be demoted or deleted through the
		// original user lifecycle. Never create an irreversible one in bulk.
		return User{}, false
	case "普通成员", "Member":
	default:
		return User{}, false
	}
	status := 1
	switch strings.TrimSpace(row[5]) {
	case "已启用", "Enabled":
	case "已禁用", "Disabled":
		status = 0
	default:
		return User{}, false
	}
	if source := strings.TrimSpace(row[6]); source != "本地创建" && source != "Local" {
		return User{}, false
	}
	user := User{ID: id, Account: account, Name: name, Email: email, Workspace: workspace,
		Language: "zh-CN", Status: status, Origin: 0,
		Memberships:    []Membership{{Workspace: workspace, Name: strings.TrimSpace(row[3])}},
		PasswordFormat: "bcrypt", AuthRevision: 1, Created: time.Now().UTC().UnixMilli()}
	return user, true
}

func (h *Handler) legacyUserBatchImport(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	if len(h.initialUserPassword) < 12 || len(h.initialUserPassword) > 72 {
		writeJSON(w, http.StatusConflict, "Configure AGENTSQL_CHATBI_INITIAL_USER_PASSWORD before importing users")
		return
	}
	data, err := readSemanticUpload(w, r)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid or oversized XLSX upload")
		return
	}
	rows, err := readSemanticWorkbook(data, 8)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid or unsupported XLSX workbook")
		return
	}
	options, err := h.service.allWorkspaceOptions(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	workspaces := userImportWorkspaceOptions(options)
	success, failed := 0, 0
	failedRows := make([][]string, 0)
	reason := "Invalid or duplicate user"
	if !strings.HasPrefix(strings.ToLower(p.User.Language), "en") {
		reason = "用户信息无效或账号重复"
	}
	for _, row := range rows {
		blank := true
		for _, cell := range row {
			if strings.TrimSpace(cell) != "" {
				blank = false
				break
			}
		}
		if blank {
			continue
		}
		if err := h.currentIdentity(r.Context(), p, token); err != nil {
			failure(w, err)
			return
		}
		id, err := newID()
		if err != nil {
			failure(w, err)
			return
		}
		user, valid := userImportCandidate(row, workspaces, id)
		if valid {
			user.PasswordHash, err = HashPassword(h.initialUserPassword)
			if err == nil {
				err = h.service.Store.CreateUserManaged(r.Context(), user, p)
			}
		}
		if !valid || errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidAccountChange) {
			failed++
			failedRows = append(failedRows, append(append([]string(nil), row...), reason))
			continue
		}
		if err != nil {
			failure(w, err)
			return
		}
		success++
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var key any
	if failed != 0 {
		id, err := h.saveUserImportErrors(r, p, failedRows)
		if err != nil {
			failure(w, err)
			return
		}
		key = id
	}
	okay(w, map[string]any{"successCount": success, "errorCount": failed, "dataKey": key})
}

func userImportErrorKey(p Principal) objectKey {
	return objectKey{"user-import-error", string(p.Workspace), string(p.User.ID), "latest"}
}

func (h *Handler) saveUserImportErrors(r *http.Request, p Principal, failedRows [][]string) (string, error) {
	if len(failedRows) == 0 || len(failedRows) > 1000 {
		return "", ErrUnavailable
	}
	errorHeader := "错误原因"
	if strings.HasPrefix(strings.ToLower(p.User.Language), "en") {
		errorHeader = "Error"
	}
	rows := append([][]string{append(userImportHeaders(p.User.Language), errorHeader)}, failedRows...)
	workbook, err := semanticWorkbook(rows)
	if err != nil {
		return "", err
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	report := semanticErrorReport{Filename: string(id) + ".xlsx", Workbook: workbook,
		Kind: "user-import", Rows: len(failedRows), Expires: time.Now().UTC().Add(7 * 24 * time.Hour)}
	var prior semanticErrorReport
	revision, err := h.service.Store.get(r.Context(), userImportErrorKey(p), &prior)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}
	if err := h.service.Store.writeSemantic(r.Context(), userImportErrorKey(p), report, revision, "user-import.error-write"); err != nil {
		return "", err
	}
	return string(id), nil
}

func (h *Handler) legacyUserImportErrors(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		failure(w, ErrNotFound)
		return
	}
	var report semanticErrorReport
	if _, err := h.service.Store.get(r.Context(), userImportErrorKey(p), &report); err != nil {
		failure(w, err)
		return
	}
	if report.Kind != "user-import" || report.Filename != string(id)+".xlsx" || report.Expires.Before(time.Now().UTC()) ||
		len(report.Workbook) < 4 || len(report.Workbook) > maxSemanticWorkbook || report.Rows < 1 || report.Rows > 1000 {
		failure(w, ErrNotFound)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.auditSemanticErrorDownload(r.Context(), p, report.Filename, report.Rows); err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="agentsql-user-import-errors.xlsx"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(report.Workbook)
}
