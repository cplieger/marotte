package command

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

//go:fix inline
func floatp(f float64) *float64 { return new(f) }

// decodeContent decodes an answer the way the command door does, so numbers are float64,
// arrays []any and null a nil value.
func decodeContent(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return m
}

func TestElicitationContent(t *testing.T) {
	props := map[string]marotte.ElicitationPropertySchema{
		"name":     {Type: "string", MinLength: new(2), MaxLength: new(3)},
		"one":      {Type: "string", MaxLength: new(1)},
		"color":    {Type: "string", Enum: []string{"red", "green"}},
		"legacy":   {Type: "string", Enum: []string{"r", "g"}, EnumNames: []string{"Red", "Green"}},
		"size":     {Type: "string", OneOf: []marotte.ElicitationEnumOption{{Const: "s", Title: "Small"}, {Const: "l", Title: "Large"}}},
		"score":    {Type: "number", Minimum: floatp(1), Maximum: floatp(5)},
		"count":    {Type: "integer"},
		"ok":       {Type: "boolean"},
		"tags":     {Type: "array", Items: &marotte.ElicitationItemsSchema{Type: "string", Enum: []string{"a", "b", "c"}}, MinItems: new(1), MaxItems: new(2)},
		"titled":   {Type: "array", Items: &marotte.ElicitationItemsSchema{AnyOf: []marotte.ElicitationEnumOption{{Const: "x", Title: "X"}}}},
		"required": {Type: "string"},
	}
	ask := marotte.ElicitationNeededPayload{
		Mode:            "form",
		RequestedSchema: &marotte.ElicitationRequestSchema{Properties: props, Required: []string{"required", "ghost"}},
	}
	cases := []struct {
		name    string
		content string
		ok      bool
	}{
		{"a full valid form", `{"required":"r","name":"ab","color":"red","legacy":"g","size":"l","score":3,"count":2,"ok":false,"tags":["a","b"],"titled":["x"]}`, true},
		{"a required name with no property is not asked for", `{"required":"r"}`, true},
		{"a key that names no property", `{"required":"r","extra":"x"}`, false},
		{"a required property absent", `{}`, false},
		{"a null value", `{"required":null}`, false},
		{"a string given a number", `{"required":1}`, false},
		{"minLength counts code points: one astral rune is too short", `{"required":"r","name":"😀"}`, false},
		{"maxLength counts code points: one astral rune fits", `{"required":"r","one":"😀"}`, true},
		{"over maxLength", `{"required":"r","name":"abcd"}`, false},
		{"an enum non-member", `{"required":"r","color":"blue"}`, false},
		{"enumNames: the value is answered", `{"required":"r","legacy":"r"}`, true},
		{"enumNames: the label is not a value", `{"required":"r","legacy":"Red"}`, false},
		{"a oneOf non-member", `{"required":"r","size":"Small"}`, false},
		{"exactly the minimum", `{"required":"r","score":1}`, true},
		{"exactly the maximum", `{"required":"r","score":5}`, true},
		{"below the minimum", `{"required":"r","score":0.99}`, false},
		{"above the maximum", `{"required":"r","score":5.01}`, false},
		{"a number given a string", `{"required":"r","score":"3"}`, false},
		{"an integer written 2.0", `{"required":"r","count":2.0}`, true},
		{"an integer given 1.5", `{"required":"r","count":1.5}`, false},
		{"a boolean given a string", `{"required":"r","ok":"true"}`, false},
		{"a multi-select non-member", `{"required":"r","tags":["d"]}`, false},
		{"a multi-select below minItems", `{"required":"r","tags":[]}`, false},
		{"a multi-select above maxItems", `{"required":"r","tags":["a","b","c"]}`, false},
		{"a multi-select at minItems", `{"required":"r","tags":["c"]}`, true},
		{"a titled multi-select non-member", `{"required":"r","titled":["X"]}`, false},
		{"a multi-select given a string", `{"required":"r","tags":"a"}`, false},
		{"a multi-select item that is not a string", `{"required":"r","titled":[1]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := decodeContent(t, tc.content)

			got, err := elicitationContent(&ask, content)

			if (err == nil) != tc.ok {
				t.Fatalf("elicitationContent(%s) error = %v, want ok %v", tc.content, err, tc.ok)
			}
			if tc.ok && !reflect.DeepEqual(got, content) {
				t.Errorf("forwarded %v, want the answer unchanged %v", got, content)
			}
		})
	}
}

func TestElicitationContent_URLModeForwardsNothing(t *testing.T) {
	ask := marotte.ElicitationNeededPayload{Mode: marotte.ElicitationModeURL, URL: "https://example.com/connect"}

	got, err := elicitationContent(&ask, map[string]any{"x": 1.0})

	if err != nil || got != nil {
		t.Errorf("elicitationContent(url ask) = %v, %v, want nil, nil", got, err)
	}
}
