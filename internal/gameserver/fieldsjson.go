package gameserver

import (
	"encoding/json"
	"fmt"
	"strings"

	"rf4server/internal/proto"
)

// 物品字段快照 JSON ↔ proto.Field 互转。
// 格式（item.fields_json）：
//
//	[{"f":"u32","v":7}, {"f":"str","v":"x"|null}, {"f":"guid","g":"hex"},
//	 {"f":"optguid","v":"hex"|null}, {"f":"opt","v":0}, {"f":"bytes","hex":".."},
//	 {"f":"float","v":1.5}, {"f":"double","v":2.5}, {"f":"u64","v":9}, {"f":"byte","v":3},
//	 {"f":"u16","v":5|null}, {"f":"val","v":{tree}} ...]

// JSON 使用小写操作名，内存 proto.Field.Op 使用 wiregen 的大写名称。
// FieldsFromJSON 输出大写 Op；FieldsToJSON 和 fieldToPy 输出小写 JSON 名。
var jsonOpToWire = map[string]string{
	"guid": "GUID16", "optguid": "OPTGUID", "bytes": "BYTES", "str": "STR",
	"u16str": "U16STR", "u32n": "U32N", "float": "FLOAT", "double": "DOUBLE",
	"u32": "U32", "u64": "U64", "byte": "BYTE", "u16": "U16", "opt": "OPT",
	"val": "VAL", "dict": "DICT", "array": "ARRAY", "u32arr": "U32ARR",
	"str3": "STR3", "kv": "KV",
}

func wireOpToJSONName(op string) string {
	switch op {
	case "GUID16":
		return "guid"
	case "OPTGUID":
		return "optguid"
	case "BYTES":
		return "bytes"
	case "STR":
		return "str"
	case "U16STR":
		return "u16str"
	case "U32N":
		return "u32n"
	case "FLOAT":
		return "float"
	case "DOUBLE":
		return "double"
	case "U32":
		return "u32"
	case "U64":
		return "u64"
	case "BYTE":
		return "byte"
	case "U16":
		return "u16"
	case "OPT":
		return "opt"
	case "VAL":
		return "val"
	case "DICT":
		return "dict"
	case "ARRAY":
		return "array"
	case "U32ARR":
		return "u32arr"
	case "STR3":
		return "str3"
	case "KV":
		return "kv"
	}
	return strings.ToLower(op)
}

type pyField struct {
	F   string           `json:"f"`
	V   *json.RawMessage `json:"v,omitempty"`
	G   string           `json:"g,omitempty"`
	Hex string           `json:"hex,omitempty"`
}

