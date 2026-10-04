package compact_table

import (
	"encoding/binary"
	"math"
	"testing"
)

type StringKey string

func (StringKey) FromBytes(b []byte) (StringKey, bool) {
	return StringKey(b), true
}

func (s StringKey) ToBytes() ([]byte, bool) {
	return []byte(s), true
}

func TestCompactTableSerializeDeserialize(t *testing.T) {
	enc32 := func(v int32) []byte {
		return binary.BigEndian.AppendUint32(nil, uint32(v))
	}

	fields := []FieldData{
		{
			Name:        "num",
			InitialType: TypeInt32,
			NullValues:  [][]byte{enc32(-1)},
		},
		{
			Name:        "title",
			InitialType: TypeString,
			Tokenizer:   TokenizerStandardText,
		},
	}

	table := New[StringKey]("test", 10, fields)

	table.PutSingleRecord("a", map[string][][]byte{
		"num":   {enc32(100)},
		"title": {[]byte("hello world")},
	})
	table.PutSingleRecord("b", map[string][][]byte{
		"num":   {enc32(-1)}, // null
		"title": {[]byte("goodbye world")},
	})

	table.Compact(nil)

	data := table.Serialize(true, false)

	restored := DeserializeCompactTable[StringKey](
		data,
		"test",
	)

	view, ok := restored.GetMany([]StringKey{"a", "b"}, 0)
	if !ok {
		t.Fatal("GetMany failed after deserialize")
	}

	if view.Count != 2 {
		t.Fatalf("Count=%d want 2", view.Count)
	}

	// num
	num := &view.Columns[0]

	if num.IsNull(0) {
		t.Fatal("num[0] unexpectedly null")
	}
	if int32(num.Values[0]) != 100 {
		t.Fatalf("num[0]=%d want 100", num.Values[0])
	}
	if !num.IsNull(1) {
		t.Fatal("num[1] should be null")
	}

	// title
	title := &view.Columns[1]

	if title.Strings[0] != "hello world" {
		t.Fatalf("title[0]=%q want %q", title.Strings[0], "hello world")
	}
	if title.Strings[1] != "goodbye world" {
		t.Fatalf("title[1]=%q want %q", title.Strings[1], "goodbye world")
	}
}

