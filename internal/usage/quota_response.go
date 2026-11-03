package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
)

// Subscription usage responses are small. Bound the entire document, including
// trailing bytes, before decoding: accepting a valid prefix can hide a truncated
// or concatenated response. Errors must not repeat provider-controlled data.
const maxUsageResponseBytes = 1 << 20

func decodeUsageResponse(reader io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, maxUsageResponseBytes+1))
	if err != nil {
		return fmt.Errorf("usage response could not be read completely")
	}
	if len(data) > maxUsageResponseBytes {
		return fmt.Errorf("usage response exceeds the size limit")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' || json.Unmarshal(data, target) != nil {
		return fmt.Errorf("usage response is not a complete valid quota document")
	}
	return nil
}

// A present window needs an explicit measurement. Providers that do not
// distinguish inapplicable windows with null must still reject null numbers.
func requireUsagePercent(data []byte, field string) error {
	percent, err := nullableUsagePercent(data, field)
	if err != nil {
		return err
	}
	if percent == nil {
		return fmt.Errorf("usage window %s must be a number between 0 and 100", field)
	}
	return nil
}

// nullableUsagePercent distinguishes an explicit null from a missing or
// malformed measurement. When accepting null for an inapplicable window,
// callers must skip that window, never turn it into measured zero usage.
// Missing fields, strings, and out-of-range numbers remain invalid.
func nullableUsagePercent(data []byte, field string) (*float64, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return nil, fmt.Errorf("usage window must be an object")
	}
	var percent *float64
	if json.Unmarshal(object[field], &percent) != nil || (percent != nil &&
		(math.IsNaN(*percent) || math.IsInf(*percent, 0) || *percent < 0 || *percent > 100)) {
		return nil, fmt.Errorf("usage window %s must be a number between 0 and 100", field)
	}
	return percent, nil
}
