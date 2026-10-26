package chatbi

import (
	"net/http"
	"strconv"
	"strings"
)

func semanticPageParams(r *http.Request) (int, int, bool) {
	page, err1 := strconv.Atoi(r.PathValue("page"))
	size, err2 := strconv.Atoi(r.PathValue("size"))
	return page, size, err1 == nil && err2 == nil && page > 0 && page <= 100000 && size > 0 && size <= 100
}

func pagedSemantics[T any](items []T, page, size int) map[string]any {
	total := len(items)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	return map[string]any{"current_page": page, "page_size": size, "total_count": total, "total_pages": (total + size - 1) / size, "data": items[start:end]}
}

func (h *Handler) terminologyPage(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	page, size, valid := semanticPageParams(r)
	if !valid {
		writeJSON(w, 422, "Invalid pagination")
		return
	}
	terms, err := h.service.Terminologies(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	word := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("word")))
	filtered := make([]Terminology, 0, len(terms))
	for _, term := range terms {
		if word == "" || strings.Contains(strings.ToLower(term.Word), word) {
			filtered = append(filtered, term)
		}
	}
	okay(w, pagedSemantics(filtered, page, size))
}

func (h *Handler) examplePage(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	page, size, valid := semanticPageParams(r)
	if !valid {
		writeJSON(w, 422, "Invalid pagination")
		return
	}
	examples, err := h.service.Examples(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	question := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("question")))
	filtered := make([]SQLExample, 0, len(examples))
	for _, example := range examples {
		if question == "" || strings.Contains(strings.ToLower(example.Question), question) {
			filtered = append(filtered, example)
		}
	}
	okay(w, pagedSemantics(filtered, page, size))
}

func (h *Handler) saveTerminology(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	term := Terminology{Enabled: true}
	if !readBody(w, r, &term) {
		return
	}
	id, err := h.service.SaveTerminology(r.Context(), p, term)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, id)
}

func (h *Handler) saveExample(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	example := SQLExample{Enabled: true}
	if !readBody(w, r, &example) {
		return
	}
	if example.Workspace == 0 {
		example.Workspace, _ = strconv.ParseInt(string(p.Workspace), 10, 64)
	}
	id, err := h.service.SaveExample(r.Context(), p, example)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, id)
}

func (h *Handler) setSemanticEnabled(w http.ResponseWriter, r *http.Request, p Principal, kind string) {
	id, err1 := strconv.ParseInt(r.PathValue("id"), 10, 64)
	enabled, err2 := strconv.ParseBool(r.PathValue("enabled"))
	if err1 != nil || err2 != nil || id <= 0 {
		writeJSON(w, 422, "Invalid ID or enabled flag")
		return
	}
	if err := h.service.SetSemanticEnabled(r.Context(), p, kind, id, enabled); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (h *Handler) enableTerminology(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	h.setSemanticEnabled(w, r, p, "terminology")
}
func (h *Handler) enableExample(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	h.setSemanticEnabled(w, r, p, "sql-example")
}

func (h *Handler) deleteSemantics(w http.ResponseWriter, r *http.Request, p Principal, kind string) {
	var ids []int64
	if !readBody(w, r, &ids) {
		return
	}
	if len(ids) == 0 || len(ids) > 100 {
		writeJSON(w, 422, "Invalid ID list")
		return
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			writeJSON(w, 422, "Invalid ID list")
			return
		}
		seen[id] = true
	}
	if err := h.service.DeleteSemanticBatch(r.Context(), p, kind, ids); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (h *Handler) deleteTerminologies(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	h.deleteSemantics(w, r, p, "terminology")
}
func (h *Handler) deleteExamples(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	h.deleteSemantics(w, r, p, "sql-example")
}
