package weibo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// weiboURLKeysFromRaw reads source-key order from bounded upstream JSON,
// rather than iterating Go maps. Repeated keys retain their first insertion
// position as in PHP. The function does not authorize any source URL.
func weiboURLKeysFromRaw(raw []byte, path ...string) ([]string, error) {
	if len(raw) > 2<<20 {
		return nil, fmt.Errorf("weibo source JSON exceeds metadata budget")
	}
	current := json.RawMessage(raw)
	for _, field := range path {
		object, empty, err := weiboRawObject(current)
		if err != nil {
			return nil, err
		}
		if empty {
			return []string{}, nil
		}
		next, exists := object[field]
		if !exists {
			return []string{}, nil
		}
		current = next
	}
	value := bytes.TrimSpace(current)
	if _, empty, err := weiboRawObject(value); err != nil {
		return nil, err
	} else if empty {
		return []string{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, fmt.Errorf("weibo urls must be a JSON object: %v", err)
	}
	keys := make([]string, 0)
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("weibo JSON property key must be a string")
		}
		var ignored json.RawMessage
		if err := decoder.Decode(&ignored); err != nil {
			return nil, err
		}
		if !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
		if len(keys) > 4096 {
			return nil, fmt.Errorf("weibo JSON contains too many URLs")
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, fmt.Errorf("unterminated weibo URL object: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("trailing weibo URL data")
	}
	return keys, nil
}

func weiboRawObject(raw json.RawMessage) (map[string]json.RawMessage, bool, error) {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 || bytes.Equal(value, []byte("null")) {
		return nil, true, nil
	}
	if value[0] == '[' {
		var list []json.RawMessage
		if err := json.Unmarshal(value, &list); err != nil {
			return nil, false, err
		}
		if len(list) == 0 {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("nonempty array cannot replace ordered weibo object")
	}
	if value[0] != '{' {
		return nil, false, fmt.Errorf("weibo JSON must be an object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil {
		return nil, false, err
	}
	return object, false, nil
}
