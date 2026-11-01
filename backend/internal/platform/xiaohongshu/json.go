package xiaohongshu

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

// Retain object insertion order while applying PHP's default json_decode
// numeric boundary. XHS does not enable JSON_BIGINT_AS_STRING.
type jsonNode struct {
	value  any
	fields map[string]*jsonNode
	keys   []string
	list   []*jsonNode
}

func (n *jsonNode) field(path ...string) *jsonNode {
	for _, key := range path {
		if n == nil {
			return nil
		}
		n = n.fields[key]
	}
	return n
}
func (n *jsonNode) val() any {
	if n == nil {
		return nil
	}
	return n.value
}
func (n *jsonNode) array() bool { return n != nil && (n.fields != nil || n.list != nil) }
func (n *jsonNode) values() []*jsonNode {
	if n == nil {
		return nil
	}
	if n.list != nil {
		return n.list
	}
	values := make([]*jsonNode, 0, len(n.keys))
	for _, key := range n.keys {
		values = append(values, n.fields[key])
	}
	return values
}

func decodeNode(raw string) (*jsonNode, error) {
	if !utf8.ValidString(raw) || !validJSONSurrogates([]byte(raw)) {
		return nil, fmt.Errorf("invalid JSON Unicode")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	var read func(int) (*jsonNode, error)
	read = func(depth int) (*jsonNode, error) {
		if depth >= 512 {
			return nil, fmt.Errorf("upstream JSON depth exceeded")
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		n := &jsonNode{}
		switch token {
		case json.Delim('{'):
			n.fields = map[string]*jsonNode{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				name, ok := key.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				child, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				if _, exists := n.fields[name]; !exists {
					n.keys = append(n.keys, name)
				}
				n.fields[name] = child
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			object := unified.Object{}
			sequential := true
			array := unified.Array{}
			for i, key := range n.keys {
				value := n.fields[key].value
				object[key] = value
				array = append(array, value)
				sequential = sequential && key == strconv.Itoa(i)
			}
			n.value = object
			if sequential {
				n.value = array
			}
		case json.Delim('['):
			n.list = []*jsonNode{}
			values := unified.Array{}
			for decoder.More() {
				child, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				n.list = append(n.list, child)
				values = append(values, child.value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			n.value = values
		default:
			n.value = token
			if number, ok := token.(json.Number); ok {
				if integer, err := number.Int64(); err == nil && !strings.ContainsAny(number.String(), ".eE") {
					n.value = integer
				} else {
					v, err := number.Float64()
					if err != nil {
						return nil, err
					}
					n.value = v
				}
			}
		}
		return n, nil
	}
	n, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	return n, nil
}

func first(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func text(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "1"
		}
		return ""
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	case float64:
		s := strconv.FormatFloat(v, 'G', 14, 64)
		if i := strings.IndexByte(s, 'E'); i >= 0 {
			mantissa, exponent := s[:i], s[i+1:]
			if !strings.Contains(mantissa, ".") {
				mantissa += ".0"
			}
			sign := ""
			if strings.HasPrefix(exponent, "+") || strings.HasPrefix(exponent, "-") {
				sign, exponent = exponent[:1], exponent[1:]
			}
			exponent = strings.TrimLeft(exponent, "0")
			if exponent == "" {
				exponent = "0"
			}
			s = mantissa + "E" + sign + exponent
		}
		return s
	}
	return "Array"
}

func validJSONSurrogates(raw []byte) bool {
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
