package rule

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// serverFields are written only by the server, so a client body naming one is refused with its own sentence.
var serverFields = map[string]bool{"id": true, "status": true, "hook_id": true, "secret": true, "digest": true, "rotated_at": true}

// Decode reads a client's rule spec, refusing every unknown, server-held and mistyped field at its own path, all
// at once. A spec it answers with no error still needs Validate.
func Decode(data []byte) (Spec, []FieldError) {
	var errs errList
	if !json.Valid(data) {
		errs.add("", "This is not a rule")
		return Spec{}, errs
	}
	checkShape(data, reflect.TypeFor[Spec](), "", errs.add)
	if len(errs) > 0 {
		return Spec{}, errs
	}
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		errs.add("", "This is not a rule")
		return Spec{}, errs
	}
	return spec, nil
}

func checkShape(raw json.RawMessage, typ reflect.Type, path string, add addFunc) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return
	}
	switch typ.Kind() {
	case reflect.Pointer:
		checkShape(raw, typ.Elem(), path, add)
	case reflect.Struct:
		checkObject(raw, typ, path, add)
	case reflect.Slice:
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			add(path, "This must be a list")
			return
		}
		for i, item := range items {
			checkShape(item, typ.Elem(), fmt.Sprintf("%s[%d]", path, i), add)
		}
	default:
		if err := json.Unmarshal(raw, reflect.New(typ).Interface()); err != nil {
			add(path, kindSentence(typ.Kind()))
		}
	}
}

func checkObject(raw json.RawMessage, typ reflect.Type, path string, add addFunc) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		add(path, "This must be an object")
		return
	}
	names := make(map[string]reflect.Type, typ.NumField())
	var order []string
	for f := range typ.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.IsExported() && name != "" && name != "-" {
			names[name] = f.Type
			order = append(order, name)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		switch {
		case names[k] != nil:
		case serverFields[k]:
			add(joinPath(path, k), "The server writes this field")
		default:
			add(joinPath(path, k), "This field is not part of a rule")
		}
	}
	for _, name := range order {
		if v, ok := obj[name]; ok {
			checkShape(v, names[name], joinPath(path, name), add)
		}
	}
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func kindSentence(k reflect.Kind) string {
	switch k {
	case reflect.String:
		return "This must be text"
	case reflect.Bool:
		return "This must be true or false"
	default:
		return "This must be a whole number"
	}
}
