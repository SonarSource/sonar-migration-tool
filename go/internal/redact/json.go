// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package redact

import (
	"bytes"
	"encoding/json"
	"io"
)

type member struct {
	key string
	val any
}

// object keeps JSON member order so redacted output diffs cleanly.
type object []member

func redactJSON(body []byte, scm bool) []byte {
	if !Enabled() || len(body) == 0 {
		return body
	}
	root, err := parseDocument(body)
	if err != nil {
		return body
	}
	var buf bytes.Buffer
	buf.Grow(len(body))
	w := &jsonWriter{buf: &buf}
	w.enc = json.NewEncoder(&buf)
	w.enc.SetEscapeHTML(false)
	w.write(redactNode(root, scm))
	return buf.Bytes()
}

func parseDocument(body []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	root, err := parseValue(dec, tok)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, io.ErrUnexpectedEOF
	}
	return root, nil
}

func parseValue(dec *json.Decoder, tok json.Token) (any, error) {
	d, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	if d == '{' {
		return parseObject(dec)
	}
	return parseArray(dec)
}

func parseObject(dec *json.Decoder) (any, error) {
	obj := object{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		val, err := parseNext(dec)
		if err != nil {
			return nil, err
		}
		obj = append(obj, member{key: kt.(string), val: val})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return obj, nil
}

func parseArray(dec *json.Decoder) (any, error) {
	arr := []any{}
	for dec.More() {
		val, err := parseNext(dec)
		if err != nil {
			return nil, err
		}
		arr = append(arr, val)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return arr, nil
}

func parseNext(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return parseValue(dec, tok)
}

// redactNode returns v with sensitive content masked.
func redactNode(v any, scm bool) any {
	switch t := v.(type) {
	case string:
		return maskEmailsInText(t)
	case []any:
		for i := range t {
			t[i] = redactNode(t[i], scm)
		}
		return t
	case object:
		return redactObject(t, scm)
	}
	return v
}

func redactObject(obj object, scm bool) object {
	diffKey := changelogKey(obj)
	isUser := hasUserIdentity(obj)
	for i, m := range obj {
		if isUser && m.key == "name" {
			if s, ok := m.val.(string); ok {
				obj[i].val = maskPlain(s)
				continue
			}
		}
		obj[i].val = redactMember(m, diffKey, scm)
	}
	return obj
}

// hasUserIdentity reports whether obj is a user record (it carries a string
// "login"), whose "name" is a person's display name.
func hasUserIdentity(obj object) bool {
	for _, m := range obj {
		if _, ok := m.val.(string); ok && normalizeKey(m.key) == "login" {
			return true
		}
	}
	return false
}

func redactMember(m member, diffKey string, scm bool) any {
	if s, ok := m.val.(string); ok {
		return redactString(m.key, s, diffKey)
	}
	if scm && m.key == "scm" {
		maskSCMAuthors(m.val)
	}
	return redactNode(m.val, scm)
}

func redactString(key, s, diffKey string) string {
	if isSensitiveKey(key) {
		return maskByKey(key, s)
	}
	if diffKey != "" && (key == "oldValue" || key == "newValue") {
		return maskByKey(diffKey, s)
	}
	return maskEmailsInText(s)
}

// changelogKey returns the sensitive "key" of a changelog diff entry, or "".
func changelogKey(obj object) string {
	for _, m := range obj {
		if s, ok := m.val.(string); ok && m.key == "key" && isSensitiveKey(s) {
			return s
		}
	}
	return ""
}

func maskSCMAuthors(v any) {
	rows, ok := v.([]any)
	if !ok {
		return
	}
	for _, row := range rows {
		cols, ok := row.([]any)
		if !ok || len(cols) < 2 {
			continue
		}
		if s, ok := cols[1].(string); ok {
			cols[1] = maskPlain(s)
		}
	}
}

type jsonWriter struct {
	buf *bytes.Buffer
	enc *json.Encoder
}

func (w *jsonWriter) write(v any) {
	switch t := v.(type) {
	case object:
		w.writeObject(t)
	case []any:
		w.writeArray(t)
	case string:
		w.writeString(t)
	case json.Number:
		w.buf.WriteString(t.String())
	case bool:
		if t {
			w.buf.WriteString("true")
		} else {
			w.buf.WriteString("false")
		}
	default:
		w.buf.WriteString("null")
	}
}

func (w *jsonWriter) writeString(s string) {
	_ = w.enc.Encode(s)
	w.buf.Truncate(w.buf.Len() - 1)
}

func (w *jsonWriter) writeObject(obj object) {
	w.buf.WriteByte('{')
	for i, m := range obj {
		if i > 0 {
			w.buf.WriteByte(',')
		}
		w.writeString(m.key)
		w.buf.WriteByte(':')
		w.write(m.val)
	}
	w.buf.WriteByte('}')
}

func (w *jsonWriter) writeArray(arr []any) {
	w.buf.WriteByte('[')
	for i, v := range arr {
		if i > 0 {
			w.buf.WriteByte(',')
		}
		w.write(v)
	}
	w.buf.WriteByte(']')
}
