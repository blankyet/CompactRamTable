package compact_table

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"testing"
)

func TestCompactTableNoNulls(t *testing.T) {
	table := New[string]("test", 100, []FieldData{{Name: "title"}})

	table.PutSingleRecord("a", map[string][][]byte{"title": {[]byte("one")}})
	table.PutSingleRecord("b", map[string][][]byte{"title": {[]byte("two")}})

	f := &table.Fields[0]
	assert(t, !f.HasNullBits, "HasNullBits should stay false without nulls")
	assert(t, !f.IsNull(0) && !f.IsNull(1), "no element should be null")
}

func TestCompactTableSimplePut(t *testing.T) {
	fields := []FieldData{
		{Name: "title", NullValues: [][]byte{[]byte("N/A")}},
		{Name: "aliases", IsList: true},
		{Name: "url"},
	}

	table := New[string]("test", 100_000, fields)

	records := []map[string][][]byte{
		{
			"title":   {[]byte("the quick brown fox jumps over the lazy dog")},
			"aliases": {[]byte("quick-brown-fox"), []byte("lazy-dog")},
			"url":     {[]byte("https://example.com/movies/the-quick-brown-fox")},
		},
		{
			"title":   {[]byte("the quick brown fox runs through the forest")},
			"aliases": {[]byte("brown-fox")},
			"url":     {[]byte("https://example.com/movies/forest")},
		},
		{
			"title":   {[]byte("N/A")}, // matches NullValues
			"aliases": {},              // empty list
			"url":     {[]byte("https://example.com/movies/nulls")},
		},
	}

	for i, data := range records {
		table.PutSingleRecord(fmt.Sprintf("id-%d", i), data)
	}

	assert(t, table.ElementCount == uint32(len(records)),
		"element count=%d", table.ElementCount)

	assert(t, string(table.Fields[0].OriginalData[0][0]) == "the quick brown fox jumps over the lazy dog",
		"title mismatch")

	assert(t, len(table.Fields[1].OriginalData[0]) == 2,
		"aliases count=%d", len(table.Fields[1].OriginalData[0]))

	assert(t, string(table.Fields[1].OriginalData[0][0]) == "quick-brown-fox",
		"first alias mismatch")

	assert(t, string(table.Fields[1].OriginalData[0][1]) == "lazy-dog",
		"second alias mismatch")

	assert(t, string(table.Fields[2].OriginalData[0][0]) == "https://example.com/movies/the-quick-brown-fox",
		"url mismatch")

	// nulls
	assert(t, table.Fields[0].IsNull(2), "title[2] should be null (NullValues)")
	assert(t, table.Fields[1].IsNull(2), "aliases[2] should be null (empty list)")

	// non-nulls
	assert(t, !table.Fields[0].IsNull(0) && !table.Fields[1].IsNull(0),
		"record 0 should have no nulls")
	assert(t, !table.Fields[2].IsNull(2), "url[2] is a real value")
}

func be32(v int32) []byte {
	return binary.BigEndian.AppendUint32(nil, uint32(v))
}

func TestCompactInt32Scaled(t *testing.T) {
	fields := []FieldData{
		{Name: "num", InitialType: TypeInt32, NullValues: [][]byte{be32(-1)}},
	}
	table := New[string]("test", 100, fields)

	values := []int32{100, 105, -1, 110, 100} // index 2 is the null sentinel
	for i, v := range values {
		table.PutSingleRecord(fmt.Sprintf("id-%d", i),
			map[string][][]byte{"num": {be32(v)}})
	}

	table.Compact(nil)

	f := &table.Fields[0]

	assert(t, table.Compacted, "table should be compacted")
	assert(t, !f.Empty, "field should not be empty")
	assert(t, f.CompactingStrategy == CompactingScaled,
		"strategy=%v", f.CompactingStrategy)
	assert(t, f.FinalType == TypeByte, "finalType=%v", f.FinalType)
	assert(t, len(f.Helpers) == 1 && f.Helpers[0] == 100,
		"helpers=%v want [100]", f.Helpers)

	// null removed: 5 slots -> 4 dense values, stored as value-min
	want := []uint8{0, 5, 10, 0}
	assert(t, len(f.ByteValues) == len(want),
		"len(ByteValues)=%d want %d", len(f.ByteValues), len(want))
	for i, w := range want {
		assert(t, f.ByteValues[i] == w, "ByteValues[%d]=%d want %d", i, f.ByteValues[i], w)
	}

	// null bitmap survived compaction
	assert(t, f.IsNull(2), "index 2 should be null")
	assert(t, !f.IsNull(0) && !f.IsNull(1) && !f.IsNull(3) && !f.IsNull(4),
		"other indexes should not be null")

	// read back through the rank prefix: original index 3 -> dense index 2
	i := uint32(3)
	w := i >> 6
	dense := i - (f.NullPrefix[w] + uint32(bits.OnesCount64(f.NullBits[w]&((uint64(1)<<(i&63))-1))))
	assert(t, dense == 2, "dense index=%d want 2", dense)
	assert(t, int32(f.Helpers[0])+int32(f.ByteValues[dense]) == 110,
		"decoded value wrong")
}

