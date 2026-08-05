// Package jcs implements the RFC 8785 subset frozen by the Sluice server
// reference in contract-input/reference/jcs-server-reference.go.txt.
//
// The subset accepts integers and strings, rejects duplicate object keys,
// floating-point/exponent notation, invalid UTF-8, and unpaired UTF-16
// surrogates. Empty input is treated as {} as required by the server
// reference. This implementation intentionally follows the reference's
// encoding/json + UseNumber behavior rather than a different JCS library.
package jcs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CanonicalizeJSON canonicalizes raw JSON according to the frozen server
// subset. It returns canonical UTF-8 JSON without surrounding whitespace.
func CanonicalizeJSON(raw []byte) (string, error) {
	if err := validateRawJSONUnicode(raw); err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder)
	if err != nil {
		return "", err
	}
	if decoder.More() {
		return "", fmt.Errorf("json trailing data")
	}
	var builder strings.Builder
	if err := writeJCS(&builder, value); err != nil {
		return "", err
	}
	return builder.String(), nil
}

// validateRawJSONUnicode must run before encoding/json. encoding/json
// otherwise replaces invalid UTF-8 and unpaired surrogate escapes with U+FFFD,
// which would collapse distinct request bytes into one fingerprint.
func validateRawJSONUnicode(raw []byte) error {
	if !utf8.Valid(raw) {
		return fmt.Errorf("json contains invalid UTF-8")
	}
	inString := false
	for index := 0; index < len(raw); index++ {
		switch raw[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString {
				continue
			}
			if index+1 >= len(raw) {
				return fmt.Errorf("json string escape truncated")
			}
			if raw[index+1] != 'u' {
				index++
				continue
			}
			unit, ok := parseJSONHexCodeUnit(raw, index+2)
			if !ok {
				return fmt.Errorf("json unicode escape invalid")
			}
			index += 5
			switch {
			case unit >= 0xd800 && unit <= 0xdbff:
				pairStart := index + 1
				if pairStart+6 > len(raw) ||
					raw[pairStart] != '\\' ||
					raw[pairStart+1] != 'u' {
					return fmt.Errorf("json high surrogate is unpaired")
				}
				low, ok := parseJSONHexCodeUnit(raw, pairStart+2)
				if !ok || low < 0xdc00 || low > 0xdfff {
					return fmt.Errorf("json high surrogate is unpaired")
				}
				index = pairStart + 5
			case unit >= 0xdc00 && unit <= 0xdfff:
				return fmt.Errorf("json low surrogate is unpaired")
			}
		}
	}
	return nil
}

func parseJSONHexCodeUnit(raw []byte, start int) (uint16, bool) {
	if start < 0 || start+4 > len(raw) {
		return 0, false
	}
	var value uint16
	for _, digit := range raw[start : start+4] {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value |= uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value |= uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value |= uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func decodeJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			object := make(map[string]any)
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("json object key must be string")
				}
				if _, exists := seen[key]; exists {
					return nil, fmt.Errorf("json duplicate key %q", key)
				}
				seen[key] = struct{}{}
				child, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				object[key] = child
			}
			end, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			if end != json.Delim('}') {
				return nil, fmt.Errorf("json object not closed")
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				child, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, child)
			}
			end, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			if end != json.Delim(']') {
				return nil, fmt.Errorf("json array not closed")
			}
			return array, nil
		default:
			return nil, fmt.Errorf("unexpected json delim %q", typed)
		}
	case bool, string, nil:
		return typed, nil
	case json.Number:
		text := typed.String()
		if strings.ContainsAny(text, ".eE") {
			return nil, fmt.Errorf("json number must be integer decimal string form")
		}
		if _, err := strconv.ParseInt(text, 10, 64); err != nil {
			return nil, fmt.Errorf("json integer out of int64 range")
		}
		return typed, nil
	default:
		return nil, fmt.Errorf("unsupported json token %T", token)
	}
}

func writeJCS(builder *strings.Builder, value any) error {
	switch typed := value.(type) {
	case nil:
		builder.WriteString("null")
	case bool:
		if typed {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case string:
		writeJCSString(builder, typed)
	case json.Number:
		builder.WriteString(typed.String())
	case []any:
		builder.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				builder.WriteByte(',')
			}
			if err := writeJCS(builder, item); err != nil {
				return err
			}
		}
		builder.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			if err := validateJCSKey(key); err != nil {
				return err
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		builder.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				builder.WriteByte(',')
			}
			writeJCSString(builder, key)
			builder.WriteByte(':')
			if err := writeJCS(builder, typed[key]); err != nil {
				return err
			}
		}
		builder.WriteByte('}')
	default:
		return fmt.Errorf("unsupported jcs value %T", value)
	}
	return nil
}

func writeJCSString(builder *strings.Builder, value string) {
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if r < 0x20 {
				builder.WriteString(fmt.Sprintf(`\u%04x`, r))
				continue
			}
			builder.WriteRune(r)
		}
	}
	builder.WriteByte('"')
}

func validateJCSKey(key string) error {
	for _, r := range key {
		if unicode.IsControl(r) {
			return fmt.Errorf("json key contains control character")
		}
	}
	return nil
}
