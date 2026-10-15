// SPDX-License-Identifier: GPL-2.0-only

// Package jsonnum decodes client JSON as upstream Jellyfin does.
//
// Upstream reads every request body and every inbound WebSocket message
// with System.Text.Json and NumberHandling =
// JsonNumberHandling.AllowReadingFromString
// (src/Jellyfin.Extensions/Json/JsonDefaults.cs:34, copied onto the MVC
// options in Jellyfin.Server/Extensions/ApiServiceCollectionExtensions.cs).
// A numeric member then also accepts a JSON string that holds a number, for
// example "AudioStreamIndex":"2". jellyfin-web sends such values.
//
// Unmarshal is encoding/json's Unmarshal plus that rule. Field matching,
// unknown-field tolerance, and every other behavior stay those of
// encoding/json. The extra work runs only when encoding/json rejects a
// string for a numeric target, so a valid body costs one normal decode.
package jsonnum

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Unmarshal decodes data into v as json.Unmarshal does. In addition, a JSON
// string whose whole content is a valid JSON number literal decodes into a
// numeric target (any int, uint, or float kind, also through pointers,
// struct fields, slice and array elements, and map values). Surrounding
// whitespace, a leading '+', and other non-JSON forms stay errors, as do
// strings for bool and other non-numeric targets.
//
// Types that implement json.Unmarshaler or encoding.TextUnmarshaler keep
// their own decoding. A wrapper UnmarshalJSON that decodes an alias type
// calls this function to keep the rule for its members.
func Unmarshal(data []byte, v any) error {
	err := json.Unmarshal(data, v)
	if err == nil || !isQuotedNumberError(err) {
		return err
	}
	rewritten, changed := unquoteNumbers(data, reflect.TypeOf(v))
	if !changed {
		return err
	}
	// A repeated decode over the same document is idempotent for the
	// standard kinds: it sets the same members to the same values.
	return json.Unmarshal(rewritten, v)
}

// isQuotedNumberError reports whether encoding/json rejected a JSON string
// for a numeric target.
func isQuotedNumberError(err error) bool {
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || typeErr.Value != "string" || typeErr.Type == nil {
		return false
	}
	return isNumericKind(typeErr.Type.Kind())
}

func isNumericKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// edit replaces data[start:end], a JSON string literal, with number.
type edit struct {
	start, end int
	number     []byte
}

// rewriter walks a syntactically valid JSON document beside the Go type
// that receives it and records each string literal that lands on a numeric
// target and holds a valid number.
type rewriter struct {
	data   []byte
	edits  []edit
	fields map[reflect.Type][]field
}

// unquoteNumbers returns data with each quoted number that targets a
// numeric member of t replaced by the bare number. encoding/json validates
// the syntax before it reports a type error, so data is valid JSON here.
func unquoteNumbers(data []byte, t reflect.Type) ([]byte, bool) {
	w := rewriter{data: data, fields: map[reflect.Type][]field{}}
	w.value(w.space(0), t)
	if len(w.edits) == 0 {
		return nil, false
	}
	size := len(data)
	for _, e := range w.edits {
		size += len(e.number) - (e.end - e.start)
	}
	out := make([]byte, 0, size)
	last := 0
	for _, e := range w.edits {
		out = append(out, data[last:e.start]...)
		out = append(out, e.number...)
		last = e.end
	}
	out = append(out, data[last:]...)
	return out, true
}

// target resolves t as encoding/json's indirect does: it follows pointers
// and returns nil when a type decodes itself or when no static type
// governs the value.
func target(t reflect.Type) reflect.Type {
	unmarshalerType := reflect.TypeFor[json.Unmarshaler]()
	textUnmarshalerType := reflect.TypeFor[encoding.TextUnmarshaler]()
	for t != nil {
		if t.Implements(unmarshalerType) || t.Implements(textUnmarshalerType) {
			return nil
		}
		if t.Kind() != reflect.Pointer {
			pointer := reflect.PointerTo(t)
			if pointer.Implements(unmarshalerType) || pointer.Implements(textUnmarshalerType) {
				return nil
			}
			if t.Kind() == reflect.Interface {
				return nil
			}
			return t
		}
		t = t.Elem()
	}
	return nil
}

