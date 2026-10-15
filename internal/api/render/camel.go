// SPDX-License-Identifier: GPL-2.0-only

// Package render owns response encoding: PascalCase JSON by default (struct
// tags carry the wire names), camelCase when the client negotiates
// `Accept: application/json; profile="CamelCase"` — 362 spec operations
// declare that profile.
//
// The camel encoder re-cases STRUCT property names only. Map keys
// (ProviderIds, ImageTags, …) are never re-cased: upstream configures
// JsonNamingPolicy.CamelCase as PropertyNamingPolicy with no
// DictionaryKeyPolicy (src/Jellyfin.Extensions/Json/JsonDefaults.cs).
package render

import (
	"bytes"
	"encoding"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// MarshalCamel encodes v as JSON with camelCase struct property names.
func MarshalCamel(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := encodeCamel(&buf, reflect.ValueOf(v)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// camelCase reproduces System.Text.Json's JsonNamingPolicy.CamelCase
// (FixCasing): lowercase the leading run of uppercase letters, but stop
// before an uppercase letter that is followed by a lowercase one (so
// "URLValue" => "urlValue", "ID" => "id", "ProviderIds" => "providerIds").
func camelCase(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	for i := 0; i < len(runes) && (i == 0 || unicode.IsUpper(runes[i])); i++ {
		hasNext := i+1 < len(runes)
		if i > 0 && hasNext && !unicode.IsUpper(runes[i+1]) {
			break
		}
		runes[i] = unicode.ToLower(runes[i])
	}
	return string(runes)
}

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

type fieldInfo struct {
	index     []int
	jsonName  string
	camelName string
	omitEmpty bool
	omitZero  bool
	quoted    bool
}

var structCache sync.Map // reflect.Type -> []fieldInfo

// structFields resolves the encodable fields of a struct type, honoring json
// tags and flattening anonymous embeds the way encoding/json does for the
// common case (no name conflicts — our DTOs have none; conflicts are a bug).
func structFields(t reflect.Type) []fieldInfo {
	if cached, ok := structCache.Load(t); ok {
		return cached.([]fieldInfo)
	}
	var fields []fieldInfo
	var walk func(t reflect.Type, prefix []int)
	walk = func(t reflect.Type, prefix []int) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, opts, _ := strings.Cut(tag, ",")
			if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct &&
				!f.Type.Implements(jsonMarshalerType) && !reflect.PointerTo(f.Type).Implements(jsonMarshalerType) {
				walk(f.Type, append(append([]int{}, prefix...), i))
				continue
			}
			if name == "" {
				name = f.Name
			}
			optSet := map[string]bool{}
			for _, o := range strings.Split(opts, ",") {
				optSet[o] = true
			}
			fields = append(fields, fieldInfo{
				index:     append(append([]int{}, prefix...), i),
				jsonName:  name,
				camelName: camelCase(name),
				omitEmpty: optSet["omitempty"],
				omitZero:  optSet["omitzero"],
				quoted:    optSet["string"],
			})
		}
	}
	walk(t, nil)
	structCache.Store(t, fields)
	return fields
}

func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Interface, reflect.Pointer:
		return v.IsZero()
	default:
		return false
	}
}

func isZeroValue(v reflect.Value) bool {
	if (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil() {
		return true
	}
	// omitzero semantics (Go 1.24 encoding/json): use IsZero() method when
	// present, else reflect zero-ness.
	if m := v.MethodByName("IsZero"); m.IsValid() && m.Type().NumIn() == 0 && m.Type().NumOut() == 1 && m.Type().Out(0).Kind() == reflect.Bool {
		return m.Call(nil)[0].Bool()
	}
	return v.IsZero()
}

// encodeCustomCamel preserves custom MarshalJSON's null and empty-collection
// semantics while applying the negotiated naming policy to its struct fields.
// Raw maps stay intact, matching System.Text.Json's lack of DictionaryKeyPolicy.
func encodeCustomCamel(buf *bytes.Buffer, raw []byte, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		buf.Write(raw)
		return nil
	}
	return encodeRawCamel(buf, raw, typ)
}