func getString(field *Field, index uint32, tokenizer Tokenizer) (string, bool) {
	if field.IsNull(index) {
		return "", false
	}

	dense := index
	if field.HasNullBits {
		w := index >> 6
		mask := (uint64(1) << (index & 63)) - 1
		dense -= field.NullPrefix[w] + uint32(bits.OnesCount64(field.NullBits[w]&mask))
	}

	tokens := field.ByteValues[field.ListOffsets[dense]:field.ListOffsets[dense+1]]
	return tokenizer.Detokenize(tokens), true
}

func TestCompactStringTokenized(t *testing.T) {
	fields := []FieldData{
		{
			Name:        "title",
			InitialType: TypeString,
			Tokenizer:   TokenizerStandardText,
			NullValues:  [][]byte{[]byte("N/A")},
		},
	}
	table := New[string]("test", 100, fields)

	values := []string{
		"the quick brown fox", // 0
		"N/A",                 // 1: NullValues sentinel
		"the lazy dog",        // 2
		"",                    // 3: empty string, nulled by TryCompactStrings
		"the quick dog",       // 4
	}
	for i, v := range values {
		table.PutSingleRecord(fmt.Sprintf("id-%d", i),
			map[string][][]byte{"title": {[]byte(v)}})
	}

	table.Compact(nil)

	f := &table.Fields[0]
	tk := table.WordTokenizer

	assert(t, table.Compacted, "table should be compacted")
	assert(t, !f.Empty, "field should not be empty")
	assert(t, f.CompactingStrategy == CompactingTokenized,
		"strategy=%v", f.CompactingStrategy)
	assert(t, f.FinalType == TypeByte, "finalType=%v", f.FinalType)

	// the vocabulary is tiny, so every word is popular and ids fit in 2 bytes
	// (3 bytes per record with the continuation flag)
	assert(t, tk.GetByteCountPerToken() == 2,
		"bytesPerToken=%d want 2", tk.GetByteCountPerToken())
	assert(t, len(f.Helpers) == 1 && f.Helpers[0] == 2,
		"helpers=%v want [2]", f.Helpers)

	// 3 non-null strings with 4, 3 and 3 words, 3 bytes per word
	wantOffsets := []uint32{0, 12, 21, 30}
	assert(t, len(f.ListOffsets) == len(wantOffsets),
		"len(ListOffsets)=%d want %d", len(f.ListOffsets), len(wantOffsets))
	for i, w := range wantOffsets {
		assert(t, f.ListOffsets[i] == w, "ListOffsets[%d]=%d want %d", i, f.ListOffsets[i], w)
	}
	assert(t, len(f.ByteValues) == 30, "len(ByteValues)=%d want 30", len(f.ByteValues))

	// nulls
	assert(t, f.IsNull(1), "index 1 should be null (NullValues)")
	assert(t, f.IsNull(3), "index 3 should be null (empty string)")
	assert(t, !f.IsNull(0) && !f.IsNull(2) && !f.IsNull(4),
		"indexes 0, 2, 4 should not be null")

	// original strings released
	for i := range values {
		assert(t, f.OriginalData == nil, "OriginalData[%d] should be released", i)
	}

	// round trip through the rank prefix
	for _, c := range []struct {
		index uint32
		want  string
	}{
		{0, "the quick brown fox"},
		{2, "the lazy dog"},
		{4, "the quick dog"},
	} {
		got, ok := getString(f, c.index, tk)
		assert(t, ok && got == c.want, "GetString(%d)=%q,%v want %q", c.index, got, ok, c.want)
	}
	for _, i := range []uint32{1, 3} {
		_, ok := getString(f, i, tk)
		assert(t, !ok, "GetString(%d) should report null", i)
	}
}

