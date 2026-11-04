package gameserver

import (
	"bytes"
	"testing"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func TestFieldsJSONPreservesNativeStringWidths(t *testing.T) {
	text := "钓组尖梢 1.25 oz"
	for _, op := range []string{"STR", "U16STR", "U32N"} {
		for _, value := range []*string{nil, proto.S(""), &text} {
			fields := []proto.Field{{Op: op, Str: value}}
			encoded, err := FieldsToJSON(fields)
			if err != nil {
				t.Fatal(err)
			}
			got, err := FieldsFromJSON(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Op != op || (got[0].Str == nil) != (value == nil) {
				t.Fatalf("string shape lost for %s", op)
			}
			if value != nil && *got[0].Str != *value {
				t.Fatalf("string lost for %s", op)
			}
		}
	}
}

func TestFieldsJSONPreservesNestedRodTip(t *testing.T) {
	table := wiregen.Table()
	rod := wiregen.MustMakeObj("cnolibojkao", map[int]*proto.Value{8: proto.StrVal("tip_example")})
	before, err := proto.EncodeValue(rod, table)
	if err != nil {
		t.Fatal(err)
	}
	jsonFields, err := FieldsToJSON([]proto.Field{{Op: "VAL", Val: rod}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := FieldsFromJSON(jsonFields)
	if err != nil {
		t.Fatal(err)
	}
	after, err := proto.EncodeValue(got[0].Val, table)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("nested native string changed after JSON storage")
	}
}
