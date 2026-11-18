package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

var errInvalidDataset = errors.New("invalid offline evaluation data")

func decodeDataset(reader io.Reader) (Dataset, error) {
	var data Dataset
	if reader == nil {
		return data, errInvalidDataset
	}
	payload, err := io.ReadAll(io.LimitReader(reader, maxEvaluationInputBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxEvaluationInputBytes || !utf8.Valid(payload) || !validEvaluationUnicodeEscapes(payload) {
		return data, errInvalidDataset
	}
	if err := validateUniqueJSON(payload); err != nil {
		return data, errInvalidDataset
	}
	if err := validateEvaluationShape(payload, reflect.TypeOf(Dataset{}), 0); err != nil {
		return data, errInvalidDataset
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil {
		return Dataset{}, errInvalidDataset
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Dataset{}, errInvalidDataset
	}
	if err := validateDataset(data); err != nil {
		return Dataset{}, errInvalidDataset
	}
	return data, nil
}

// validateEvaluationShape applies exact, case-sensitive JSON field names and
// requires every field without the `omitempty` tag. encoding/json alone accepts
// case-insensitive field matches and silently supplies zero values for missing
// fields, which would make signed review records ambiguous.
func validateEvaluationShape(payload []byte, target reflect.Type, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting limit exceeded")
	}
	if target == reflect.TypeOf(json.RawMessage{}) {
		return nil // The saved analysis has its own stricter application decoder.
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		return errors.New("unexpected null value")
	}
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
			return errors.New("expected object")
		}
		for key := range fields {
			if _, ok := target.FieldByNameFunc(func(name string) bool {
				field, _ := target.FieldByName(name)
				jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
				return jsonName == key
			}); !ok {
				return errors.New("unknown or incorrectly cased field")
			}
		}
		for index := 0; index < target.NumField(); index++ {
			field := target.Field(index)
			parts := strings.Split(field.Tag.Get("json"), ",")
			name := parts[0]
			if name == "" {
				name = field.Name
			}
			if name == "-" {
				continue
			}
			raw, present := fields[name]
			optional := len(parts) > 1 && strings.Contains(parts[1], "omitempty")
			if !present {
				if !optional {
					return errors.New("required field is missing")
				}
				continue
			}
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				if field.Type.Kind() == reflect.Pointer && optional {
					continue
				}
				return errors.New("unexpected null value")
			}
			if err := validateEvaluationShape(raw, field.Type, depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
			return errors.New("null array")
		}
		var values []json.RawMessage
		if err := json.Unmarshal(payload, &values); err != nil || values == nil {
			return errors.New("expected array")
		}
		for _, value := range values {
			if err := validateEvaluationShape(value, target.Elem(), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateUniqueJSON rejects duplicate object keys and excessive nesting
// before encoding/json can silently select a last value. The input is already
// bounded and checked for raw UTF-8 and escaped surrogate validity.
func validateUniqueJSON(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := scanJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing JSON value")
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting limit exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid object key")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate object key")
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid object")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

// encoding/json replaces unpaired UTF-16 surrogate escapes with U+FFFD.
// Reject them before decoding so saved model and label text is never changed.
func validEvaluationUnicodeEscapes(payload []byte) bool {
	inString := false
	for i := 0; i < len(payload); i++ {
		if !inString {
			if payload[i] == '"' {
				inString = true
			}
			continue
		}
		switch payload[i] {
		case '"':
			inString = false
		case '\\':
			i++
			if i >= len(payload) {
				return false
			}
			if payload[i] != 'u' {
				continue
			}
			if i+4 >= len(payload) {
				return false
			}
			value, ok := evaluationHex16(payload[i+1 : i+5])
			if !ok {
				return false
			}
			i += 4
			if value >= 0xD800 && value <= 0xDBFF {
				if i+6 >= len(payload) || payload[i+1] != '\\' || payload[i+2] != 'u' {
					return false
				}
				low, ok := evaluationHex16(payload[i+3 : i+7])
				if !ok || low < 0xDC00 || low > 0xDFFF {
					return false
				}
				i += 6
			} else if value >= 0xDC00 && value <= 0xDFFF {
				return false
			}
		}
	}
	return !inString
}

func evaluationHex16(input []byte) (uint16, bool) {
	if len(input) != 4 {
		return 0, false
	}
	var value uint16
	for _, digit := range input {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value += uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value += uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value += uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