type UUIDT [16]byte

// Value receiver, doesn't read the receiver (it's called on the zero value).
// Expects the raw 16 bytes; anything else is treated as null.
func (UUIDT) FromBytes(b []byte) (UUIDT, bool) {
	var u UUIDT
	if len(b) != len(u) {
		return u, false
	}
	copy(u[:], b)
	return u, true
}

func (u UUIDT) ToBytes() ([]byte, bool) {
	return u[:], true
}

func testUUID(n byte) UUIDT {
	var u UUIDT
	for i := range u {
		u[i] = n + byte(i)
	}
	return u
}

func TestCompactExternalIndexUUID(t *testing.T) {
	a, b, c := testUUID(1), testUUID(2), testUUID(3)

	authors := New[UUIDT]("authors", 10, []FieldData{{Name: "name"}})
	for i, k := range []UUIDT{a, b, c} {
		authors.PutSingleRecord(k, map[string][][]byte{"name": {[]byte{byte('a' + i)}}})
	}

	books := New[UUIDT]("books", 10, []FieldData{
		{Name: "author", InitialType: TypeExternalIndex, ExternalTableName: "authors"},
	})

	values := [][]byte{
		b[:],
		nil,
		c[:],
		a[:],
	}
	for i, v := range values {
		data := map[string][][]byte{}
		if v != nil {
			data["author"] = [][]byte{v}
		}
		books.PutSingleRecord(testUUID(byte(100+i)), data)
	}

	books.Compact(map[string]*CompactTable[UUIDT]{"authors": authors})

	f := &books.Fields[0]

	assert(t, !f.Empty, "field should not be empty")
	assert(t, f.CompactingStrategy == CompactingIndexed, "strategy=%v", f.CompactingStrategy)
	assert(t, f.FinalType == Type4Byte, "finalType=%v", f.FinalType)

	want := []uint32{1, 2, 0} // b, c, a
	assert(t, len(f.Byte4Values) == len(want), "len=%d want %d", len(f.Byte4Values), len(want))
	for i, w := range want {
		assert(t, f.Byte4Values[i] == w, "Byte4Values[%d]=%d want %d", i, f.Byte4Values[i], w)
	}

	// read back: original index 3 -> dense 2 -> authors row 0 -> key a
	i := uint32(3)
	w := i >> 6
	dense := i - (f.NullPrefix[w] + uint32(bits.OnesCount64(f.NullBits[w]&((uint64(1)<<(i&63))-1))))
	assert(t, dense == 2, "dense=%d want 2", dense)
	assert(t, authors.IndexToKey[f.Byte4Values[dense]] == a, "resolved key wrong")
}

func TestDenseIndices(t *testing.T) {
	const n = 300

	cases := []struct {
		name   string
		isNull func(i int) bool
	}{
		{"with nulls", func(i int) bool { // границы слов: 63/64, 127/128
			return i%3 == 0 || i == 63 || i == 64 || i == 127 || i == 128
		}},
		{"no nulls", func(i int) bool { return false }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := make([]int32, n)
			nonNull := 0
			for i := range values {
				values[i] = 100 + int32(i%50)
				if tc.isNull(i) {
					values[i] = -1
				} else {
					nonNull++
				}
			}

			table := New[string]("test", n, []FieldData{
				{Name: "num", InitialType: TypeInt32, NullValues: [][]byte{be32(-1)}},
			})
			for i, v := range values {
				table.PutSingleRecord(fmt.Sprintf("id-%d", i),
					map[string][][]byte{"num": {be32(v)}})
			}
			table.Compact(nil)
			f := &table.Fields[0]

			assert(t, f.CompactingStrategy == CompactingScaled, "strategy=%v", f.CompactingStrategy)
			assert(t, len(f.ByteValues) == nonNull,
				"len(ByteValues)=%d want %d", len(f.ByteValues), nonNull)

			// запрос в обратном порядке: position != index
			req := make([]IndexGetPosition, n)
			orig := make([]IndexGetPosition, n)
			for pos := 0; pos < n; pos++ {
				src := n - 1 - pos
				req[pos] = IndexGetPosition{index: table.KeyToIndex[fmt.Sprintf("id-%d", src)], position: pos}
			}
			copy(orig, req)

			got := f.DenseIndices(req, nil)

			assert(t, len(got) == nonNull, "len(dense)=%d want %d", len(got), nonNull)

			// входной список не изменён
			for i := range req {
				assert(t, req[i] == orig[i], "input mutated at %d: %v want %v", i, req[i], orig[i])
			}

			// каждый плотный индекс декодируется в исходное значение
			for _, it := range got {
				src := n - 1 - it.position
				assert(t, values[src] != -1, "null leaked into result: id-%d", src)
				decoded := int32(f.Helpers[0]) + int32(f.ByteValues[it.index])
				assert(t, decoded == values[src],
					"id-%d: dense=%d decoded=%d want %d", src, it.index, decoded, values[src])
			}
		})
	}
}

