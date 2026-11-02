package zuiyou

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// One bounded decoding pass keeps the PHP array insertion order alongside the
// ordinary value tree. Map iteration must never decide imgs/videos/defaults.
type jsonNode struct {
	value    any
	keys     []string
	children map[string]*jsonNode
}

func (n *jsonNode) valueOf() any {
	if n == nil {
		return nil
	}
	return n.value
}

func (n *jsonNode) isArray() bool {
	return n != nil && (unified.Map(n.value) != nil || unified.Slice(n.value) != nil)
}

func (n *jsonNode) keysOf() []string {
	if !n.isArray() {
		return nil
	}
	return n.keys
}

func (n *jsonNode) has(key string) bool {
	if !n.isArray() {
		return false
	}
	_, exists := n.children[key]
	return exists
}

func (n *jsonNode) child(path ...string) *jsonNode {
	for _, key := range path {
		if n == nil {
			return nil
		}
		n = n.children[key]
	}
	return n
}

func (n *jsonNode) values() []*jsonNode {
	values := []*jsonNode{}
	for _, key := range n.keysOf() {
		values = append(values, n.children[key])
	}
	return values
}

func decodeDocument(raw []byte) (*jsonNode, error) {
	if !utf8.Valid(raw) || !validSurrogates(raw) {
		return nil, fmt.Errorf("invalid PHP JSON Unicode")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var read func(int) (*jsonNode, error)
	read = func(depth int) (*jsonNode, error) {
		if depth >= 512 {
			return nil, fmt.Errorf("PHP JSON depth exceeded")
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		node := &jsonNode{value: token}
		if number, ok := token.(json.Number); ok {
			if !strings.ContainsAny(number.String(), ".eE") {
				if integer, err := number.Int64(); err == nil {
					node.value = integer
					return node, nil
				}
			}
			node.value, err = number.Float64()
			return node, err
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return node, nil
		}
		if delimiter != '{' && delimiter != '[' {
			return nil, fmt.Errorf("unexpected JSON delimiter")
		}
		node.keys, node.children = []string{}, map[string]*jsonNode{}
		for decoder.More() {
			key := strconv.Itoa(len(node.keys))
			if delimiter == '{' {
				name, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				var valid bool
				key, valid = name.(string)
				if !valid {
					return nil, fmt.Errorf("invalid JSON property")
				}
			}
			child, err := read(depth + 1)
			if err != nil {
				return nil, err
			}
			if _, exists := node.children[key]; !exists {
				node.keys = append(node.keys, key)
			}
			node.children[key] = child
		}
		end, err := decoder.Token()
		if err != nil || delimiter == '{' && end != json.Delim('}') || delimiter == '[' && end != json.Delim(']') {
			return nil, fmt.Errorf("unterminated JSON container")
		}
		// Associative PHP decoding re-encodes 0..n-1 in order as a list,
		// including {} -> []. Other numeric object orders remain objects.
		list := true
		for index, key := range node.keys {
			if key != strconv.Itoa(index) {
				list = false
				break
			}
		}
		if list {
			values := unified.Array{}
			for _, key := range node.keys {
				values = append(values, node.children[key].value)
			}
			node.value = values
		} else {
			fields := unified.Object{}
			for _, key := range node.keys {
				fields[key] = node.children[key].value
			}
			node.value = fields
		}
		return node, nil
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON data")
	}
	return value, nil
}

func validSurrogates(raw []byte) bool {
	inString := false
	for index := 0; index < len(raw); index++ {
		if raw[index] == '"' {
			inString = !inString
			continue
		}
		if !inString || raw[index] != '\\' {
			continue
		}
		index++
		if index >= len(raw) {
			return false
		}
		if raw[index] != 'u' {
			continue
		}
		if index+4 >= len(raw) {
			return false
		}
		unit, err := strconv.ParseUint(string(raw[index+1:index+5]), 16, 16)
		if err != nil {
			return false
		}
		index += 4
		if unit >= 0xdc00 && unit <= 0xdfff {
			return false
		}
		if unit >= 0xd800 && unit <= 0xdbff {
			if index+6 >= len(raw) || raw[index+1] != '\\' || raw[index+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[index+3:index+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			index += 6
		}
	}
	return true
}
