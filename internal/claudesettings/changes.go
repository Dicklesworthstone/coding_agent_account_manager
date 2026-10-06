package claudesettings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// ChangedKeys reports semantic changes without exposing setting values (which
// can contain credentials). Project changes are reported at the policy-field
// level, e.g. projects./work/app.allowedTools. Formatting does not count as a
// change, and numbers are kept losslessly, just as they are during merging.
func (u *Update) ChangedKeys() ([]string, error) {
	decode := func(data []byte) (map[string]interface{}, error) {
		obj := make(map[string]interface{})
		if data == nil {
			return obj, nil
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&obj); err != nil || obj == nil {
			return nil, fmt.Errorf("settings must contain a JSON object")
		}
		return obj, nil
	}
	before, err := decode(u.before)
	if err != nil {
		return nil, err
	}
	after, err := decode(u.after)
	if err != nil {
		return nil, err
	}
	var changed []string
	var diff func(map[string]interface{}, map[string]interface{}, string, int)
	diff = func(a, b map[string]interface{}, prefix string, depth int) {
		keys := make(map[string]bool, len(a)+len(b))
		for key := range a {
			keys[key] = true
		}
		for key := range b {
			keys[key] = true
		}
		for key := range keys {
			left, leftPresent := a[key]
			right, rightPresent := b[key]
			if leftPresent == rightPresent && reflect.DeepEqual(left, right) {
				continue
			}
			// Expand the projects map and then each project entry, but keep
			// individual policy values (including arrays) as whole fields.
			if (depth == 0 && key == "projects") || depth == 1 {
				leftMap, leftOK := left.(map[string]interface{})
				rightMap, rightOK := right.(map[string]interface{})
				if (left == nil || leftOK) && (right == nil || rightOK) {
					diff(leftMap, rightMap, prefix+key+".", depth+1)
					continue
				}
			}
			changed = append(changed, prefix+key)
		}
	}
	diff(before, after, "", 0)
	sort.Strings(changed)
	return changed, nil
}