func TestGetMany(t *testing.T) {
	const n, m = 500, 700 // m > n: в запросе есть дубликаты
	const intNull, fNull = int32(-1), -999.5

	enc32 := func(v int32) []byte { return binary.BigEndian.AppendUint32(nil, uint32(v)) }
	enc64 := func(v float64) []byte { return binary.BigEndian.AppendUint64(nil, math.Float64bits(v)) }

	nullS := func(i int) bool { return i%7 == 0 || i == 63 || i == 64 }
	nullE := func(i int) bool { return i%11 == 3 || i == 127 || i == 128 }
	nullF := func(i int) bool { return i%5 == 1 }

	scaledV := func(i int) int32 { return 1000 + int32(i%100) }
	enumV := func(i int) int32 { return []int32{10, 70000, -500000, 3000000}[i%4] }
	floatV := func(i int) float64 { return []float64{1.5, -2.25, 3.14159, 1e10}[i%4] }

	table := New[string]("test", n, []FieldData{
		{Name: "scaled", InitialType: TypeInt32, NullValues: [][]byte{enc32(intNull)}},
		{Name: "enum", InitialType: TypeInt32, NullValues: [][]byte{enc32(intNull)}},
		{Name: "float", InitialType: TypeDouble, NullValues: [][]byte{enc64(fNull)}},
	})
	keyOf := func(i int) string { return fmt.Sprintf("id-%d", i) }

	for i := 0; i < n; i++ {
		s, e, f := enc32(scaledV(i)), enc32(enumV(i)), enc64(floatV(i))
		if nullS(i) {
			s = enc32(intNull)
		}
		if nullE(i) {
			e = enc32(intNull)
		}
		if nullF(i) {
			f = enc64(fNull)
		}
		table.PutSingleRecord(keyOf(i), map[string][][]byte{"scaled": {s}, "enum": {e}, "float": {f}})
	}

	if v, ok := table.GetMany([]string{keyOf(0)}, 0); ok || v != nil {
		t.Fatal("GetMany before Compact must fail")
	}
	table.Compact(nil)

	keys := make([]string, m)
	src := make([]int, m)
	for pos := range keys {
		src[pos] = (pos*37 + 11) % n
		keys[pos] = keyOf(src[pos])
	}

	view, ok := table.GetMany(keys, 0)
	assert(t, ok && view != nil, "GetMany failed")
	assert(t, view.Count == m && len(view.Columns) == 3, "count=%d cols=%d", view.Count, len(view.Columns))

	nulls := []func(int) bool{nullS, nullE, nullF}
	for pos, s := range src {
		for c, isNull := range nulls {
			col := &view.Columns[c]
			assert(t, col.IsNull(pos) == isNull(s), "col %d pos=%d (id-%d): null=%v want %v",
				c, pos, s, col.IsNull(pos), isNull(s))
		}
		if !nullS(s) {
			got := int32(view.Columns[0].Values[pos])
			assert(t, got == scaledV(s), "scaled pos=%d (id-%d): got %d want %d", pos, s, got, scaledV(s))
		}
		if !nullE(s) {
			got := int32(view.Columns[1].Values[pos])
			assert(t, got == enumV(s), "enum pos=%d (id-%d): got %d want %d", pos, s, got, enumV(s))
		}
		if !nullF(s) {
			got := math.Float64frombits(view.Columns[2].Values[pos])
			assert(t, math.Float64bits(got) == math.Float64bits(floatV(s)),
				"float pos=%d (id-%d): got %v want %v", pos, s, got, floatV(s))
		}
	}

	// неизвестный ключ -> false
	v, ok := table.GetMany([]string{keyOf(1), "missing"}, 0)
	assert(t, !ok && v == nil, "unknown key must fail")

	// пустой запрос
	view, ok = table.GetMany(nil, 0)
	assert(t, ok && view.Count == 0, "empty request must succeed")
}

