package providers

import "encoding/json"

// NumericUsageExtensions keeps token-detail and numeric provider extensions in
// the response. Free-form strings and arrays are not copied into diagnostics.
// This value is response-only and must never be persisted as an upstream blob.
func NumericUsageExtensions(value any) map[string]any {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var input map[string]any
	if json.Unmarshal(raw, &input) != nil {
		return nil
	}
	var filter func(map[string]any) map[string]any
	filter = func(source map[string]any) map[string]any {
		out := map[string]any{}
		for key, value := range source {
			switch node := value.(type) {
			case float64:
				out[key] = node
			case nil:
				out[key] = nil
			case map[string]any:
				out[key] = filter(node)
			}
		}
		return out
	}
	return filter(input)
}
