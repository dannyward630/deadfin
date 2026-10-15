// SPDX-License-Identifier: GPL-2.0-only

// Package bind owns request decoding. Handlers never parse queries or bodies
// by hand (CONVENTIONS §3.1) — every quirk lives here once:
//
//   - query keys are case-insensitive (ASP.NET model binding);
//   - list parameters accept BOTH repeated params (?a=1&a=2 — the Swift SDK)
//     and CSV (?a=1,2 — jellyfin-web's legacy getUrl), mixed freely, with
//     empty entries dropped;
//   - enum values bind case-insensitively (delegated to each enum type's
//     TextUnmarshaler);
//   - booleans accept "true"/"false" in any casing (bool.TryParse semantics);
//   - empty string values bind as absent, not as zero values.
package bind

import (
	"encoding"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
)

// QueryRequest binds the request query into dst using Query's model-binding
// semantics.
func QueryRequest(r *http.Request, dst any) error {
	return Query(r.URL.Query(), dst)
}

// QueryFirst returns the first value for key using url.Values.Get semantics.
// It intentionally preserves key case sensitivity for request paths that use
// legacy exact-key lookup rather than model binding.
func QueryFirst(r *http.Request, key string) string {
	return r.URL.Query().Get(key)
}

// QueryScalar returns the first non-empty value for key, matching query keys
// case-insensitively. Different non-empty values supplied through differently
// cased aliases are rejected so a scalar selection cannot depend on map order.
func QueryScalar(r *http.Request, key string) (string, error) {
	value, _, err := scalarQueryValue(r.URL.Query(), key)
	return value, err
}

// LowercaseQueryOptions returns the source-compatible stream-option subset of
// a request query. StreamingHelpers.ParseStreamOptions forwards keys whose
// first character is lowercase, retaining their original spelling and joining
// repeated values as StringValues.ToString does.
type LowercaseQueryOption struct {
	Name  string
	Value string
}

func LowercaseQueryOptions(r *http.Request) []LowercaseQueryOption {
	type optionGroup struct {
		name   string
		values []string
	}

	groups := make([]optionGroup, 0)
	groupIndex := make(map[string]int)
	for query := r.URL.RawQuery; query != ""; {
		part := query
		if ampersand := strings.IndexByte(query, '&'); ampersand >= 0 {
			part, query = query[:ampersand], query[ampersand+1:]
		} else {
			query = ""
		}
		if part == "" {
			continue
		}

		rawName, rawValue, _ := strings.Cut(part, "=")
		name, err := url.QueryUnescape(rawName)
		if err != nil {
			return nil
		}
		value, err := url.QueryUnescape(rawValue)
		if err != nil {
			return nil
		}

		foldedName := strings.ToLower(name)
		if index, ok := groupIndex[foldedName]; ok {
			if name != "" && name[0] >= 'a' && name[0] <= 'z' {
				groups[index].values = append(groups[index].values, value)
			}
			continue
		}
		groupIndex[foldedName] = len(groups)
		group := optionGroup{name: name}
		if name != "" && name[0] >= 'a' && name[0] <= 'z' {
			group.values = append(group.values, value)
		}
		groups = append(groups, group)
	}

	options := make([]LowercaseQueryOption, 0, len(groups))
	for _, group := range groups {
		if group.name == "" || group.name[0] < 'a' || group.name[0] > 'z' {
			continue
		}
		options = append(options, LowercaseQueryOption{Name: group.name, Value: strings.Join(group.values, ",")})
	}
	return options
}

// Query binds src into dst, a pointer to a struct whose exported fields carry
// `query:"name"` tags (untagged fields use the Go field name). Unknown query
// keys are ignored (upstream ignores them).
func Query(src url.Values, dst any) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("bind.Query: dst must be a pointer to struct, got %T", dst)
	}
	folded := foldQueryValues(src)

	sv := rv.Elem()
	st := sv.Type()
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Tag.Get("query")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		values, ok := folded[strings.ToLower(name)]
		if !ok {
			continue
		}
		if !isQuerySlice(sv.Field(i)) && values.conflictingScalars {
			return fmt.Errorf("parameter %q: conflicting case-insensitive scalar values", name)
		}
		if err := setField(sv.Field(i), values.raw); err != nil {
			return fmt.Errorf("parameter %q: %w", name, err)
		}
	}
	return nil
}