func TestGetManyIndexed(t *testing.T) {
	const na, n, m = 20, 300, 450

	enc32 := func(v int32) []byte { return binary.BigEndian.AppendUint32(nil, uint32(v)) }
	age := func(a int) int32 { return int32(20 + a) }

	authors := New[UUIDT]("authors", na, []FieldData{{Name: "age", InitialType: TypeInt32}})
	for i := 0; i < na; i++ {
		authors.PutSingleRecord(testUUID(byte(i)), map[string][][]byte{"age": {enc32(age(i))}})
	}
	authors.Compact(nil)

	unknown := testUUID(200)
	kind := func(i int) int { return []int{2, 0, 1, 2, 2}[i%5] } // 0: пусто, 1: неизвестный автор (null), 2: автор
	author := func(i int) int { return (i * 7) % na }
	keyOf := func(i int) UUIDT { var u UUIDT; binary.BigEndian.PutUint32(u[:], uint32(i+1)); return u }

	books := New[UUIDT]("books", n, []FieldData{
		{Name: "author", InitialType: TypeExternalIndex, ExternalTableName: "authors"},
	})
	for i := 0; i < n; i++ {
		data := map[string][][]byte{}
		switch kind(i) {
		case 1:
			data["author"] = [][]byte{unknown[:]}
		case 2:
			a := testUUID(byte(author(i)))
			data["author"] = [][]byte{a[:]}
		}
		books.PutSingleRecord(keyOf(i), data)
	}
	books.Compact(map[string]*CompactTable[UUIDT]{"authors": authors})

	assert(t, books.Fields[0].CompactingStrategy == CompactingIndexed,
		"strategy=%v", books.Fields[0].CompactingStrategy)
	assert(t, authors.Fields[0].CompactingStrategy == CompactingScaled,
		"authors strategy=%v", authors.Fields[0].CompactingStrategy)

	keys := make([]UUIDT, m)
	src := make([]int, m)
	for pos := range keys {
		src[pos] = (pos*37 + 11) % n // перемешано, с дубликатами
		keys[pos] = keyOf(src[pos])
	}

	// depth 0: только refs, внешняя таблица не трогается
	v0, ok := books.GetMany(keys, 0)
	assert(t, ok, "GetMany depth=0 failed")
	c0 := &v0.Columns[0]
	assert(t, c0.Sub == nil, "Sub must be nil at depth 0")

	for pos, s := range src {
		wantNull := kind(s) != 2
		assert(t, c0.IsNull(pos) == wantNull, "pos=%d (book %d): null=%v want %v",
			pos, s, c0.IsNull(pos), wantNull)
		if wantNull {
			continue
		}
		got := authors.IndexToKey[c0.Refs[pos]]
		assert(t, got == testUUID(byte(author(s))), "pos=%d (book %d): wrong author key", pos, s)
	}

	// depth 1: один запрос во внешнюю таблицу
	v1, ok := books.GetMany(keys, 1)
	assert(t, ok, "GetMany depth=1 failed")
	c1 := &v1.Columns[0]
	assert(t, c1.Sub != nil, "Sub must be loaded at depth 1")

	rows := 0
	for pos, s := range src {
		row, has := c1.SubRow(pos)
		if kind(s) != 2 {
			assert(t, !has, "pos=%d (book %d): null must have no sub row", pos, s)
			continue
		}
		assert(t, has && row == rows, "pos=%d: sub row=%d,%v want %d", pos, row, has, rows)
		rows++
		got := int32(c1.Sub.Columns[0].Values[row])
		assert(t, got == age(author(s)), "pos=%d (book %d): age=%d want %d", pos, s, got, age(author(s)))
	}
	assert(t, c1.Sub.Count == rows, "Sub.Count=%d want %d", c1.Sub.Count, rows)
}

