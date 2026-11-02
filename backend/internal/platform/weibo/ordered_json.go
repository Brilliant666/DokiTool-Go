package weibo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
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
	if err != nil || (start != json.Delim('{') && start != json.Delim('[')) {
		return nil, fmt.Errorf("weibo urls must be a JSON object or array: %v", err)
	}
	object := start == json.Delim('{')
	endDelim := json.Delim(']')
	if object {
		endDelim = json.Delim('}')
	}
	keys := make([]string, 0)
	seen := make(map[string]bool)
	for decoder.More() {
		key := strconv.Itoa(len(keys))
		if object {
			token, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			var ok bool
			key, ok = token.(string)
			if !ok {
				return nil, fmt.Errorf("weibo JSON property key must be a string")
			}
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
	if err != nil || end != endDelim {
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
		object := make(map[string]json.RawMessage, len(list))
		for index, child := range list {
			object[strconv.Itoa(index)] = child
		}
		return object, len(list) == 0, nil
	}
	if value[0] != '{' {
		// Valid scalar/missing fields carry no source keys in PHP. They are
		// interpreted by the formatter's business-error/media checks; only
		// malformed JSON is an extraction error.
		var scalar json.RawMessage
		if err := json.Unmarshal(value, &scalar); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil {
		return nil, false, err
	}
	return object, false, nil
}
