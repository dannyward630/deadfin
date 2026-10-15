// SPDX-License-Identifier: GPL-2.0-only

package jsonnum

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type embeddedBase struct {
	Count int `json:"Count"`
	Size  int64
}

type ambiguousA struct {
	Count int
}

type ambiguousB struct {
	Count int
	Size  int64
}

type withEmbedded struct {
	embeddedBase
	*Ptr
	Level int `json:"Level"`
}

type Ptr struct {
	Depth float64
}

type ambiguous struct {
	ambiguousA
	ambiguousB
}

type quotedOption struct {
	Count int `json:",string"`
}

// selfDecoding owns its decoding; a quoted number must reach it unchanged.
type selfDecoding int

func (s *selfDecoding) UnmarshalJSON(data []byte) error {
	if string(data) != `"7"` {
		return errors.New(`selfDecoding wants the literal "7"`)
	}
	*s = 7
	return nil
}

type withSelfDecoding struct {
	Value selfDecoding
	Index int
}

func TestUnmarshalFollowsEncodingJSONFieldResolution(t *testing.T) {
	var got withEmbedded
	if err := Unmarshal([]byte(`{"count":"3","SIZE":"4","depth":"0.5","Level":"1"}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Count != 3 || got.Size != 4 || got.Ptr == nil || got.Depth != 0.5 || got.Level != 1 {
		t.Fatalf("got %+v ptr %+v", got, got.Ptr)
	}
}

func TestUnmarshalIgnoresAmbiguousEmbeddedNames(t *testing.T) {
	var got ambiguous
	if err := Unmarshal([]byte(`{"Count":"3","Size":"4"}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.ambiguousA.Count != 0 || got.ambiguousB.Count != 0 || got.Size != 4 {
		t.Fatalf("got %+v", got)
	}
}

func TestUnmarshalKeepsStringOption(t *testing.T) {
	var got quotedOption
	if err := Unmarshal([]byte(`{"Count":"5"}`), &got); err != nil || got.Count != 5 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestUnmarshalLeavesUnmarshalersAlone(t *testing.T) {
	var got withSelfDecoding
	if err := Unmarshal([]byte(`{"Value":"7","Index":"2"}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Value != 7 || got.Index != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestUnmarshalTopLevelAndInterfaces(t *testing.T) {
	var number int
	if err := Unmarshal([]byte(` "12" `), &number); err != nil || number != 12 {
		t.Fatalf("got %d, %v", number, err)
	}
	var anything map[string]any
	if err := Unmarshal([]byte(`{"a":"1"}`), &anything); err != nil || anything["a"] != "1" {
		t.Fatalf("got %#v, %v", anything, err)
	}
}

func TestUnmarshalReturnsOriginalErrors(t *testing.T) {
	var number int
	err := Unmarshal([]byte(`"x"`), &number)
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || typeErr.Value != "string" {
		t.Fatalf("err = %v", err)
	}
	var syntax *json.SyntaxError
	if err := Unmarshal([]byte(`{"a":`), &number); !errors.As(err, &syntax) {
		t.Fatalf("err = %v", err)
	}
	var flag bool
	if err := Unmarshal([]byte(`"true"`), &flag); err == nil {
		t.Fatal("bool from string decoded")
	}
}

func TestIsValidNumber(t *testing.T) {
	for _, s := range []string{"0", "-0", "12", "-12", "0.5", "1e3", "1E+3", "1.5e-3"} {
		if !isValidNumber([]byte(s)) {
			t.Errorf("isValidNumber(%q) = false", s)
		}
	}
	for _, s := range []string{"", "-", "+1", "01", "1.", ".5", "1e", "1e+", " 1", "1 ", "0x1", "NaN", "Infinity", "1_000"} {
		if isValidNumber([]byte(s)) {
			t.Errorf("isValidNumber(%q) = true", s)
		}
	}
}

func TestUnquoteNumbersPreservesLayout(t *testing.T) {
	type body struct {
		A int
		B []float64
	}
	in := []byte(`{ "B" : [ "1" , 2 ], "A":"3", "a":"4" }`)
	out, changed := unquoteNumbers(in, reflect.TypeFor[*body]())
	if !changed || string(out) != `{ "B" : [ 1 , 2 ], "A":3, "a":4 }` {
		t.Fatalf("got %s %v", out, changed)
	}
}
