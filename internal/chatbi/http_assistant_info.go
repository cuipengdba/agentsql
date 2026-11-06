package chatbi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

// Only display settings may leave a public basic assistant. Management fields,
// host callbacks and unknown configuration keys remain server-side.
func publicAssistantConfiguration(raw string) (string, error) {
	if raw == "" {
		return "{}", nil
	}
	var source map[string]json.RawMessage
	var fields struct {
		Name            string `json:"name"`
		Welcome         string `json:"welcome"`
		WelcomeDesc     string `json:"welcome_desc"`
		Theme           string `json:"theme"`
		HeaderFontColor string `json:"header_font_color"`
		Logo            string `json:"logo"`
		FloatIcon       string `json:"float_icon"`
		FloatIconDrag   bool   `json:"float_icon_drag"`
		XType           string `json:"x_type"`
		XVal            int    `json:"x_val"`
		YType           string `json:"y_type"`
		YVal            int    `json:"y_val"`
		AutoDS          bool   `json:"auto_ds"`
	}
	if json.Unmarshal([]byte(raw), &source) != nil || source == nil ||
		json.Unmarshal([]byte(raw), &fields) != nil ||
		!validEmbeddedUIText(fields.Name, 120) || !validEmbeddedUIText(fields.Welcome, 500) ||
		!validEmbeddedUIText(fields.WelcomeDesc, 2000) ||
		(fields.Theme != "" && !validEmbeddedUIColor(fields.Theme)) ||
		(fields.HeaderFontColor != "" && !validEmbeddedUIColor(fields.HeaderFontColor)) ||
		(fields.Logo != "" && !validAssistantImageID(fields.Logo)) ||
		(fields.FloatIcon != "" && !validAssistantImageID(fields.FloatIcon)) ||
		(fields.XType != "" && fields.XType != "left" && fields.XType != "right") ||
		(fields.YType != "" && fields.YType != "top" && fields.YType != "bottom") ||
		fields.XVal < 0 || fields.XVal > 4096 || fields.YVal < 0 || fields.YVal > 4096 {
		return "", ErrUnavailable
	}
	allowed := map[string]bool{"name": true, "welcome": true, "welcome_desc": true,
		"theme": true, "header_font_color": true, "logo": true, "float_icon": true,
		"float_icon_drag": true, "x_type": true, "x_val": true, "y_type": true,
		"y_val": true, "auto_ds": true}
	public := make(map[string]json.RawMessage)
	for key, value := range source {
		if allowed[key] {
			public[key] = value
		}
	}
	encoded, err := json.Marshal(public)
	if err != nil {
		return "", ErrUnavailable
	}
	return string(encoded), nil
}

func requestAssistantOrigin(r *http.Request) string {
	values := r.Header.Values("Origin")
	if len(values) == 1 {
		return values[0]
	}
	if len(values) != 0 {
		return ""
	}
	referer := r.Header.Get("Referer")
	if referer == "" {
		return ""
	}
	parsed, err := url.Parse(referer)
	if err != nil || parsed.User != nil || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

// The original SDK's RSA header proves only payload format. This endpoint
// issues a usable token solely when an administrator has separately designated
// a non-admin guest with its own datasource bindings. Such data is public.
func (h *Handler) publicAssistantInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !h.appLookupLimit.Allow() {
		writeJSON(w, http.StatusTooManyRequests, "Too many embedding lookups")
		return
	}
	app, err := h.service.publicAssistantByID(r.Context(), ID(r.PathValue("id")))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusNotFound, "Application not found")
		} else {
			failure(w, err)
		}
		return
	}
	if app.Type != 0 || !app.PublicGuestUser.Valid() || len(app.DatasourceIDs) == 0 {
		failure(w, ErrUnauthenticated)
		return
	}
	origin := requestAssistantOrigin(r)
	if !assistantOriginAllowed(app.Domain, origin) {
		writeJSON(w, http.StatusForbidden, "Application origin is not allowed")
		return
	}
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Origin", origin)
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "SQLBOT-EMBEDDED-SIGN, Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	values := r.Header.Values("SQLBOT-EMBEDDED-SIGN")
	if len(values) != 1 {
		failure(w, ErrUnauthenticated)
		return
	}
	payload, err := decodeAssistantSign(h.service.Identity, values[0])
	if err != nil || validateAssistantSign(h.service.Identity, payload, app, origin, r.UserAgent()) != nil {
		failure(w, ErrUnauthenticated)
		return
	}
	configuration, err := publicAssistantConfiguration(app.Configuration)
	if err != nil {
		failure(w, err)
		return
	}
	token, err := h.service.issuePublicGuestToken(r.Context(), app)
	if err != nil {
		failure(w, err)
		return
	}
	view := legacyAssistantView(app)
	view["configuration"] = configuration
	view["token"] = token
	view["online"] = false
	okay(w, view)
}
