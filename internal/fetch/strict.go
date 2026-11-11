package fetch

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// object decodes a JSON object strictly. A key that comes twice, in any
// case, fails it: services differ on which one they take, so den could
// judge one value while the service acts on another.
func object(body []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	out := map[string]json.RawMessage{}
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key := t.(string)
		if seen[strings.ToLower(key)] {
			return nil, errors.New("a key comes twice: " + key)
		}
		seen[strings.ToLower(key)] = true
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out[key] = v
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("more after the JSON object")
	}
	return out, nil
}

// field reads one field of a strict JSON object into v. It reports false
// when the body is not such an object or the field does not decode; a
// missing field leaves v as it is.
func field(body []byte, name string, v any) bool {
	obj, err := object(body)
	if err != nil {
		return false
	}
	raw, ok := obj[name]
	if !ok {
		return true
	}
	return json.Unmarshal(raw, v) == nil
}

// readOnly says whether a GraphQL document only reads: none of its
// operations is a mutation or a subscription. It reads the document's top
// level, skipping strings and comments, and takes a word it is unsure of
// as a change.
func readOnly(doc string) bool {
	depth := 0
	for i := 0; i < len(doc); i++ {
		c := doc[i]
		switch {
		case c == '#':
			for i < len(doc) && doc[i] != '\n' {
				i++
			}
		case strings.HasPrefix(doc[i:], `"""`):
			end := strings.Index(doc[i+3:], `"""`)
			if end < 0 {
				return false
			}
			i += end + 5
		case c == '"':
			for i++; i < len(doc) && doc[i] != '"'; i++ {
				if doc[i] == '\\' {
					i++
				}
			}
		case c == '{' || c == '(' || c == '[':
			depth++
		case c == '}' || c == ')' || c == ']':
			depth--
		case depth == 0 && (c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'):
			start := i
			for i < len(doc) && (doc[i] == '_' || doc[i] >= '0' && doc[i] <= '9' || doc[i] >= 'A' && doc[i] <= 'Z' || doc[i] >= 'a' && doc[i] <= 'z') {
				i++
			}
			if word := doc[start:i]; word == "mutation" || word == "subscription" {
				return false
			}
			i--
		}
	}
	return depth == 0
}