type foldedQueryValues struct {
	raw                []string
	scalar             string
	hasScalar          bool
	conflictingScalars bool
}

func foldQueryValues(src url.Values) map[string]foldedQueryValues {
	folded := make(map[string]foldedQueryValues, len(src))
	for key, raw := range src {
		name := strings.ToLower(key)
		values := folded[name]
		values.raw = append(values.raw, raw...)
		if candidate, present := firstNonEmpty(raw); present {
			if values.hasScalar && candidate != values.scalar {
				values.conflictingScalars = true
			} else if !values.hasScalar {
				values.scalar, values.hasScalar = candidate, true
			}
		}
		folded[name] = values
	}
	return folded
}

func scalarQueryValue(src url.Values, name string) (string, bool, error) {
	values, found := foldQueryValues(src)[strings.ToLower(name)]
	if !found || !values.hasScalar {
		return "", false, nil
	}
	if values.conflictingScalars {
		return "", false, fmt.Errorf("conflicting case-insensitive scalar values")
	}
	return values.scalar, true, nil
}

func firstNonEmpty(raw []string) (string, bool) {
	for _, value := range raw {
		if value != "" {
			return value, true
		}
	}
	return "", false
}

func isQuerySlice(fv reflect.Value) bool {
	return fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() != reflect.Uint8
}

func setField(fv reflect.Value, raw []string) error {
	if isQuerySlice(fv) {
		return setSlice(fv, raw)
	}
	// Scalar: first non-empty occurrence wins. Types that implement
	// emptyQueryValue opt into receiving an explicitly supplied empty value.
	val, found := firstNonEmpty(raw)
	if !found {
		if !acceptsEmptyQueryValue(fv) {
			return nil
		}
		val = ""
	}
	return setScalar(fv, val)
}

type emptyQueryValue interface {
	BindQueryEmpty()
}

func acceptsEmptyQueryValue(fv reflect.Value) bool {
	t := fv.Type()
	if t.Kind() != reflect.Pointer {
		t = reflect.PointerTo(t)
	}
	return t.Implements(reflect.TypeFor[emptyQueryValue]())
}

func setSlice(fv reflect.Value, raw []string) error {
	elemType := fv.Type().Elem()
	out := reflect.MakeSlice(fv.Type(), 0, len(raw))
	for _, r := range raw {
		// Each occurrence may itself be a CSV list.
		for part := range strings.SplitSeq(r, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue // empty entries dropped (both client styles produce them)
			}
			ev := reflect.New(elemType).Elem()
			if err := setScalar(ev, part); err != nil {
				return err
			}
			out = reflect.Append(out, ev)
		}
	}
	fv.Set(out)
	return nil
}

func setScalar(fv reflect.Value, s string) error {
	// Pointers allocate and recurse.
	if fv.Kind() == reflect.Pointer {
		p := reflect.New(fv.Type().Elem())
		if err := setScalar(p.Elem(), s); err != nil {
			return err
		}
		fv.Set(p)
		return nil
	}

	// TextUnmarshaler types (dto.UUID, dto.Time, enums) own their parsing —
	// enums implement case-insensitive parses there.
	if fv.CanAddr() {
		if tu, ok := fv.Addr().Interface().(encoding.TextUnmarshaler); ok {
			return tu.UnmarshalText([]byte(s))
		}
	}

	switch fv.Kind() {
	case reflect.String:
		fv.SetString(s)
	case reflect.Bool:
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			fv.SetBool(true)
		case "false":
			fv.SetBool(false)
		default:
			return fmt.Errorf("invalid boolean %q", s)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid integer %q", s)
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(strings.TrimSpace(s), 10, fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid integer %q", s)
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(strings.TrimSpace(s), fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid number %q", s)
		}
		fv.SetFloat(n)
	default:
		return fmt.Errorf("bind.Query: unsupported field kind %s", fv.Kind())
	}
	return nil
}
