package chatbi

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The AgentSQL-owned UI uses decimal string identifiers. The SQLBot
// compatibility endpoints retain their original numeric response contract;
// this separate API avoids losing 63-bit IDs in a JavaScript Number.
type nativeTerminology struct {
	ID              ID        `json:"id"`
	Word            string    `json:"word"`
	Description     string    `json:"description"`
	OtherWords      []string  `json:"other_words"`
	DatasourceIDs   []ID      `json:"datasource_ids"`
	DatasourceNames []string  `json:"datasource_names"`
	Enabled         bool      `json:"enabled"`
	Created         time.Time `json:"create_time"`
}

type nativeSQLExample struct {
	ID             ID        `json:"id"`
	Datasource     ID        `json:"datasource"`
	DatasourceName string    `json:"datasource_name"`
	Question       string    `json:"question"`
	Description    string    `json:"description"`
	Enabled        bool      `json:"enabled"`
	Created        time.Time `json:"create_time"`
}

func nativeSemanticPage(r *http.Request) (page, size int, search string, valid bool) {
	page, size = 1, 25
	if value := r.URL.Query().Get("page"); value != "" {
		page, _ = strconv.Atoi(value)
	}
	if value := r.URL.Query().Get("size"); value != "" {
		size, _ = strconv.Atoi(value)
	}
	search = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	return page, size, search, page > 0 && page <= 100000 && size > 0 && size <= 100 && len(search) <= 200
}

func nativeTermFromModel(term Terminology) nativeTerminology {
	ids := make([]ID, 0, len(term.DatasourceIDs))
	for _, id := range term.DatasourceIDs {
		ids = append(ids, ID(strconv.FormatInt(id, 10)))
	}
	return nativeTerminology{ID: ID(strconv.FormatInt(term.ID, 10)), Word: term.Word,
		Description: term.Description, OtherWords: term.OtherWords, DatasourceIDs: ids,
		DatasourceNames: term.DatasourceNames, Enabled: term.Enabled, Created: term.Created}
}

func nativeExampleFromModel(example SQLExample) nativeSQLExample {
	return nativeSQLExample{ID: ID(strconv.FormatInt(example.ID, 10)),
		Datasource: ID(strconv.FormatInt(example.Datasource, 10)), DatasourceName: example.DatasourceName,
		Question: example.Question, Description: example.Description, Enabled: example.Enabled, Created: example.Created}
}

func (h *Handler) nativeTerminologies(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	page, size, search, valid := nativeSemanticPage(r)
	if !valid {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid pagination")
		return
	}
	terms, err := h.service.Terminologies(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	filtered := make([]nativeTerminology, 0, len(terms))
	for _, term := range terms {
		if search == "" || strings.Contains(strings.ToLower(term.Word), search) || strings.Contains(strings.ToLower(term.Description), search) {
			filtered = append(filtered, nativeTermFromModel(term))
		}
	}
	okay(w, pagedSemantics(filtered, page, size))
}

func (h *Handler) nativeExamples(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	page, size, search, valid := nativeSemanticPage(r)
	if !valid {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid pagination")
		return
	}
	examples, err := h.service.Examples(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	filtered := make([]nativeSQLExample, 0, len(examples))
	for _, example := range examples {
		if search == "" || strings.Contains(strings.ToLower(example.Question), search) {
			filtered = append(filtered, nativeExampleFromModel(example))
		}
	}
	okay(w, pagedSemantics(filtered, page, size))
}

func parseNativeOptionalID(id ID) (int64, bool) {
	if id == "" {
		return 0, true
	}
	if !id.Valid() {
		return 0, false
	}
	value, _ := strconv.ParseInt(string(id), 10, 64)
	return value, true
}

func (h *Handler) nativeSaveTerminology(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	var input nativeTerminology
	if !readBody(w, r, &input) {
		return
	}
	id, valid := parseNativeOptionalID(input.ID)
	if !valid {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid terminology ID")
		return
	}
	ids := make([]int64, 0, len(input.DatasourceIDs))
	for _, source := range input.DatasourceIDs {
		if !source.Valid() {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
			return
		}
		parsed, _ := strconv.ParseInt(string(source), 10, 64)
		ids = append(ids, parsed)
	}
	saved, err := h.service.SaveTerminology(r.Context(), p, Terminology{ID: id, Word: input.Word,
		Description: input.Description, OtherWords: input.OtherWords, SpecificDS: len(ids) != 0,
		DatasourceIDs: ids, Enabled: input.Enabled})
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]ID{"id": ID(strconv.FormatInt(saved, 10))})
}

func (h *Handler) nativeSaveExample(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	var input nativeSQLExample
	if !readBody(w, r, &input) {
		return
	}
	id, valid := parseNativeOptionalID(input.ID)
	if !valid || !input.Datasource.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid example or datasource ID")
		return
	}
	datasource, _ := strconv.ParseInt(string(input.Datasource), 10, 64)
	workspace, err := strconv.ParseInt(string(p.Workspace), 10, 64)
	if err != nil {
		failure(w, ErrUnauthenticated)
		return
	}
	saved, err := h.service.SaveExample(r.Context(), p, SQLExample{ID: id, Workspace: workspace,
		Datasource: datasource, Question: input.Question, Description: input.Description, Enabled: input.Enabled})
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]ID{"id": ID(strconv.FormatInt(saved, 10))})
}

func nativeSemanticID(r *http.Request) (int64, bool) {
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		return 0, false
	}
	value, _ := strconv.ParseInt(string(id), 10, 64)
	return value, true
}

func (h *Handler) nativeDeleteSemantic(w http.ResponseWriter, r *http.Request, p Principal, kind string) {
	id, valid := nativeSemanticID(r)
	if !valid {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid semantic ID")
		return
	}
	if err := h.service.DeleteSemanticBatch(r.Context(), p, kind, []int64{id}); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (h *Handler) nativeEnableSemantic(w http.ResponseWriter, r *http.Request, p Principal, kind string) {
	id, valid := nativeSemanticID(r)
	if !valid {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid semantic ID")
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		writeJSON(w, http.StatusUnprocessableEntity, "Missing enabled flag")
		return
	}
	if err := h.service.SetSemanticEnabled(r.Context(), p, kind, id, *input.Enabled); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (h *Handler) nativeDeleteTerminology(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	h.nativeDeleteSemantic(w, r, p, "terminology")
}
func (h *Handler) nativeEnableTerminology(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	h.nativeEnableSemantic(w, r, p, "terminology")
}
func (h *Handler) nativeDeleteExample(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	h.nativeDeleteSemantic(w, r, p, "sql-example")
}
func (h *Handler) nativeEnableExample(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	h.nativeEnableSemantic(w, r, p, "sql-example")
}
