package chatbi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type legacyEmbeddedUIInput struct {
	ID              ID      `json:"id"`
	Theme           *string `json:"theme"`
	HeaderFontColor *string `json:"header_font_color"`
	Logo            *string `json:"logo"`
	FloatIcon       *string `json:"float_icon"`
	FloatIconDrag   *bool   `json:"float_icon_drag"`
	XType           *string `json:"x_type"`
	XVal            *int    `json:"x_val"`
	YType           *string `json:"y_type"`
	YVal            *int    `json:"y_val"`
	Name            *string `json:"name"`
	Welcome         *string `json:"welcome"`
	WelcomeDesc     *string `json:"welcome_desc"`
}

// Read the original multipart form without spilling uploads into a host temp
// directory. Image persistence has a separate contract; reject files here.
func readLegacyEmbeddedUI(r *http.Request) (legacyEmbeddedUIInput, error) {
	var input legacyEmbeddedUIInput
	reader, err := r.MultipartReader()
	if err != nil {
		return input, ErrInvalidAssistant
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "data" || part.FileName() != "" {
		return input, ErrInvalidAssistant
	}
	data, err := io.ReadAll(io.LimitReader(part, (16<<10)+1))
	_ = part.Close()
	if err != nil || len(data) == 0 || len(data) > 16<<10 {
		return input, ErrInvalidAssistant
	}
	if next, err := reader.NextPart(); err != io.EOF || next != nil {
		if next != nil {
			_ = next.Close()
		}
		return input, ErrInvalidAssistant
	}
	if json.Unmarshal(data, &input) != nil || !input.ID.Valid() {
		return input, ErrInvalidAssistant
	}
	if input.Logo != nil && *input.Logo != "" || input.FloatIcon != nil && *input.FloatIcon != "" {
		return input, ErrInvalidAssistant
	}
	if input.Theme != nil && !validEmbeddedUIColor(*input.Theme) ||
		input.HeaderFontColor != nil && !validEmbeddedUIColor(*input.HeaderFontColor) ||
		input.XType != nil && *input.XType != "left" && *input.XType != "right" ||
		input.YType != nil && *input.YType != "top" && *input.YType != "bottom" ||
		input.XVal != nil && (*input.XVal < 0 || *input.XVal > 4096) ||
		input.YVal != nil && (*input.YVal < 0 || *input.YVal > 4096) ||
		input.Name != nil && !validEmbeddedUIText(*input.Name, 120) ||
		input.Welcome != nil && !validEmbeddedUIText(*input.Welcome, 500) ||
		input.WelcomeDesc != nil && !validEmbeddedUIText(*input.WelcomeDesc, 2000) {
		return input, ErrInvalidAssistant
	}
	return input, nil
}

func validEmbeddedUIColor(value string) bool {
	if len(value) != 7 || value[0] != '#' {
		return false
	}
	for _, character := range value[1:] {
		if !(character >= '0' && character <= '9' ||
			character >= 'A' && character <= 'F' ||
			character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validEmbeddedUIText(value string, limit int) bool {
	return len([]rune(value)) <= limit && !strings.ContainsAny(value, "\x00<>")
}

func (h *Handler) legacyEmbeddedUI(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	input, err := readLegacyEmbeddedUI(r)
	if err != nil {
		failure(w, err)
		return
	}
	app, err := h.service.Assistant(r.Context(), p, input.ID)
	if err != nil {
		failure(w, err)
		return
	}
	if app.Type != 4 {
		failure(w, ErrInvalidAssistant)
		return
	}
	config := make(map[string]json.RawMessage)
	if app.Configuration != "" && (json.Unmarshal([]byte(app.Configuration), &config) != nil || config == nil) {
		failure(w, ErrUnavailable)
		return
	}
	set := func(name string, value any) {
		encoded, _ := json.Marshal(value)
		config[name] = encoded
	}
	if input.Theme != nil {
		set("theme", *input.Theme)
	}
	if input.HeaderFontColor != nil {
		set("header_font_color", *input.HeaderFontColor)
	}
	if input.Logo != nil {
		set("logo", *input.Logo)
	}
	if input.FloatIcon != nil {
		set("float_icon", *input.FloatIcon)
	}
	if input.FloatIconDrag != nil {
		set("float_icon_drag", *input.FloatIconDrag)
	}
	if input.XType != nil {
		set("x_type", *input.XType)
	}
	if input.XVal != nil {
		set("x_val", *input.XVal)
	}
	if input.YType != nil {
		set("y_type", *input.YType)
	}
	if input.YVal != nil {
		set("y_val", *input.YVal)
	}
	if input.Name != nil {
		set("name", *input.Name)
	}
	if input.Welcome != nil {
		set("welcome", *input.Welcome)
	}
	if input.WelcomeDesc != nil {
		set("welcome_desc", *input.WelcomeDesc)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	app.Configuration = string(encoded)
	saved, err := h.service.SaveAssistant(r.Context(), p, app)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, legacyEmbeddedView(saved))
}