// FieldsFromJSON 解析 fields_json -> []proto.Field。
func FieldsFromJSON(s string) ([]proto.Field, error) {
	if s == "" {
		return nil, nil
	}
	var raw []pyField
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("fields_json 解析: %w", err)
	}
	out := make([]proto.Field, 0, len(raw))
	for _, pf := range raw {
		op := strings.ToLower(pf.F)
		f := proto.Field{Op: op}
		if wire, ok := jsonOpToWire[op]; ok {
			f.Op = wire
		}
		switch op {
		case "guid":
			f.Hex = pf.G
		case "optguid":
			if pf.V != nil && string(*pf.V) != "null" {
				_ = json.Unmarshal(*pf.V, &f.Hex)
			}
		case "bytes":
			f.Hex = pf.Hex
		case "float", "double":
			if pf.V != nil && string(*pf.V) != "null" {
				_ = json.Unmarshal(*pf.V, &f.Float)
			}
		case "str", "u16str", "u32n":
			if pf.V != nil && string(*pf.V) != "null" {
				var s string
				if err := json.Unmarshal(*pf.V, &s); err == nil {
					f.Str = &s
				}
			}
		case "val":
			if pf.V != nil && string(*pf.V) != "null" {
				var tv pyTree
				if err := json.Unmarshal(*pf.V, &tv); err == nil {
					f.Val = treeFromPy(tv)
				}
			}
		default: // u32/u64/byte/u16/opt
			if pf.V != nil && string(*pf.V) != "null" {
				var n uint64
				if err := json.Unmarshal(*pf.V, &n); err == nil {
					f.Num = &n
				}
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// FieldsToJSON 序列化 []proto.Field -> fields_json。
func FieldsToJSON(fields []proto.Field) (string, error) {
	out := make([]pyField, 0, len(fields))
	for i := range fields {
		f := &fields[i]
		op := wireOpToJSONName(f.Op)
		pf := pyField{F: op}
		switch op {
		case "guid":
			pf.G = f.Hex
		case "optguid":
			if f.Hex == "" {
				pf.V = rawNull()
			} else {
				pf.V = rawString(f.Hex)
			}
		case "bytes":
			pf.Hex = f.Hex
		case "float", "double":
			pf.V = rawFloat(f.Float)
		case "str", "u16str", "u32n":
			if f.Str == nil {
				pf.V = rawNull()
			} else {
				pf.V = rawString(*f.Str)
			}
		case "val":
			if f.Val == nil {
				pf.V = rawNull()
			} else {
				b, err := json.Marshal(treeToPy(f.Val))
				if err != nil {
					return "", err
				}
				rm := json.RawMessage(b)
				pf.V = &rm
			}
		default:
			if f.Num == nil {
				pf.V = rawNull()
			} else {
				pf.V = rawUint(*f.Num)
			}
		}
		out = append(out, pf)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func rawNull() *json.RawMessage {
	rm := json.RawMessage("null")
	return &rm
}

func rawString(s string) *json.RawMessage {
	b, _ := json.Marshal(s)
	rm := json.RawMessage(b)
	return &rm
}

func rawFloat(v float64) *json.RawMessage {
	b, _ := json.Marshal(v)
	rm := json.RawMessage(b)
	return &rm
}

func rawUint(v uint64) *json.RawMessage {
	b, _ := json.Marshal(v)
	rm := json.RawMessage(b)
	return &rm
}

// ---- value tree JSON（val 字段内的树）----
// 形如 {"t":"u32","v":7,"m":16} / {"t":"args","code":"524","items":[...]} / {"t":"obj","type_id":N,"class":"x","fields":[...]}

type pyTree struct {
	T      string            `json:"t"`
	V      *json.RawMessage  `json:"v,omitempty"`
	M      *int              `json:"m,omitempty"`
	Num    *json.RawMessage  `json:"-"`
	Bool   *bool             `json:"-"`
	G      string            `json:"g,omitempty"`
	Hex    string            `json:"hex,omitempty"`
	TypeID *uint32           `json:"type_id,omitempty"`
	Class  string            `json:"class,omitempty"`
	Code   *string           `json:"code,omitempty"`
	Type   string            `json:"type,omitempty"`
	Items  []json.RawMessage `json:"items,omitempty"`
	Fields []pyField         `json:"fields,omitempty"`
	S      []json.RawMessage `json:"s,omitempty"`
	K      *string           `json:"k,omitempty"`
}

func treeFromPy(p pyTree) *proto.Value {
	if p.T == "" {
		return proto.Null()
	}
	v := &proto.Value{Kind: proto.Kind(p.T)}
	switch v.Kind {
	case proto.KindU32, proto.KindU64, proto.KindDateTime:
		if p.V != nil {
			var n uint64
			_ = json.Unmarshal(*p.V, &n)
			v.Num = n
		}
		if p.M != nil {
			v.Marker = byte(*p.M)
		}
	case proto.KindBool:
		if p.V != nil {
			_ = json.Unmarshal(*p.V, &v.Bool)
		}
	case proto.KindStr:
		if p.V != nil && string(*p.V) != "null" {
			var s string
			_ = json.Unmarshal(*p.V, &s)
			v.Str = &s
		}
		if p.M != nil {
			v.Marker = byte(*p.M)
		}
	case proto.KindGuid:
		v.Hex = p.G
		if p.M != nil {
			v.Marker = byte(*p.M)
		}
	case proto.KindBytes:
		v.Hex = p.Hex
	case proto.KindObj:
		if p.TypeID != nil {
			v.TypeID = *p.TypeID
		}
		v.Class = p.Class
		fields, _ := fieldsFromPy(p.Fields)
		v.Fields = fields
	case proto.KindArgs:
		v.Code = p.Code
		v.TypeName = p.Type
		v.Items = treesFromPy(p.Items)
	case proto.KindValList, proto.KindU32Arr, proto.KindGuidArr, proto.KindArray:
		v.Items = treesFromPy(p.Items)
	case proto.KindDict:
		v.TypeName = p.Type
		// pairs 略：dict 项以 items 二元组存，重建罕见（物品树基本不用 dict）
	case proto.KindStr3:
		if len(p.S) == 3 {
			for i := range p.S {
				var s *string
				if string(p.S[i]) != "null" {
					var sv string
					_ = json.Unmarshal(p.S[i], &sv)
					s = &sv
				}
				v.S3[i] = s
			}
		}
	case proto.KindKV:
		if len(p.S) >= 2 {
			for i := 0; i < 2; i++ {
				var s *string
				if string(p.S[i]) != "null" {
					var sv string
					_ = json.Unmarshal(p.S[i], &sv)
					s = &sv
				}
				v.S3[i] = s
			}
		}
	case proto.KindElemNull, proto.KindNull:
	}
	return v
}

func treesFromPy(items []json.RawMessage) []*proto.Value {
	out := make([]*proto.Value, 0, len(items))
	for _, it := range items {
		var p pyTree
		if err := json.Unmarshal(it, &p); err == nil {
			out = append(out, treeFromPy(p))
		}
	}
	return out
}

func fieldsFromPy(fs []pyField) ([]proto.Field, error) {
	b, err := json.Marshal(fs)
	if err != nil {
		return nil, err
	}
	return FieldsFromJSON(string(b))
}

func treeToPy(v *proto.Value) map[string]any {
	if v == nil {
		return map[string]any{"t": "null"}
	}
	m := map[string]any{"t": string(v.Kind)}
	switch v.Kind {
	case proto.KindU32, proto.KindU64, proto.KindDateTime:
		m["v"] = v.Num
		if v.Marker != 0 {
			m["m"] = int(v.Marker)
		}
	case proto.KindBool:
		m["v"] = v.Bool
	case proto.KindStr:
		if v.Str == nil {
			m["v"] = nil
		} else {
			m["v"] = *v.Str
		}
		if v.Marker != 0 {
			m["m"] = int(v.Marker)
		}
	case proto.KindGuid:
		m["g"] = v.Hex
		if v.Marker != 0 {
			m["m"] = int(v.Marker)
		}
	case proto.KindBytes:
		m["hex"] = v.Hex
		m["len"] = len(v.Hex) / 2
	case proto.KindObj:
		m["type_id"] = v.TypeID
		m["class"] = v.Class
		fs := make([]map[string]any, 0, len(v.Fields))
		for i := range v.Fields {
			fs = append(fs, fieldToPy(&v.Fields[i]))
		}
		m["fields"] = fs
	case proto.KindArgs:
		m["code"] = v.Code
		items := make([]any, 0, len(v.Items))
		for _, it := range v.Items {
			items = append(items, treeToPy(it))
		}
		m["items"] = items
	case proto.KindValList, proto.KindU32Arr, proto.KindGuidArr, proto.KindArray:
		items := make([]any, 0, len(v.Items))
		for _, it := range v.Items {
			if it.Kind == proto.KindBytes && v.Kind == proto.KindArray {
				items = append(items, map[string]any{"t": "bytes", "hex": it.Hex})
				continue
			}
			items = append(items, treeToPy(it))
		}
		m["items"] = items
	case proto.KindStr3:
		m["s"] = []any{sOrNull(v.S3[0]), sOrNull(v.S3[1]), sOrNull(v.S3[2])}
	case proto.KindKV:
		m["k"] = sOrNull(v.S3[0])
		m["v"] = sOrNull(v.S3[1])
	case proto.KindElemNull, proto.KindNull, proto.KindDict, proto.KindTypedU32Arr:
		// 简化：TypedU32Arr 补 items
		if v.Kind == proto.KindTypedU32Arr {
			m["type"] = v.TypeName
			items := make([]any, 0, len(v.Items))
			for _, it := range v.Items {
				items = append(items, map[string]any{"t": "u32", "v": it.Num})
			}
			m["items"] = items
		}
	}
	return m
}

func fieldToPy(f *proto.Field) map[string]any {
	op := wireOpToJSONName(f.Op)
	m := map[string]any{"f": op}
	switch op {
	case "guid":
		m["g"] = f.Hex
	case "optguid":
		if f.Hex == "" {
			m["v"] = nil
		} else {
			m["v"] = f.Hex
		}
	case "bytes":
		m["hex"] = f.Hex
	case "float", "double":
		m["v"] = f.Float
	case "str", "u16str", "u32n":
		if f.Str == nil {
			m["v"] = nil
		} else {
			m["v"] = *f.Str
		}
	case "val":
		if f.Val == nil {
			m["v"] = map[string]any{"t": "null"}
		} else {
			m["v"] = treeToPy(f.Val)
		}
	default:
		if f.Num == nil {
			m["v"] = nil
		} else {
			m["v"] = *f.Num
		}
	}
	return m
}

func sOrNull(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
