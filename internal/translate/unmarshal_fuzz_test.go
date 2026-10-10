package translate

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// FuzzUnmarshalParams pins that unmarshalParams reports success exactly when a direct
// json.Unmarshal into the same type does, with the same decoded value.
func FuzzUnmarshalParams(f *testing.F) {
	f.Add([]byte(`{"commands":[{"name":"x"}]}`))
	f.Add([]byte(`{"subagents":[{"group":"g","status":{"type":"running"}}]}`))
	f.Add([]byte(`{"contextUsagePercentage":42.5,"meteringUsage":[{"unitPlural":"credits","value":3}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`"string"`))
	f.Add([]byte{0xff, 0xfe}) // invalid UTF-8

	f.Fuzz(func(t *testing.T, data []byte) {
		msg := &marotte.RPCResponse{
			JSONRPC: "2.0",
			Method:  "test",
			Params:  json.RawMessage(data),
		}
		assertUnmarshalParamsConsistent[acpChunkWire](t, msg, data)
		assertUnmarshalParamsConsistent[usageUpdate](t, msg, data)
		assertUnmarshalParamsConsistent[acpToolCallWire](t, msg, data)
	})
}

func assertUnmarshalParamsConsistent[T any](t *testing.T, msg *marotte.RPCResponse, data []byte) {
	t.Helper()
	got, ok := unmarshalParams[T](msg, "test")
	var want T
	wantErr := json.Unmarshal(data, &want)
	if (wantErr == nil) != ok {
		t.Fatalf("%T: unmarshalParams ok=%v but json.Unmarshal err=%v (must agree)", want, ok, wantErr)
	}
	if ok && !reflect.DeepEqual(got, want) {
		t.Fatalf("%T: unmarshalParams=%+v, direct json.Unmarshal=%+v (must match on success)", want, got, want)
	}
}
