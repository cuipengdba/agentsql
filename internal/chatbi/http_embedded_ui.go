package chatbi

import (
	"bytes"
	"encoding/json"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"path"
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
// directory. The original browser sends files before its JSON data part.
func readLegacyEmbeddedUI(r *http.Request) (legacyEmbeddedUIInput, map[string]assistantImageUpload, error) {
	var input legacyEmbeddedUIInput
	reader, err := r.MultipartReader()
	if err != nil {
		return input, nil, ErrInvalidAssistant
	}
	var data []byte
	uploads := make(map[string]assistantImageUpload, 2)
	for count := 0; count < 4; count++ {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil || count == 3 {
			return input, nil, ErrInvalidAssistant
		}
		if part.FormName() == "data" && part.FileName() == "" && data == nil {
			data, err = io.ReadAll(io.LimitReader(part, (16<<10)+1))
			_ = part.Close()
			if err != nil || len(data) == 0 || len(data) > 16<<10 {
				return input, nil, ErrInvalidAssistant
			}
			continue
		}
		name := part.FileName()
		comma := strings.LastIndex(name, ",")
		if part.FormName() != "files" || comma < 1 || comma > 128 || strings.ContainsAny(name[:comma], "/\\\x00") {
			_ = part.Close()
			return input, nil, ErrInvalidAssistant
		}
		flag := name[comma+1:]
		if (flag != "logo" && flag != "float_icon") || uploads[flag].ID != "" {
			_ = part.Close()
			return input, nil, ErrInvalidAssistant
		}
		body, err := io.ReadAll(io.LimitReader(part, maxAssistantImageBytes+1))
		_ = part.Close()
		if err != nil || len(body) == 0 || len(body) > maxAssistantImageBytes {
			return input, nil, ErrInvalidAssistant
		}
		config, format, err := image.DecodeConfig(bytes.NewReader(body))
		if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 2048 || config.Height > 2048 {
			return input, nil, ErrInvalidAssistant
		}
		extension := strings.ToLower(path.Ext(name[:comma]))
		if format == "png" && extension != ".png" || format == "jpeg" && extension != ".jpg" && extension != ".jpeg" || format != "png" && format != "jpeg" {
			return input, nil, ErrInvalidAssistant
		}
		if _, decodedFormat, err := image.Decode(bytes.NewReader(body)); err != nil || decodedFormat != format {
			return input, nil, ErrInvalidAssistant
		}
		mime := "image/png"
		if format == "jpeg" {
			mime = "image/jpeg"
		}
		id, err := newAssistantImageID(mime)
		if err != nil {
			return input, nil, err
		}
		uploads[flag] = assistantImageUpload{ID: id, assistantImage: assistantImage{MIME: mime, Data: body}}
	}
	if json.Unmarshal(data, &input) != nil || !input.ID.Valid() {
		return input, nil, ErrInvalidAssistant
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
		return input, nil, ErrInvalidAssistant
	}
	return input, uploads, nil
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
	r.Body = http.MaxBytesReader(w, r.Body, (21<<20)+(64<<10))
	input, uploads, err := readLegacyEmbeddedUI(r)
	if err != nil {
		failure(w, err)
		return
	}
	app, err := h.service.Assistant(r.Context(), p, input.ID)
	if err != nil {
		failure(w, err)
		return
	}
	if app.Type != 0 && app.Type != 1 && app.Type != 4 {
		failure(w, ErrInvalidAssistant)
		return
	}
	config := make(map[string]json.RawMessage)
	if app.Configuration != "" && (json.Unmarshal([]byte(app.Configuration), &config) != nil || config == nil) {
		failure(w, ErrUnavailable)
		return
	}
	imageValue := func(name string) (string, error) {
		var value string
		if len(config[name]) > 0 && json.Unmarshal(config[name], &value) != nil {
			return "", ErrUnavailable
		}
		return value, nil
	}
	oldLogo, err := imageValue("logo")
	if err != nil {
		failure(w, err)
		return
	}
	oldFloatIcon, err := imageValue("float_icon")
	if err != nil {
		failure(w, err)
		return
	}
	if input.Logo != nil && *input.Logo != "" && *input.Logo != oldLogo ||
		input.FloatIcon != nil && *input.FloatIcon != "" && *input.FloatIcon != oldFloatIcon {
		failure(w, ErrInvalidAssistant)
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
	if uploaded := uploads["logo"]; uploaded.ID != "" {
		set("logo", uploaded.ID)
	} else if input.Logo != nil {
		set("logo", *input.Logo)
	}
	if uploaded := uploads["float_icon"]; uploaded.ID != "" {
		set("float_icon", uploaded.ID)
	} else if input.FloatIcon != nil {
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
	newLogo, err := imageValue("logo")
	if err != nil {
		failure(w, err)
		return
	}
	newFloatIcon, err := imageValue("float_icon")
	if err != nil {
		failure(w, err)
		return
	}
	obsolete := make([]string, 0, 2)
	seenObsolete := map[string]bool{}
	for _, oldID := range []string{oldLogo, oldFloatIcon} {
		if validAssistantImageID(oldID) && oldID != newLogo && oldID != newFloatIcon && !seenObsolete[oldID] {
			obsolete = append(obsolete, oldID)
			seenObsolete[oldID] = true
		}
	}
	newImages := make([]assistantImageUpload, 0, len(uploads))
	for _, flag := range []string{"logo", "float_icon"} {
		if uploaded := uploads[flag]; uploaded.ID != "" {
			newImages = append(newImages, uploaded)
		}
	}
	saved, err := h.service.saveAssistantUI(r.Context(), p, app, string(encoded), newImages, obsolete)
	if err != nil {
		failure(w, err)
		return
	}
	if saved.Type == 4 {
		okay(w, legacyEmbeddedView(saved))
		return
	}
	okay(w, legacyAssistantView(saved))
}
