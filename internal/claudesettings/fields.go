package claudesettings

import (
	"encoding/json"
	"fmt"
)

// PrepareFieldsRestore replaces only named account fields in a mixed document.
// It is used for Desktop OAuth caches: absence of a snapshot/cache must remove
// the outgoing account's cache without reverting unrelated Desktop preferences.
// Unlike settings.json, other fields (including env) have no assumed schema.
func PrepareFieldsRestore(snapshotPath, livePath string, keys []string) (*Update, error) {
	for _, key := range keys {
		if key == "" {
			return nil, fmt.Errorf("account field name must not be empty")
		}
	}
	return prepare(snapshotPath, livePath, livePath, Policy{}, func(shared, account []byte, _ Policy) ([]byte, error) {
		decode := func(data []byte) (map[string]json.RawMessage, error) {
			obj := make(map[string]json.RawMessage)
			if data == nil {
				return obj, nil
			}
			if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
				return nil, fmt.Errorf("account document must contain a JSON object")
			}
			return obj, nil
		}
		live, err := decode(shared)
		if err != nil {
			return nil, fmt.Errorf("live account document: %w", err)
		}
		target, err := decode(account)
		if err != nil {
			return nil, fmt.Errorf("snapshot account document: %w", err)
		}
		if shared == nil && account == nil {
			return nil, nil
		}
		for _, key := range keys {
			delete(live, key)
			if value, exists := target[key]; exists {
				live[key] = value
			}
		}
		data, err := json.MarshalIndent(live, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(data, '\n'), nil
	})
}
