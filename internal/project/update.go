package project

import (
	"fmt"
	"slices"
	"strings"
)

// ParseFieldValue splits a "field=value" expression. Returns an error if
// no '=' is present.
func ParseFieldValue(s string) (string, string, error) {
	field, val, ok := strings.Cut(s, "=")
	if !ok {
		return "", "", fmt.Errorf("expected field=value, got %q", s)
	}
	return field, val, nil
}

// arrayField returns a pointer to the named array field on m, or nil if the
// field name is not recognized.
func arrayField(m *Manifest, name string) (*[]string, error) {
	switch name {
	case "phases_completed":
		return &m.PhasesCompleted, nil
	case "phases_remaining":
		return &m.PhasesRemaining, nil
	default:
		return nil, fmt.Errorf("unknown array field: %s", name)
	}
}

// ApplySet overwrites an array field. Empty value clears the slice.
func ApplySet(m *Manifest, expr string) error {
	field, val, err := ParseFieldValue(expr)
	if err != nil {
		return err
	}
	arr, err := arrayField(m, field)
	if err != nil {
		return err
	}
	if val == "" {
		*arr = []string{}
	} else {
		*arr = strings.Split(val, ",")
	}
	return nil
}

// ApplyAdd appends to an array field if the value is not already present.
func ApplyAdd(m *Manifest, expr string) error {
	field, val, err := ParseFieldValue(expr)
	if err != nil {
		return err
	}
	arr, err := arrayField(m, field)
	if err != nil {
		return err
	}
	if slices.Contains(*arr, val) {
		return nil
	}
	*arr = append(*arr, val)
	return nil
}

// ApplyRemove drops a value from an array field if present.
func ApplyRemove(m *Manifest, expr string) error {
	field, val, err := ParseFieldValue(expr)
	if err != nil {
		return err
	}
	arr, err := arrayField(m, field)
	if err != nil {
		return err
	}
	result := make([]string, 0, len(*arr))
	for _, v := range *arr {
		if v != val {
			result = append(result, v)
		}
	}
	*arr = result
	return nil
}
