// SPDX-License-Identifier: GPL-2.0-only

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/deadfin-org/deadfin/internal/jsonnum"
)

// ErrInvalidConfiguration reports a posted configuration body that does not
// decode to the configuration type. Upstream throws ArgumentException
// ("Body doesn't contain a valid configuration"), which ExceptionMiddleware
// maps to 400 (ConfigurationController.cs:98-101, ExceptionMiddleware.cs:127).
var ErrInvalidConfiguration = errors.New("config: body doesn't contain a valid configuration")

// decodeJSONOverDefaults decodes a JSON object into target, which must point
// to a struct that already holds the upstream constructor defaults.
//
// System.Text.Json constructs the configuration type, which runs its field
// initializers, and then assigns only the members present in the document.
// A member that the client omits keeps its upstream default. encoding/json
// alone is not equivalent: it merges a present array into the existing
// elements and writes through existing pointers. This decoder replaces every
// present member with a fresh value, and it recurses into a present nested
// object so that the nested defaults also survive (TrickplayOptions).
//
// Upstream reads with JsonNumberHandling.AllowReadingFromString
// (JsonDefaults.cs:34), so a quoted number is accepted for a numeric member
// (jsonnum).
func decodeJSONOverDefaults(data []byte, target any) error {
	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Pointer || value.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("config: decode target %T is not a struct pointer", target)
	}
	if isJSONNull(data) {
		return ErrInvalidConfiguration
	}
	if err := decodeStructOverDefaults(data, value.Elem()); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfiguration, err)
	}
	return nil
}

func decodeStructOverDefaults(data []byte, target reflect.Value) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	if members == nil {
		return errors.New("configuration is not a JSON object")
	}
	structType := target.Type()
	for index := range structType.NumField() {
		field := structType.Field(index)
		if !field.IsExported() {
			continue
		}
		name, skip := jsonMemberName(field)
		if skip {
			continue
		}
		raw, ok := lookupJSONMember(members, name)
		if !ok {
			continue
		}
		if err := decodeMember(raw, target.Field(index)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func decodeMember(raw json.RawMessage, member reflect.Value) error {
	if member.Kind() == reflect.Struct && !implementsJSONUnmarshaler(member) && isJSONObject(raw) {
		return decodeStructOverDefaults(raw, member)
	}
	fresh := reflect.New(member.Type())
	if err := jsonnum.Unmarshal(raw, fresh.Interface()); err != nil {
		return err
	}
	member.Set(fresh.Elem())
	return nil
}

// jsonMemberName returns the wire name encoding/json uses for field.
func jsonMemberName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", true
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		name = field.Name
	}
	return name, false
}

// lookupJSONMember prefers an exact name and falls back to a case-insensitive
// match, as encoding/json and PropertyNameCaseInsensitive do.
func lookupJSONMember(members map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	if raw, ok := members[name]; ok {
		return raw, true
	}
	for key, raw := range members {
		if strings.EqualFold(key, name) {
			return raw, true
		}
	}
	return nil, false
}

func implementsJSONUnmarshaler(member reflect.Value) bool {
	_, ok := member.Addr().Interface().(json.Unmarshaler)
	return ok
}

func isJSONNull(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func isJSONObject(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && trimmed[0] == '{'
}