// value walks the JSON value at p into type t and returns the offset after
// the value.
func (w *rewriter) value(p int, t reflect.Type) int {
	t = target(t)
	if t == nil {
		return w.skip(p)
	}
	switch w.data[p] {
	case '{':
		switch t.Kind() {
		case reflect.Struct:
			return w.object(p, func(key string) reflect.Type { return w.field(t, key) })
		case reflect.Map:
			elem := t.Elem()
			return w.object(p, func(string) reflect.Type { return elem })
		}
	case '[':
		switch t.Kind() {
		case reflect.Slice, reflect.Array:
			return w.array(p, t.Elem())
		}
	case '"':
		end := w.stringEnd(p)
		if isNumericKind(t.Kind()) {
			if number, ok := quotedNumber(w.data[p:end]); ok {
				w.edits = append(w.edits, edit{start: p, end: end, number: number})
			}
		}
		return end
	}
	return w.skip(p)
}

func (w *rewriter) object(p int, member func(key string) reflect.Type) int {
	p = w.space(p + 1)
	if w.data[p] == '}' {
		return p + 1
	}
	for {
		end := w.stringEnd(p)
		key := unquote(w.data[p:end])
		p = w.space(end)
		p = w.space(p + 1) // ':'
		p = w.space(w.value(p, member(key)))
		if w.data[p] == '}' {
			return p + 1
		}
		p = w.space(p + 1) // ','
	}
}

func (w *rewriter) array(p int, elem reflect.Type) int {
	p = w.space(p + 1)
	if w.data[p] == ']' {
		return p + 1
	}
	for {
		p = w.space(w.value(p, elem))
		if w.data[p] == ']' {
			return p + 1
		}
		p = w.space(p + 1) // ','
	}
}

func (w *rewriter) space(p int) int {
	for p < len(w.data) {
		switch w.data[p] {
		case ' ', '\t', '\r', '\n':
			p++
		default:
			return p
		}
	}
	return p
}

// stringEnd returns the offset after the string literal that starts at p.
func (w *rewriter) stringEnd(p int) int {
	for p++; p < len(w.data); p++ {
		switch w.data[p] {
		case '\\':
			p++
		case '"':
			return p + 1
		}
	}
	return p
}

// skip returns the offset after the JSON value that starts at p.
func (w *rewriter) skip(p int) int {
	switch w.data[p] {
	case '"':
		return w.stringEnd(p)
	case '{', '[':
		depth := 0
		for p < len(w.data) {
			switch w.data[p] {
			case '"':
				p = w.stringEnd(p)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return p + 1
				}
			}
			p++
		}
		return p
	}
	for p < len(w.data) {
		switch w.data[p] {
		case ',', ']', '}', ' ', '\t', '\r', '\n':
			return p
		}
		p++
	}
	return p
}

// unquote returns the content of a JSON string literal.
func unquote(literal []byte) string {
	content := literal[1 : len(literal)-1]
	if bytes.IndexByte(content, '\\') < 0 && utf8.Valid(content) {
		return string(content)
	}
	var s string
	_ = json.Unmarshal(literal, &s)
	return s
}

// quotedNumber returns the number a string literal holds when its whole
// content is a valid JSON number literal.
func quotedNumber(literal []byte) ([]byte, bool) {
	content := literal[1 : len(literal)-1]
	if bytes.IndexByte(content, '\\') >= 0 {
		content = []byte(unquote(literal))
	}
	if !isValidNumber(content) {
		return nil, false
	}
	return content, true
}