func TestGetManyIndexedList(t *testing.T) {
	const n, m = 200, 300
	enc32 := func(v int32) []byte { return binary.BigEndian.AppendUint32(nil, uint32(v)) }
	keyOf := func(i int) UUIDT { var u UUIDT; binary.BigEndian.PutUint32(u[12:], uint32(i+1)); return u }
	val := func(i int) int32 { return int32(100 + i%50) }
	kids := func(i int) []int { // ожидаемые дети
		if i%4 == 0 || i%7 == 0 {
			return nil
		}
		return []int{(i*3 + 1) % n, (i*5 + 2) % n}
	}
	unknown := testUUID(250)

	nodes := New[UUIDT]("nodes", n, []FieldData{
		{Name: "val", InitialType: TypeInt32},
		{Name: "children", InitialType: TypeExternalIndex, ExternalTableName: "nodes", IsList: true},
	})
	for i := 0; i < n; i++ {
		var blobs [][]byte
		add := func(k UUIDT) { blobs = append(blobs, k[:]) }
		switch {
		case i%4 == 0: // пустой список
		case i%7 == 0: // только неизвестный ключ
			add(unknown)
		default:
			k := kids(i)
			add(keyOf(k[0]))
			if i%3 == 0 {
				add(unknown) // неизвестный посередине выпадает
			}
			add(keyOf(k[1]))
		}
		data := map[string][][]byte{"val": {enc32(val(i))}}
		if blobs != nil {
			data["children"] = blobs
		}
		nodes.PutSingleRecord(keyOf(i), data)
	}
	nodes.Compact(nil) // самоссылка резолвится по имени таблицы

	f := &nodes.Fields[1]

	if f.Empty || f.CompactingStrategy != CompactingIndexed {
		t.Fatalf("empty=%v strategy=%v ext=%v", f.Empty, f.CompactingStrategy, nodes.external("nodes") != nil)
	}

	keys := make([]UUIDT, m)
	src := make([]int, m)
	for pos := range keys {
		src[pos] = (pos*37 + 11) % n // перемешано, с дубликатами
		keys[pos] = keyOf(src[pos])
	}

	// depth 0: только списки
	v0, ok := nodes.GetMany(keys, 0)
	assert(t, ok, "GetMany depth=0 failed")
	c0 := &v0.Columns[1]
	assert(t, c0.Sub == nil, "Sub must be nil at depth 0")
	for pos, s := range src {
		want := kids(s)
		assert(t, c0.IsNull(pos) == (want == nil), "pos=%d (node %d): null=%v", pos, s, c0.IsNull(pos))
		assert(t, len(c0.Lists[pos]) == len(want), "pos=%d (node %d): len=%d want %d",
			pos, s, len(c0.Lists[pos]), len(want))
		for j, r := range c0.Lists[pos] {
			assert(t, nodes.IndexToKey[r] == keyOf(want[j]), "pos=%d (node %d) child %d: wrong key", pos, s, j)
		}
	}

	// depth 1: один запрос в ту же таблицу
	v1, ok := nodes.GetMany(keys, 1)
	assert(t, ok, "GetMany depth=1 failed")
	c1 := &v1.Columns[1]
	assert(t, c1.Sub != nil, "Sub must be loaded at depth 1")
	total := 0
	for pos, s := range src {
		want := kids(s)
		from, to := c1.ListRows(pos)
		assert(t, to-from == len(want), "pos=%d (node %d): rows=%d want %d", pos, s, to-from, len(want))
		for j, kid := range want {
			got := int32(c1.Sub.Columns[0].Values[from+j])
			assert(t, got == val(kid), "pos=%d (node %d) child %d: val=%d want %d", pos, s, j, got, val(kid))
		}
		total += len(want)
	}
	assert(t, c1.Sub.Count == total, "Sub.Count=%d want %d", c1.Sub.Count, total)
	assert(t, c1.Sub.Columns[1].Sub == nil, "depth 1 must not go further")

	// depth 2: внуки
	v2, _ := nodes.GetMany(keys, 2)
	assert(t, v2.Columns[1].Sub.Columns[1].Sub != nil, "grandchildren must load at depth 2")
}
