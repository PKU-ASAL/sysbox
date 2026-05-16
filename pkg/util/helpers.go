package util

// AsString extracts a string value from an any. Returns empty string
// for nil or non-string values.
func AsString(v any) string {
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}
