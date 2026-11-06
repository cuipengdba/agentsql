package chatbi

import "encoding/json"

// SQLBot clients send custom_model as an integer, a decimal string, null, or
// an empty string. Invalid IDs fail before any model or datasource is used.
func optionalQuestionModelID(raw json.RawMessage) (*ID, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `""` {
		return nil, nil
	}
	var id ID
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil, ErrModel
	}
	return &id, nil
}