func TestCompactTableSerializeDeserializeTypes(t *testing.T) {
	be32 := func(v int32) []byte {
		return binary.BigEndian.AppendUint32(nil, uint32(v))
	}
	be64 := func(v int64) []byte {
		return binary.BigEndian.AppendUint64(nil, uint64(v))
	}
	bef32 := func(v float32) []byte {
		return binary.BigEndian.AppendUint32(nil, math.Float32bits(v))
	}
	bef64 := func(v float64) []byte {
		return binary.BigEndian.AppendUint64(nil, math.Float64bits(v))
	}

	fields := []FieldData{
		{Name: "i32", InitialType: TypeInt32, NullValues: [][]byte{be32(-1)}},
		{Name: "i64", InitialType: TypeInt64},
		{Name: "u32", InitialType: TypeUInt32},
		{Name: "u64", InitialType: TypeUInt64},
		{Name: "f32", InitialType: TypeFloat},
		{Name: "f64", InitialType: TypeDouble},
		{Name: "text", InitialType: TypeString, Tokenizer: TokenizerStandardText},
	}

	table := New[StringKey]("types", 10, fields)

	table.PutSingleRecord("a", map[string][][]byte{
		"i32":  {be32(100)},
		"i64":  {be64(100000)},
		"u32":  {be32(200)},
		"u64":  {be64(200000)},
		"f32":  {bef32(1.25)},
		"f64":  {bef64(2.5)},
		"text": {[]byte("hello world")},
	})

	table.PutSingleRecord("b", map[string][][]byte{
		"i32":  {be32(-1)}, // null
		"i64":  {be64(100001)},
		"u32":  {be32(201)},
		"u64":  {be64(200001)},
		"f32":  {bef32(1.5)},
		"f64":  {bef64(3.5)},
		"text": {[]byte("goodbye world")},
	})

	table.Compact(nil)

	data := table.Serialize(true, false)

	restored := DeserializeCompactTable[StringKey](
		data,
		"types",
	)

	view, ok := restored.GetMany([]StringKey{"a", "b"}, 0)
	if !ok {
		t.Fatal("GetMany failed after deserialize")
	}

	if view.Count != 2 {
		t.Fatalf("Count=%d want 2", view.Count)
	}

	tests := []struct {
		name string
		want uint64
	}{
		{"i32", 100},
		{"i64", 100000},
		{"u32", 200},
		{"u64", 200000},
	}

	for i, tc := range tests {
		column := &view.Columns[i]

		if column.IsNull(0) {
			t.Fatalf("%s[0] unexpectedly null", tc.name)
		}
		if column.Values[0] != tc.want {
			t.Fatalf("%s[0]=%d want %d", tc.name, column.Values[0], tc.want)
		}
	}

	if !view.Columns[0].IsNull(1) {
		t.Fatal("i32[1] should be null")
	}

	if view.Columns[1].Values[1] != 100001 {
		t.Fatalf("i64[1]=%d want 100001", view.Columns[1].Values[1])
	}

	if view.Columns[2].Values[1] != 201 {
		t.Fatalf("u32[1]=%d want 201", view.Columns[2].Values[1])
	}

	if view.Columns[3].Values[1] != 200001 {
		t.Fatalf("u64[1]=%d want 200001", view.Columns[3].Values[1])
	}

	if math.Float32frombits(uint32(view.Columns[4].Values[0])) != 1.25 {
		t.Fatalf("f32[0] wrong")
	}

	if math.Float64frombits(view.Columns[5].Values[0]) != 2.5 {
		t.Fatalf("f64[0] wrong")
	}

	if view.Columns[6].Strings[0] != "hello world" {
		t.Fatalf("text[0]=%q want %q",
			view.Columns[6].Strings[0], "hello world")
	}

	if view.Columns[6].Strings[1] != "goodbye world" {
		t.Fatalf("text[1]=%q want %q",
			view.Columns[6].Strings[1], "goodbye world")
	}
}
func TestCompactTableSerializeDeserializeIndexed(t *testing.T) {
	authors := New[StringKey]("authors", 3, []FieldData{
		{Name: "name", InitialType: TypeString, Tokenizer: TokenizerStandardText},
	})

	authors.PutSingleRecord("a", map[string][][]byte{
		"name": {[]byte("Alice")},
	})
	authors.PutSingleRecord("b", map[string][][]byte{
		"name": {[]byte("Bob")},
	})

	books := New[StringKey]("books", 3, []FieldData{
		{
			Name:              "author",
			InitialType:       TypeExternalIndex,
			ExternalTableName: "authors",
		},
	})

	books.PutSingleRecord("book1", map[string][][]byte{
		"author": {[]byte("b")},
	})
	books.PutSingleRecord("book2", map[string][][]byte{
		"author": {[]byte("a")},
	})

	authors.Compact(nil)
	books.Compact(map[string]*CompactTable[StringKey]{
		"authors": authors,
	})

	data := books.Serialize(true, false)

	restored := DeserializeCompactTable[StringKey](
		data,
		"books",
	)

	view, ok := restored.GetMany(
		[]StringKey{"book1", "book2"},
		0,
	)
	if !ok {
		t.Fatal("GetMany failed after deserialize")
	}

	column := &view.Columns[0]

	if column.IsNull(0) || column.IsNull(1) {
		t.Fatal("author unexpectedly null")
	}

	if column.Refs[0] != 1 {
		t.Fatalf("book1 ref=%d want 1", column.Refs[0])
	}

	if column.Refs[1] != 0 {
		t.Fatalf("book2 ref=%d want 0", column.Refs[1])
	}

	// Verify external table is available after deserialization.
	row0 := restored.Row(view, 0)
	row1 := restored.Row(view, 1)

	if got := row0["author"]; got != StringKey("b") {
		t.Fatalf("book1 author=%v want %q", got, "b")
	}

	if got := row1["author"]; got != StringKey("a") {
		t.Fatalf("book2 author=%v want %q", got, "a")
	}
}

func TestCompactTableSerializeReverseIndex(t *testing.T) {
	authors := New[StringKey]("authors", 3, []FieldData{
		{Name: "name", InitialType: TypeString, Tokenizer: TokenizerStandardText, WithSearchIndex: true},
	})

	authors.PutSingleRecord("a", map[string][][]byte{
		"name": {[]byte("Alice")},
	})
	authors.PutSingleRecord("b", map[string][][]byte{
		"name": {[]byte("Bob")},
	})

	authors.Compact(nil)

	offsets_init := authors.Fields[0].SearchIndex.Offsets

	data := authors.Serialize(true, false)

	restored := DeserializeCompactTable[StringKey](
		data,
		"authors",
	)

	if restored.Fields[0].SearchIndex.Kind != SearchIndexInverted {
		t.Fatalf("restored.Fields[0].SearchIndex.Kind != SearchIndexInverte")
	}

	assert(t, len(offsets_init) > 0, "len(values_init) > 0")
	for i, _ := range offsets_init {
		assert(t, offsets_init[i] == restored.Fields[0].SearchIndex.Offsets[i], "values_init[i] == restored.Fields[0].SearchIndex.Values[i]")
	}

}
