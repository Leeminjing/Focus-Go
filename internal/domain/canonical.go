package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxDocument = 32 << 20
const MaxInteger = int64(1<<53 - 1)

var (
	ErrInvalid               = errors.New("invalid Focus contract")
	ErrUnsupportedHistory    = errors.New("unsupported Focus history")
	ErrUnsupportedCheckpoint = errors.New("unsupported checkpoint backend")
	ErrHash                  = errors.New("content hash mismatch")
	ErrRefMismatch           = errors.New("Context Revision reference mismatch")
	ErrConflict              = errors.New("Focus catalog revision conflict")
	ErrBusy                  = errors.New("Context has an unsettled Main Run")
	ErrNotFound              = errors.New("Focus record not found")
	ErrCorrupt               = errors.New("Focus storage corrupt")
	ErrIdempotency           = errors.New("idempotency key request mismatch")
	ErrExecutionIdentity     = errors.New("Agent execution identity mismatch")
)

// Canonical implements the approved integer-only Python-compatible JSON
// profile. It rejects duplicate keys, invalid UTF-8/surrogates and lossy numbers.
func Canonical(data []byte) ([]byte, error) {
	if len(data) > MaxDocument || !utf8.Valid(data) || !validEscapes(data) {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	out, err := canonicalValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	return out, nil
}
func canonicalValue(d *json.Decoder, depth int) ([]byte, error) {
	if depth > 64 {
		return nil, ErrInvalid
	}
	token, err := d.Token()
	if err != nil {
		return nil, errors.Join(ErrInvalid, err)
	}
	switch value := token.(type) {
	case nil:
		return []byte("null"), nil
	case bool:
		if value {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case string:
		return quote(value), nil
	case json.Number:
		s := string(value)
		if strings.ContainsAny(s, ".eE") {
			return nil, ErrInvalid
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n > MaxInteger || n < -MaxInteger {
			return nil, ErrInvalid
		}
		return []byte(strconv.FormatInt(n, 10)), nil
	case json.Delim:
		switch value {
		case '[':
			out := []byte{'['}
			first := true
			for d.More() {
				part, err := canonicalValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				if !first {
					out = append(out, ',')
				}
				first = false
				out = append(out, part...)
			}
			if end, err := d.Token(); err != nil || end != json.Delim(']') {
				return nil, ErrInvalid
			}
			return append(out, ']'), nil
		case '{':
			values := map[string][]byte{}
			var keys []string
			for d.More() {
				token, err := d.Token()
				if err != nil {
					return nil, ErrInvalid
				}
				key, ok := token.(string)
				if !ok {
					return nil, ErrInvalid
				}
				if _, exists := values[key]; exists {
					return nil, ErrInvalid
				}
				part, err := canonicalValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				keys = append(keys, key)
				values[key] = part
			}
			if end, err := d.Token(); err != nil || end != json.Delim('}') {
				return nil, ErrInvalid
			}
			sort.Strings(keys)
			out := []byte{'{'}
			for i, key := range keys {
				if i > 0 {
					out = append(out, ',')
				}
				out = append(out, quote(key)...)
				out = append(out, ':')
				out = append(out, values[key]...)
			}
			return append(out, '}'), nil
		}
	}
	return nil, ErrInvalid
}
func quote(s string) []byte {
	out := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"', '\\':
			out = append(out, '\\', byte(r))
		case '\b':
			out = append(out, '\\', 'b')
		case '\f':
			out = append(out, '\\', 'f')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			if r < 32 {
				out = append(out, fmt.Sprintf("\\u%04x", r)...)
			} else {
				out = utf8.AppendRune(out, r)
			}
		}
	}
	return append(out, '"')
}
func validEscapes(data []byte) bool {
	inString := false
	for i := 0; i < len(data); i++ {
		if data[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		n, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
func HashJSON(data []byte) (string, error) {
	canonical, err := Canonical(data)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// Marshal validates Go strings before encoding/json can replace invalid bytes.
// All persisted contracts use this path, including raw JSON payloads.
func Marshal(value any) ([]byte, error) {
	if !validStrings(reflect.ValueOf(value), 0) {
		return nil, ErrInvalid
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, errors.Join(ErrInvalid, err)
	}
	return Canonical(data)
}
func validStrings(v reflect.Value, depth int) bool {
	if depth > 64 {
		return false
	}
	if !v.IsValid() {
		return true
	}
	if v.Type() == reflect.TypeOf(json.RawMessage{}) {
		if v.IsNil() {
			return true
		}
		_, err := Canonical(v.Bytes())
		return err == nil
	}
	switch v.Kind() {
	case reflect.String:
		return utf8.ValidString(v.String())
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			return validStrings(v.Elem(), depth+1)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if !validStrings(v.Index(i), depth+1) {
				return false
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if !validStrings(iter.Key(), depth+1) || !validStrings(iter.Value(), depth+1) {
				return false
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() && !validStrings(v.Field(i), depth+1) {
				return false
			}
		}
	}
	return true
}
func Strict(data []byte, target any) error {
	canonical, err := Canonical(data)
	if err != nil {
		return err
	}
	// encoding/json otherwise accepts null for non-nullable string/integer
	// fields and silently substitutes zero values, changing reference identity.
	var shape any
	shapeDecoder := json.NewDecoder(bytes.NewReader(canonical))
	shapeDecoder.UseNumber()
	if err := shapeDecoder.Decode(&shape); err != nil {
		return errors.Join(ErrInvalid, err)
	}
	typeOf := reflect.TypeOf(target)
	if typeOf == nil || typeOf.Kind() != reflect.Pointer || !nullsMatch(shape, typeOf.Elem(), 0) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(target); err != nil {
		return errors.Join(ErrInvalid, err)
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}

func nullsMatch(value any, t reflect.Type, depth int) bool {
	if depth > 64 {
		return false
	}
	if t == reflect.TypeOf(json.RawMessage{}) || t.Kind() == reflect.Interface {
		return true
	}
	if t.Kind() == reflect.Pointer {
		if value == nil {
			return true
		}
		return nullsMatch(value, t.Elem(), depth+1)
	}
	if value == nil {
		return false
	}
	switch t.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return true
		}
		known := map[string]bool{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			known[name] = true
			if fieldValue, exists := object[name]; exists && !nullsMatch(fieldValue, field.Type, depth+1) {
				return false
			}
		}
		for key := range object {
			if !known[key] {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		if values, ok := value.([]any); ok {
			for _, item := range values {
				if !nullsMatch(item, t.Elem(), depth+1) {
					return false
				}
			}
		}
	case reflect.Map:
		if values, ok := value.(map[string]any); ok {
			for _, item := range values {
				if !nullsMatch(item, t.Elem(), depth+1) {
					return false
				}
			}
		}
	}
	return true
}
func Hash(value any) (string, error) {
	data, err := Marshal(value)
	if err != nil {
		return "", err
	}
	return HashJSON(data)
}
func Copy[T any](value T) (T, error) {
	var copied T
	data, err := Marshal(value)
	if err != nil {
		return copied, err
	}
	err = Strict(data, &copied)
	return copied, err
}
