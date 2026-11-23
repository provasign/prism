package cli

import (
	"bytes"
	"encoding/json"
	"sort"
)

// encodeJSONFile renders doc for a config file another tool also owns
// (CON-07). encoding/json escaped & < > as & and sorted every
// object's keys, so writing one Prism entry rewrote the user's hook
// commands and reordered their file. This keeps each object's keys in the
// order original has them (new keys after, sorted), does not escape HTML,
// and indents two spaces like MarshalIndent. No trailing newline.
func encodeJSONFile(doc any, original []byte) ([]byte, error) {
	var b bytes.Buffer
	if err := encodeOrdered(&b, doc, bytes.TrimSpace(original), ""); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func encodeOrdered(b *bytes.Buffer, v any, orig []byte, indent string) error {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			b.WriteString("{}")
			return nil
		}
		order, children := objectLayout(orig)
		keys := make([]string, 0, len(x))
		seen := map[string]bool{}
		for _, k := range order {
			if _, ok := x[k]; ok && !seen[k] {
				keys = append(keys, k)
				seen[k] = true
			}
		}
		var extra []string
		for k := range x {
			if !seen[k] {
				extra = append(extra, k)
			}
		}
		sort.Strings(extra)
		keys = append(keys, extra...)
		inner := indent + "  "
		b.WriteString("{\n")
		for i, k := range keys {
			b.WriteString(inner)
			if err := encodeScalar(b, k); err != nil {
				return err
			}
			b.WriteString(": ")
			if err := encodeOrdered(b, x[k], children[k], inner); err != nil {
				return err
			}
			if i < len(keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "}")
		return nil
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return nil
		}
		var elems []json.RawMessage
		_ = json.Unmarshal(orig, &elems)
		inner := indent + "  "
		b.WriteString("[\n")
		for i, e := range x {
			var hint []byte
			if i < len(elems) {
				hint = elems[i]
			}
			b.WriteString(inner)
			if err := encodeOrdered(b, e, hint, inner); err != nil {
				return err
			}
			if i < len(x)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "]")
		return nil
	default:
		// Typed values (a struct, []string) keep their own field order.
		raw, err := marshalNoEscape(v)
		if err != nil {
			return err
		}
		if len(raw) > 0 && (raw[0] == '{' || raw[0] == '[') {
			var out bytes.Buffer
			if err := json.Indent(&out, raw, indent, "  "); err == nil {
				raw = out.Bytes()
			}
		}
		b.Write(raw)
		return nil
	}
}

func encodeScalar(b *bytes.Buffer, v any) error {
	raw, err := marshalNoEscape(v)
	if err != nil {
		return err
	}
	b.Write(raw)
	return nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// objectLayout returns an object's keys in source order and each key's raw
// value; empty when raw is not an object.
func objectLayout(raw []byte) ([]string, map[string][]byte) {
	children := map[string][]byte{}
	if len(raw) == 0 || raw[0] != '{' {
		return nil, children
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil, children
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var order []string
	depth := 0
	expectKey := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				depth++
				expectKey = depth == 1 && t == '{'
			case '}', ']':
				depth--
				expectKey = depth == 1
			}
			continue
		case string:
			if depth == 1 && expectKey {
				order = append(order, t)
				expectKey = false
				continue
			}
		}
		if depth == 1 {
			expectKey = true
		}
	}
	for k, v := range m {
		children[k] = v
	}
	return order, children
}
