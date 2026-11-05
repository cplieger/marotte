package command

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/marotte"
)

var (
	errElicitationUnknownField = errors.New("names no field of the form")
	errElicitationMissing      = errors.New("required field is missing")
	errElicitationNull         = errors.New("value is null")
	errElicitationType         = errors.New("value has the wrong type")
	errElicitationRange        = errors.New("value is out of range")
	errElicitationChoice       = errors.New("value is not one of the choices")
)

// elicitationContent is the content an accept on ask forwards: nil for a URL-mode ask, else
// content once it satisfies the ask's schema. It checks what MCP's primitive schema can state
// apart from format and pattern, which the card enforces: KAS's SDK defines no pattern, and
// RE2 cannot compile an ECMAScript one in general. Lengths count code points, as JSON Schema does.
func elicitationContent(ask *marotte.ElicitationNeededPayload, content map[string]any) (map[string]any, error) {
	if ask.Mode == marotte.ElicitationModeURL {
		return nil, nil
	}
	var props map[string]marotte.ElicitationPropertySchema
	var required []string
	if s := ask.RequestedSchema; s != nil {
		props, required = s.Properties, s.Required
	}
	for name, v := range content {
		prop, ok := props[name]
		if !ok {
			return nil, fmt.Errorf("%q: %w", name, errElicitationUnknownField)
		}
		if err := checkElicitationValue(&prop, v); err != nil {
			return nil, fmt.Errorf("%q: %w", name, err)
		}
	}
	// A required name with no property is ignored: refusing it would leave the user only Decline.
	for _, name := range required {
		if _, isProp := props[name]; !isProp {
			continue
		}
		if _, given := content[name]; !given {
			return nil, fmt.Errorf("%q: %w", name, errElicitationMissing)
		}
	}
	return content, nil
}

func checkElicitationValue(prop *marotte.ElicitationPropertySchema, v any) error {
	if v == nil {
		return errElicitationNull
	}
	switch prop.Type {
	case "boolean":
		if _, ok := v.(bool); !ok {
			return errElicitationType
		}
		return nil
	case "number", "integer":
		return checkElicitationNumber(prop, v)
	case "array":
		return checkElicitationChoices(prop, v)
	default:
		return checkElicitationString(prop, v)
	}
}

func checkElicitationNumber(prop *marotte.ElicitationPropertySchema, v any) error {
	n, ok := v.(float64)
	if !ok || (prop.Type == "integer" && n != math.Trunc(n)) {
		return errElicitationType
	}
	if (prop.Minimum != nil && n < *prop.Minimum) || (prop.Maximum != nil && n > *prop.Maximum) {
		return errElicitationRange
	}
	return nil
}

func checkElicitationString(prop *marotte.ElicitationPropertySchema, v any) error {
	s, ok := v.(string)
	if !ok {
		return errElicitationType
	}
	if choices := singleSelectChoices(prop); choices != nil {
		if !slices.Contains(choices, s) {
			return errElicitationChoice
		}
		return nil
	}
	n := utf8.RuneCountInString(s)
	if (prop.MinLength != nil && n < *prop.MinLength) || (prop.MaxLength != nil && n > *prop.MaxLength) {
		return errElicitationRange
	}
	return nil
}

func checkElicitationChoices(prop *marotte.ElicitationPropertySchema, v any) error {
	items, ok := v.([]any)
	if !ok {
		return errElicitationType
	}
	choices := multiSelectChoices(prop)
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return errElicitationType
		}
		if choices != nil && !slices.Contains(choices, s) {
			return errElicitationChoice
		}
	}
	if (prop.MinItems != nil && len(items) < *prop.MinItems) || (prop.MaxItems != nil && len(items) > *prop.MaxItems) {
		return errElicitationRange
	}
	return nil
}

// singleSelectChoices is a string field's allowed values, nil for a free-text one.
func singleSelectChoices(prop *marotte.ElicitationPropertySchema) []string {
	if len(prop.Enum) > 0 {
		return prop.Enum
	}
	return optionConsts(prop.OneOf)
}

// multiSelectChoices is a multi-select's allowed items, nil when its schema lists none.
func multiSelectChoices(prop *marotte.ElicitationPropertySchema) []string {
	if prop.Items == nil {
		return nil
	}
	if len(prop.Items.Enum) > 0 {
		return prop.Items.Enum
	}
	return optionConsts(prop.Items.AnyOf)
}

func optionConsts(opts []marotte.ElicitationEnumOption) []string {
	if len(opts) == 0 {
		return nil
	}
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.Const
	}
	return out
}
