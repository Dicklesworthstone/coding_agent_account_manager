package identity

import (
	"encoding/json"
	"fmt"
	"os"
)

// ExtractFromCursorAuth reads a Cursor auth.json or cli-config.json and
// returns a display identity when one is present.
//
// cli-config.json's authInfo is metadata. Callers must not treat a successful
// extract as proof the access token works or that this file is the account
// the CLI will actually use.
func ExtractFromCursorAuth(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read auth file: %w", err)
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse auth file: %w", err)
	}
	if info, ok := root["authInfo"].(map[string]interface{}); ok {
		if email := stringFromMap(info, "email"); email != "" {
			return &Identity{Email: email, Provider: "cursor"}, nil
		}
	}
	id, err := ExtractFromGenericAuth(path)
	if err != nil {
		return nil, err
	}
	if id != nil && id.Provider == "" {
		id.Provider = "cursor"
	}
	return id, nil
}