// isValidNumber reports whether s is a JSON number literal (RFC 8259 §6).
func isValidNumber(s []byte) bool {
	i := 0
	if i < len(s) && s[i] == '-' {
		i++
	}
	switch {
	case i < len(s) && s[i] == '0':
		i++
	case i < len(s) && s[i] >= '1' && s[i] <= '9':
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	default:
		return false
	}
	if i < len(s) && s[i] == '.' {
		i++
		if i >= len(s) || !isDigit(s[i]) {
			return false
		}
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if i >= len(s) || !isDigit(s[i]) {
			return false
		}
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	}
	return i == len(s)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// field is one JSON-visible struct member, resolved as encoding/json's
// typeFields resolves it.
type field struct {
	name   string
	tagged bool
	index  []int
	typ    reflect.Type
	quoted bool
}

// field returns the type of the member of struct t that receives key, or
// nil. encoding/json prefers an exact name match and then the first
// case-insensitive match. A member with the ",string" option already reads
// its value from a string, so the walk leaves it alone.
func (w *rewriter) field(t reflect.Type, key string) reflect.Type {
	fields, ok := w.fields[t]
	if !ok {
		fields = typeFields(t)
		w.fields[t] = fields
	}
	match := -1
	for i := range fields {
		if fields[i].name == key {
			match = i
			break
		}
	}
	if match < 0 {
		for i := range fields {
			if strings.EqualFold(fields[i].name, key) {
				match = i
				break
			}
		}
	}
	if match < 0 || fields[match].quoted {
		return nil
	}
	return fields[match].typ
}

// typeFields mirrors encoding/json's typeFields (Go 1.25): exported members
// and promoted members of embedded structs, named by the json tag or the Go
// name, with the shallowest (then tagged) member winning a name and
// ambiguous names dropped.
func typeFields(t reflect.Type) []field {
	var current []field
	next := []field{{typ: t}}
	var count map[reflect.Type]int
	nextCount := map[reflect.Type]int{}
	visited := map[reflect.Type]bool{}
	var fields []field

	for len(next) > 0 {
		current, next = next, current[:0]
		count, nextCount = nextCount, map[reflect.Type]int{}
		for _, f := range current {
			if visited[f.typ] {
				continue
			}
			visited[f.typ] = true
			for i := 0; i < f.typ.NumField(); i++ {
				sf := f.typ.Field(i)
				if sf.Anonymous {
					embedded := sf.Type
					if embedded.Kind() == reflect.Pointer {
						embedded = embedded.Elem()
					}
					if !sf.IsExported() && embedded.Kind() != reflect.Struct {
						continue
					}
				} else if !sf.IsExported() {
					continue
				}
				tag := sf.Tag.Get("json")
				if tag == "-" {
					continue
				}
				name, options, _ := strings.Cut(tag, ",")
				if !isValidTag(name) {
					name = ""
				}
				index := append(slices.Clone(f.index), i)
				ft := sf.Type
				if ft.Name() == "" && ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				quoted := false
				if hasOption(options, "string") {
					switch ft.Kind() {
					case reflect.Bool, reflect.String:
						quoted = true
					default:
						quoted = isNumericKind(ft.Kind())
					}
				}
				if name != "" || !sf.Anonymous || ft.Kind() != reflect.Struct {
					tagged := name != ""
					if name == "" {
						name = sf.Name
					}
					fields = append(fields, field{name: name, tagged: tagged, index: index, typ: sf.Type, quoted: quoted})
					if count[f.typ] > 1 {
						// Two copies at one depth annihilate below.
						fields = append(fields, fields[len(fields)-1])
					}
					continue
				}
				nextCount[ft]++
				if nextCount[ft] == 1 {
					next = append(next, field{name: ft.Name(), index: index, typ: ft})
				}
			}
		}
	}

	slices.SortFunc(fields, func(a, b field) int {
		if c := strings.Compare(a.name, b.name); c != 0 {
			return c
		}
		if c := len(a.index) - len(b.index); c != 0 {
			return c
		}
		if a.tagged != b.tagged {
			if a.tagged {
				return -1
			}
			return 1
		}
		return slices.Compare(a.index, b.index)
	})
	out := fields[:0]
	for advance, i := 0, 0; i < len(fields); i += advance {
		first := fields[i]
		for advance = 1; i+advance < len(fields); advance++ {
			if fields[i+advance].name != first.name {
				break
			}
		}
		if advance == 1 {
			out = append(out, first)
			continue
		}
		group := fields[i : i+advance]
		if len(group[0].index) == len(group[1].index) && group[0].tagged == group[1].tagged {
			continue
		}
		out = append(out, group[0])
	}
	slices.SortFunc(out, func(a, b field) int { return slices.Compare(a.index, b.index) })
	return out
}

func hasOption(options, name string) bool {
	for options != "" {
		var option string
		option, options, _ = strings.Cut(options, ",")
		if option == name {
			return true
		}
	}
	return false
}

func isValidTag(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", c):
		case !unicode.IsLetter(c) && !unicode.IsDigit(c):
			return false
		}
	}
	return true
}
