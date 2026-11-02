// Package publicoracle applies only declared, exact changes to frozen PHP public
// expectations. It is imported by tests, never by the production API. It does
// not call the Go projection whose output those tests are checking.
package publicoracle

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
)

const DouyinFixture = "backend/testdata/php-douyin/cases.json"
const IntegerFixture = "backend/internal/platform/douyin/testdata/php-integer-boundaries.json"

//go:embed differences.json
var declared []byte

type difference struct {
	Fixture   string `json:"fixture"`
	CaseID    string `json:"case_id"`
	View      string `json:"view"`
	Path      string `json:"path"`
	Operation string `json:"operation"`
	Before    any    `json:"before"`
	After     any    `json:"after,omitempty"`
	Reason    string `json:"reason"`
}

// Oracle tracks which declarations a complete fixture comparison has consumed.
type Oracle struct {
	changes []difference
	used    map[int]bool
}

func decode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON value")
	}
	return nil
}

// New reads the test-only manifest, retaining exact integer literals.
func New() (*Oracle, error) { return load(declared) }

func load(data []byte) (*Oracle, error) {
	var manifest struct {
		Version int          `json:"format_version"`
		Changes []difference `json:"differences"`
	}
	if err := decode(data, &manifest); err != nil {
		return nil, err
	}
	if manifest.Version != 1 || len(manifest.Changes) == 0 {
		return nil, fmt.Errorf("missing public oracle differences")
	}
	seen := map[string]bool{}
	for _, change := range manifest.Changes {
		key := strings.Join([]string{change.Fixture, change.CaseID, change.View, change.Path}, "\x00")
		if seen[key] {
			return nil, fmt.Errorf("duplicate public oracle difference: %s", change.CaseID)
		}
		seen[key] = true
		if (change.Fixture != DouyinFixture && change.Fixture != IntegerFixture) || change.CaseID == "" ||
			(change.View != "normal" && change.View != "debug") || !strings.HasPrefix(change.Path, "/") ||
			change.Reason == "" || change.Before == nil {
			return nil, fmt.Errorf("invalid public oracle difference: %s", change.CaseID)
		}
		if (change.Operation != "replace" && change.Operation != "remove") ||
			(change.Operation == "replace" && change.After == nil) || (change.Operation == "remove" && change.After != nil) {
			return nil, fmt.Errorf("invalid public oracle operation: %s", change.CaseID)
		}
	}
	return &Oracle{changes: manifest.Changes, used: map[int]bool{}}, nil
}

func member(root any, pointer string) (map[string]any, string, error) {
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	current := root
	for index, token := range parts {
		// These declarations use literal field names and array indices only.
		// Reject unsupported pointer escaping instead of silently targeting a
		// different field when a future declaration is mistyped.
		if strings.Contains(token, "~") || token == "" {
			return nil, "", fmt.Errorf("unsupported public oracle pointer: %s", pointer)
		}
		if index == len(parts)-1 {
			object, ok := current.(map[string]any)
			if !ok {
				return nil, "", fmt.Errorf("public oracle target is not an object: %s", pointer)
			}
			if _, ok := object[token]; !ok {
				return nil, "", fmt.Errorf("missing public oracle target: %s", pointer)
			}
			return object, token, nil
		}
		switch container := current.(type) {
		case map[string]any:
			value, exists := container[token]
			if !exists {
				return nil, "", fmt.Errorf("missing public oracle path: %s", pointer)
			}
			current = value
		case []any:
			position, err := strconv.Atoi(token)
			if err != nil || position < 0 || position >= len(container) || strconv.Itoa(position) != token {
				return nil, "", fmt.Errorf("invalid public oracle array index: %s", pointer)
			}
			current = container[position]
		default:
			return nil, "", fmt.Errorf("invalid public oracle path: %s", pointer)
		}
	}
	return nil, "", fmt.Errorf("empty public oracle pointer")
}

// Expected returns an independent copy of the frozen response. Only a matching
// declaration may change it, and its complete old value must match exactly.
// Callers still compare the entire actual response with this expectation.
func (oracle *Oracle) Expected(fixture, caseID, view string, frozen any) (map[string]any, error) {
	encoded, err := json.Marshal(frozen)
	if err != nil {
		return nil, err
	}
	var expected map[string]any
	if err := decode(encoded, &expected); err != nil {
		return nil, err
	}
	for index, change := range oracle.changes {
		if change.Fixture != fixture || change.CaseID != caseID || change.View != view {
			continue
		}
		object, key, err := member(expected, change.Path)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(object[key], change.Before) {
			return nil, fmt.Errorf("public oracle before mismatch: %s/%s %s", caseID, view, change.Path)
		}
		if change.Operation == "remove" {
			delete(object, key)
		} else {
			object[key] = change.After
		}
		oracle.used[index] = true
	}
	return expected, nil
}

// RequireConsumed rejects stale or misspelled declarations after all cases in
// a fixture have been checked. An empty fixture means all declarations.
func (oracle *Oracle) RequireConsumed(fixture string) error {
	for index, change := range oracle.changes {
		if (fixture == "" || change.Fixture == fixture) && !oracle.used[index] {
			return fmt.Errorf("unused public oracle difference: %s %s/%s %s", change.Fixture, change.CaseID, change.View, change.Path)
		}
	}
	return nil
}