func encodeRawCamel(buf *bytes.Buffer, raw []byte, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			buf.Write(raw)
			return nil
		}
		fields := structFields(typ)
		byName := make(map[string]fieldInfo, len(fields))
		for _, field := range fields {
			byName[field.jsonName] = field
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.SliceStable(keys, func(i, j int) bool {
			left, leftKnown := byName[keys[i]]
			right, rightKnown := byName[keys[j]]
			if leftKnown != rightKnown {
				return leftKnown
			}
			if leftKnown {
				return left.index[0] < right.index[0]
			}
			return keys[i] < keys[j]
		})
		buf.WriteByte('{')
		for index, key := range keys {
			if index != 0 {
				buf.WriteByte(',')
			}
			field, known := byName[key]
			output := camelCase(key)
			fieldType := reflect.Type(nil)
			if known {
				output = field.camelName
				fieldType = typ.FieldByIndex(field.index).Type
			}
			name, _ := json.Marshal(output)
			buf.Write(name)
			buf.WriteByte(':')
			if fieldType == nil {
				buf.Write(object[key])
				continue
			}
			if err := encodeRawCamel(buf, object[key], fieldType); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			buf.Write(raw)
			return err
		}
		buf.WriteByte('[')
		for index, value := range values {
			if index != 0 {
				buf.WriteByte(',')
			}
			if err := encodeRawCamel(buf, value, typ.Elem()); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	default:
		buf.Write(raw)
		return nil
	}
}

func encodeCamel(buf *bytes.Buffer, v reflect.Value) error {
	if !v.IsValid() {
		buf.WriteString("null")
		return nil
	}

	// Custom marshalers win (dto.UUID, dto.Time, enums, json.RawMessage).
	if v.Type().Implements(jsonMarshalerType) {
		if v.Kind() == reflect.Pointer && v.IsNil() {
			buf.WriteString("null")
			return nil
		}
		raw, err := v.Interface().(json.Marshaler).MarshalJSON()
		if err != nil {
			return err
		}
		return encodeCustomCamel(buf, raw, v.Type())
	}
	if v.Kind() != reflect.Pointer && v.CanAddr() && v.Addr().Type().Implements(jsonMarshalerType) {
		raw, err := v.Addr().Interface().(json.Marshaler).MarshalJSON()
		if err != nil {
			return err
		}
		return encodeCustomCamel(buf, raw, v.Type())
	}

	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			buf.WriteString("null")
			return nil
		}
		return encodeCamel(buf, v.Elem())

	case reflect.Struct:
		buf.WriteByte('{')
		first := true
		for _, f := range structFields(v.Type()) {
			fv := v.FieldByIndex(f.index)
			if f.omitEmpty && isEmptyValue(fv) {
				continue
			}
			if f.omitZero && isZeroValue(fv) {
				continue
			}
			if !first {
				buf.WriteByte(',')
			}
			first = false
			keyBytes, _ := json.Marshal(f.camelName)
			buf.Write(keyBytes)
			buf.WriteByte(':')
			if err := encodeCamel(buf, fv); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil

	case reflect.Map:
		if v.IsNil() {
			buf.WriteString("null")
			return nil
		}
		// Keys pass through UNCHANGED — the whole point of this encoder.
		type kv struct {
			key string
			val reflect.Value
		}
		entries := make([]kv, 0, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			k := iter.Key()
			var keyStr string
			switch {
			case k.Kind() == reflect.String:
				keyStr = k.String()
			case k.Type().Implements(textMarshalerType):
				b, err := k.Interface().(encoding.TextMarshaler).MarshalText()
				if err != nil {
					return err
				}
				keyStr = string(b)
			default:
				keyStr = fmt.Sprint(k.Interface())
			}
			entries = append(entries, kv{keyStr, iter.Value()})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
		buf.WriteByte('{')
		for i, e := range entries {
			if i > 0 {
				buf.WriteByte(',')
			}
			keyBytes, _ := json.Marshal(e.key)
			buf.Write(keyBytes)
			buf.WriteByte(':')
			if err := encodeCamel(buf, e.val); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil

	case reflect.Slice:
		if v.IsNil() {
			buf.WriteString("null")
			return nil
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			// []byte encodes base64 like encoding/json.
			buf.WriteByte('"')
			buf.WriteString(base64.StdEncoding.EncodeToString(v.Bytes()))
			buf.WriteByte('"')
			return nil
		}
		fallthrough
	case reflect.Array:
		buf.WriteByte('[')
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeCamel(buf, v.Index(i)); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil

	default:
		// Scalars: delegate for exact escaping/float formatting.
		raw, err := json.Marshal(v.Interface())
		if err != nil {
			return err
		}
		buf.Write(raw)
		return nil
	}
}

// lowerFirstValid guards utf8 handling in camelCase (defensive; wire names are ASCII).
var _ = utf8.RuneLen
