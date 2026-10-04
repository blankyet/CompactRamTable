package compact_table

import (
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"testing"
)

func TestCompact(t *testing.T) {
	values := []int{10, 20, 30, 40}
	isNull := func(i uint32) bool {
		return i == 1 || i == 3 // drop indices 1 and 3
	}

	got := compact(values, 4, isNull)
	want := []int{10, 30}

	assert(t, reflect.DeepEqual(got, want), "compact() = %v, want %v", got, want)
}

func TestMakeFieldDense(t *testing.T) {
	const count = uint32(6)

	makeNulls := func(f *Field, nulls ...uint32) {
		f.HasNullBits = true
		f.NullBits = make([]uint64, (count+63)/64)
		for _, i := range nulls {
			f.SetNull(i)
		}
	}

	t.Run("TypeByte", func(t *testing.T) {
		f := &Field{
			FinalType:  TypeByte,
			ByteValues: []uint8{10, 20, 30, 40, 50, 60},
		}
		makeNulls(f, 1, 4)

		MakeFieldDense(f, count)

		assert(t, len(f.ByteValues) == 4, "expected 4 values, got %d", len(f.ByteValues))
		assert(t, f.ByteValues[0] == 10, "got %d", f.ByteValues[0])
		assert(t, f.ByteValues[1] == 30, "got %d", f.ByteValues[1])
		assert(t, f.ByteValues[2] == 40, "got %d", f.ByteValues[2])
		assert(t, f.ByteValues[3] == 60, "got %d", f.ByteValues[3])
	})

	t.Run("Type2Byte", func(t *testing.T) {
		f := &Field{
			FinalType:   Type2Byte,
			Byte2Values: []uint16{1000, 2000, 3000, 4000, 5000, 6000},
		}
		makeNulls(f, 0, 3)

		MakeFieldDense(f, count)

		assert(t, len(f.Byte2Values) == 4, "expected 4 values, got %d", len(f.Byte2Values))
		assert(t, f.Byte2Values[0] == 2000, "got %d", f.Byte2Values[0])
		assert(t, f.Byte2Values[1] == 3000, "got %d", f.Byte2Values[1])
		assert(t, f.Byte2Values[2] == 5000, "got %d", f.Byte2Values[2])
		assert(t, f.Byte2Values[3] == 6000, "got %d", f.Byte2Values[3])
	})

	t.Run("Type4Byte", func(t *testing.T) {
		f := &Field{
			FinalType:   Type4Byte,
			Byte4Values: []uint32{100000, 200000, 300000, 400000, 500000, 600000},
		}
		makeNulls(f, 2, 5)

		MakeFieldDense(f, count)

		assert(t, len(f.Byte4Values) == 4, "expected 4 values, got %d", len(f.Byte4Values))
		assert(t, f.Byte4Values[0] == 100000, "got %d", f.Byte4Values[0])
		assert(t, f.Byte4Values[1] == 200000, "got %d", f.Byte4Values[1])
		assert(t, f.Byte4Values[2] == 400000, "got %d", f.Byte4Values[2])
		assert(t, f.Byte4Values[3] == 500000, "got %d", f.Byte4Values[3])
	})

	t.Run("Type8Byte", func(t *testing.T) {
		f := &Field{
			FinalType: Type8Byte,
			Byte8Values: []uint64{
				1_000_000_000_000,
				2_000_000_000_000,
				3_000_000_000_000,
				4_000_000_000_000,
				5_000_000_000_000,
				6_000_000_000_000,
			},
		}
		makeNulls(f, 1, 2, 5)

		MakeFieldDense(f, count)

		assert(t, len(f.Byte8Values) == 3, "expected 3 values, got %d", len(f.Byte8Values))
		assert(t, f.Byte8Values[0] == 1_000_000_000_000, "got %d", f.Byte8Values[0])
		assert(t, f.Byte8Values[1] == 4_000_000_000_000, "got %d", f.Byte8Values[1])
		assert(t, f.Byte8Values[2] == 5_000_000_000_000, "got %d", f.Byte8Values[2])
	})
}

func TestAnalyzeField(t *testing.T) {
	buf1 := make([]byte, 4)
	buf2 := make([]byte, 4)
	binary.BigEndian.PutUint32(buf1, 10)
	binary.BigEndian.PutUint32(buf2, 20)

	f := &Field{
		Data: FieldData{Name: "TestField"},
		OriginalData: [][][]byte{
			{buf1},
			{buf2},
		},
	}

	meta := analyzeField[int32](f, 2)

	assert(t, meta.Min == 10, "Min = %d, want 10", meta.Min)
	assert(t, meta.Max == 20, "Max = %d, want 20", meta.Max)
	assert(t, len(meta.Unique) == 2, "Unique count = %d, want 2", len(meta.Unique))
}

func GetDecodedFloat(field *Field, i uint32) float64 {
	if field.CompactingStrategy != CompactingEnumed {
		return 0
	}

	var enumIdx uint64
	switch field.FinalType {
	case TypeByte:
		enumIdx = uint64(field.ByteValues[i])
	case Type2Byte:
		enumIdx = uint64(field.Byte2Values[i])
	default:
		panic(fmt.Sprintf("GetDecodedFloat: unsupported final type %v", field.FinalType))
	}
	return math.Float64frombits(field.Helpers[enumIdx])
}

func TestTryCompactFloatLarge(t *testing.T) {
	count := uint32(10000)

	t.Run("FitUint8", func(t *testing.T) {
		f := &Field{Data: FieldData{Name: "LargeFloat8"}, OriginalData: make([][][]byte, count)}
		for i := uint32(0); i < count; i++ {
			buf := make([]byte, 8)
			binary.BigEndian.PutUint64(buf, math.Float64bits(float64(i%100)))
			f.OriginalData[i] = [][]byte{buf}
		}
		TryCompactFloat[float64](f, count)
		assert(t, f.FinalType == TypeByte, "Expected TypeByte, got %v", f.FinalType)
		assert(t, GetDecodedFloat(f, 500) == 0.0, "Decoded mismatch at index 500")
		assert(t, GetDecodedFloat(f, 99) == 99.0, "Decoded mismatch at index 99")
	})

	t.Run("FitUint16", func(t *testing.T) {
		f := &Field{Data: FieldData{Name: "LargeFloat16"}, OriginalData: make([][][]byte, count)}
		for i := uint32(0); i < count; i++ {
			buf := make([]byte, 8)
			binary.BigEndian.PutUint64(buf, math.Float64bits(float64(i%500)))
			f.OriginalData[i] = [][]byte{buf}
		}
		TryCompactFloat[float64](f, count)
		assert(t, f.FinalType == Type2Byte, "Expected Type2Byte, got %v", f.FinalType)
		assert(t, GetDecodedFloat(f, 450) == 450.0, "Decoded mismatch at index 450")
	})
}

func GetDecodedInt(field *Field, i uint32) uint64 {
	switch field.CompactingStrategy {
	case CompactingScaled:
		base := field.Helpers[0]
		switch field.FinalType {
		case TypeByte:
			return base + uint64(field.ByteValues[i])
		case Type2Byte:
			return base + uint64(field.Byte2Values[i])
		case Type4Byte:
			return base + uint64(field.Byte4Values[i])
		}
	case CompactingEnumed:
		var enumIdx uint64
		switch field.FinalType {
		case TypeByte:
			enumIdx = uint64(field.ByteValues[i])
		case Type2Byte:
			enumIdx = uint64(field.Byte2Values[i])
		}
		return field.Helpers[enumIdx]
	}
	return 0
}

func TestTryCompactIntegerPaths(t *testing.T) {
	count := uint32(100)

	// Helper to create int fields
	createFieldField := func(name string, values []int64) *Field {
		f := &Field{Data: FieldData{Name: name}, OriginalData: make([][][]byte, len(values))}
		for i, v := range values {
			buf := make([]byte, 8)
			binary.BigEndian.PutUint64(buf, uint64(v))
			f.OriginalData[i] = [][]byte{buf}
		}
		return f
	}

	t.Run("ScaleUint8", func(t *testing.T) {
		vals := make([]int64, count)
		for i := range vals {
			vals[i] = int64(1000 + (i % 50)) // delta = 49 <= MaxUint8
		}
		f := createFieldField("Scale8", vals)
		TryCompactInteger[int64](f, count)

		assert(t, f.FinalType == TypeByte, "Expected TypeByte, got %v", f.FinalType)
		assert(t, f.CompactingStrategy == CompactingScaled, "Expected CompactingScaled")
		assert(t, GetDecodedInt(f, 10) == 1010, "Decoded mismatch, got %d", GetDecodedInt(f, 10))
	})

	t.Run("ScaleUint16", func(t *testing.T) {
		vals := make([]int64, count)
		for i := range vals {
			vals[i] = int64(50000 + (i * 10)) // delta = 990 <= MaxUint16
		}
		f := createFieldField("Scale16", vals)
		TryCompactInteger[int64](f, count)

		assert(t, f.FinalType == Type2Byte, "Expected Type2Byte, got %v", f.FinalType)
		assert(t, f.CompactingStrategy == CompactingScaled, "Expected CompactingScaled")
		assert(t, GetDecodedInt(f, 5) == 50050, "Decoded mismatch, got %d", GetDecodedInt(f, 5))
	})

	t.Run("EnumUint8", func(t *testing.T) {
		// Small unique set count (<= 256), but large delta (large range, sparse)
		vals := make([]int64, count)
		for i := range vals {
			if i%2 == 0 {
				vals[i] = 100
			} else {
				vals[i] = 999999
			}
		}
		f := createFieldField("Enum8", vals)
		TryCompactInteger[int64](f, count)

		assert(t, f.FinalType == TypeByte, "Expected TypeByte, got %v", f.FinalType)
		assert(t, f.CompactingStrategy == CompactingEnumed, "Expected CompactingEnumed")
		assert(t, GetDecodedInt(f, 0) == 100, "Decoded mismatch at 0")
		assert(t, GetDecodedInt(f, 1) == 999999, "Decoded mismatch at 1")
	})

	count = 1_000_000
	t.Run("ScaleUint32", func(t *testing.T) {
		vals := make([]int64, count)
		for i := range vals {
			vals[i] = int64(2000000000 + (i * 1000)) // delta = 99,000 (> MaxUint16, fits in uint32)
		}
		f := createFieldField("Scale32", vals)
		TryCompactInteger[int64](f, count)

		assert(t, f.FinalType == Type4Byte, "Expected Type4Byte, got %v", f.FinalType)
		assert(t, f.CompactingStrategy == CompactingScaled, "Expected CompactingScaled")
		assert(t, GetDecodedInt(f, 42) == 2000042000, "Decoded mismatch, got %d", GetDecodedInt(f, 42))
	})
}

func TestUnpackEnum(t *testing.T) {
	const n = 300

	cases := []struct {
		name      string
		finalType FinalTypeEnum
		uniques   int // 300 > 255: проверяем, что 2-байтные коды не обрезаются
		withNulls bool
	}{
		{"byte/nulls", TypeByte, 5, true},
		{"byte/no nulls", TypeByte, 5, false},
		{"2byte/nulls", Type2Byte, 290, true},
		{"4byte/no nulls", Type4Byte, 290, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unique := make([]uint64, tc.uniques)
			for k := range unique {
				unique[k] = 1000 + uint64(k)*10
			}

			f := &Field{
				FinalType:          tc.finalType,
				CompactingStrategy: CompactingEnumed,
				Helpers:            unique,
				OriginalData:       make([][][]byte, n),
			}

			// want[i] = ожидаемое значение элемента i, nullMark = null
			const nullMark = ^uint64(0)
			want := make([]uint64, n)
			var codes []uint32 // плотный массив кодов (без null-ов)

			for i := 0; i < n; i++ {
				isNull := tc.withNulls &&
					(i%3 == 0 || i == 63 || i == 64 || i == 127 || i == 128)
				if isNull {
					want[i] = nullMark
					f.SetNull(uint32(i))
					continue
				}
				code := uint32((i * 7) % tc.uniques)
				want[i] = unique[code]
				codes = append(codes, code)
			}
			f.BuildNullPrefix()

			switch tc.finalType {
			case TypeByte:
				f.ByteValues = make([]uint8, len(codes))
				for i, c := range codes {
					f.ByteValues[i] = uint8(c)
				}
			case Type2Byte:
				f.Byte2Values = make([]uint16, len(codes))
				for i, c := range codes {
					f.Byte2Values[i] = uint16(c)
				}
			case Type4Byte:
				f.Byte4Values = codes
			}

			// запрос в обратном порядке: position != index
			req := make([]IndexGetPosition, n)
			for pos := 0; pos < n; pos++ {
				req[pos] = IndexGetPosition{index: uint32(n - 1 - pos), position: pos}
			}

			out := make([]uint64, n)
			for i := range out {
				out[i] = nullMark // null-позиции должны остаться нетронутыми
			}

			valid := f.DenseIndices(req, nil)
			f.UnpackEnum(valid, out)

			for pos := 0; pos < n; pos++ {
				src := n - 1 - pos
				assert(t, out[pos] == want[src],
					"pos=%d (id-%d): got %d want %d", pos, src, out[pos], want[src])
			}
		})
	}
}

func TestUnpackScaled(t *testing.T) {
	const n = 300

	cases := []struct {
		name      string
		base      int32
		val       func(i int) int32 // смещение от base
		wantType  FinalTypeEnum
		withNulls bool
	}{
		{"byte/positive/nulls", 100000, func(i int) int32 { return int32(i % 200) }, TypeByte, true},
		{"byte/negative/no nulls", -1000, func(i int) int32 { return int32(i % 200) }, TypeByte, false},
		{"2byte/negative/nulls", -50000, func(i int) int32 { return int32(i) * 200 }, Type2Byte, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := make([]int32, n)
			nonNull := 0
			for i := range values {
				values[i] = tc.base + tc.val(i)
				if tc.withNulls && (i%3 == 0 || i == 63 || i == 64 || i == 127 || i == 128) {
					values[i] = -1 // sentinel; base подобраны так, что -1 не встречается в данных
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
			assert(t, f.FinalType == tc.wantType, "finalType=%v want %v", f.FinalType, tc.wantType)

			// обратный порядок запроса: position != index
			req := make([]IndexGetPosition, n)
			for pos := 0; pos < n; pos++ {
				src := n - 1 - pos
				req[pos] = IndexGetPosition{index: table.KeyToIndex[fmt.Sprintf("id-%d", src)], position: pos}
			}

			const untouched = 0xDEADBEEFDEADBEEF
			out := make([]uint64, n)
			for i := range out {
				out[i] = untouched
			}

			valid := f.DenseIndices(req, nil)
			assert(t, len(valid) == nonNull, "len(valid)=%d want %d", len(valid), nonNull)
			f.UnpackScaled(valid, out)

			for pos := 0; pos < n; pos++ {
				src := n - 1 - pos
				if values[src] == -1 {
					assert(t, out[pos] == untouched, "pos=%d: null overwritten", pos)
					continue
				}
				assert(t, int32(out[pos]) == values[src],
					"pos=%d (id-%d): got %d want %d", pos, src, int32(out[pos]), values[src])
			}
		})
	}
}
func TestUnpackEnumFloat(t *testing.T) {
	enc64 := func(v float64) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, math.Float64bits(v))
		return b
	}
	enc32 := func(v float64) []byte {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, math.Float32bits(float32(v)))
		return b
	}

	const nullSentinel = -999.5     // точно представим и в float32, в данных не встречается
	isNullIdx := func(i int) bool { // границы слов: 63/64, 127/128
		return i%3 == 0 || i == 63 || i == 64 || i == 127 || i == 128
	}
	pool := []float64{1.5, -2.25, 0, math.Inf(1), math.Inf(-1), 3.14159, -1e10, 1e-7}

	cases := []struct {
		name      string
		n         int
		typ       InitialTypeEnum
		is32      bool
		val       func(i int) float64
		wantType  FinalTypeEnum
		withNulls bool
	}{
		{"f64/byte/nulls", 300, TypeDouble, false,
			func(i int) float64 { return pool[i%len(pool)] }, TypeByte, true},
		{"f64/byte/no nulls", 300, TypeDouble, false,
			func(i int) float64 { return pool[i%len(pool)] }, TypeByte, false},
		{"f64/2byte/nulls", 1000, TypeDouble, false,
			func(i int) float64 { return float64(i)*0.37 - 50 }, Type2Byte, true},
		{"f32/byte/nulls", 300, TypeFloat, true,
			func(i int) float64 { return pool[i%len(pool)] }, TypeByte, true},
		{"f32/2byte/no nulls", 1000, TypeFloat, true,
			func(i int) float64 { return float64(i)*0.5 - 100 }, Type2Byte, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := tc.n
			enc := enc64
			if tc.is32 {
				enc = enc32
			}

			values := make([]float64, n)
			isNull := make([]bool, n)
			nonNull := 0
			for i := range values {
				if tc.withNulls && isNullIdx(i) {
					values[i] = nullSentinel
					isNull[i] = true
					continue
				}
				values[i] = tc.val(i)
				nonNull++
			}

			table := New[string]("test", uint32(n), []FieldData{
				{Name: "num", InitialType: tc.typ, NullValues: [][]byte{enc(nullSentinel)}},
			})
			for i, v := range values {
				table.PutSingleRecord(fmt.Sprintf("id-%d", i),
					map[string][][]byte{"num": {enc(v)}})
			}
			table.Compact(nil)
			f := &table.Fields[0]

			assert(t, !f.Empty, "field should not be empty")
			assert(t, f.CompactingStrategy == CompactingEnumed, "strategy=%v", f.CompactingStrategy)
			assert(t, f.FinalType == tc.wantType, "finalType=%v want %v", f.FinalType, tc.wantType)

			// обратный порядок запроса: position != index
			req := make([]IndexGetPosition, n)
			for pos := 0; pos < n; pos++ {
				src := n - 1 - pos
				req[pos] = IndexGetPosition{index: table.KeyToIndex[fmt.Sprintf("id-%d", src)], position: pos}
			}

			const untouched = 0xDEADBEEFDEADBEEF
			out := make([]uint64, n)
			for i := range out {
				out[i] = untouched
			}

			valid := f.DenseIndices(req, nil)
			assert(t, len(valid) == nonNull, "len(valid)=%d want %d", len(valid), nonNull)
			f.UnpackEnum(valid, out)

			for pos := 0; pos < n; pos++ {
				src := n - 1 - pos
				if isNull[src] {
					assert(t, out[pos] == untouched, "pos=%d: null overwritten", pos)
					continue
				}
				if tc.is32 {
					got := math.Float32frombits(uint32(out[pos]))
					want := float32(values[src])
					assert(t, math.Float32bits(got) == math.Float32bits(want),
						"pos=%d (id-%d): got %v want %v", pos, src, got, want)
				} else {
					got := math.Float64frombits(out[pos])
					assert(t, math.Float64bits(got) == math.Float64bits(values[src]),
						"pos=%d (id-%d): got %v want %v", pos, src, got, values[src])
				}
			}
		})
	}
}

func TestGetUnpackedValues(t *testing.T) {
	const n = 200
	const intNull = int32(-1)

	enc32 := func(v int32) []byte {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(v))
		return b
	}
	enc64f := func(v float64) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, math.Float64bits(v))
		return b
	}

	isNull := func(i int) bool { return i%5 == 0 || i == 63 || i == 64 }

	// scaled: маленький разброс; enum: редкие значения с большой дельтой; float: enum
	scaledVal := func(i int) int32 { return 1000 + int32(i%100) }
	enumVal := func(i int) int32 { return []int32{10, 70000, -500000, 3000000}[i%4] }
	floatVal := func(i int) float64 { return []float64{1.5, -2.25, 3.14159, 1e10}[i%4] }

	table := New[string]("test", n, []FieldData{
		{Name: "scaled", InitialType: TypeInt32, NullValues: [][]byte{enc32(intNull)}},
		{Name: "enum", InitialType: TypeInt32, NullValues: [][]byte{enc32(intNull)}},
		{Name: "float", InitialType: TypeDouble, NullValues: [][]byte{enc64f(-999.5)}},
	})

	for i := 0; i < n; i++ {
		s, e, f := enc32(scaledVal(i)), enc32(enumVal(i)), enc64f(floatVal(i))
		if isNull(i) {
			s, e, f = enc32(intNull), enc32(intNull), enc64f(-999.5)
		}
		table.PutSingleRecord(fmt.Sprintf("id-%d", i),
			map[string][][]byte{"scaled": {s}, "enum": {e}, "float": {f}})
	}
	table.Compact(nil)

	assert(t, table.Fields[0].CompactingStrategy == CompactingScaled,
		"scaled: strategy=%v", table.Fields[0].CompactingStrategy)
	assert(t, table.Fields[1].CompactingStrategy == CompactingEnumed,
		"enum: strategy=%v", table.Fields[1].CompactingStrategy)
	assert(t, table.Fields[2].CompactingStrategy == CompactingEnumed,
		"float: strategy=%v", table.Fields[2].CompactingStrategy)

	// обратный порядок: position != index
	req := make([]IndexGetPosition, n)
	for pos := 0; pos < n; pos++ {
		src := n - 1 - pos
		req[pos] = IndexGetPosition{index: table.KeyToIndex[fmt.Sprintf("id-%d", src)], position: pos}
	}

	const untouched = 0xDEADBEEFDEADBEEF
	scratch := make([]IndexGetPosition, 0, n)

	for fi := range table.Fields {
		f := &table.Fields[fi]

		out := make([]uint64, n)
		for i := range out {
			out[i] = untouched
		}

		valid := f.DenseIndices(req, scratch)
		f.GetUnpackedValues(valid, out)

		for pos := 0; pos < n; pos++ {
			src := n - 1 - pos
			if isNull(src) {
				assert(t, out[pos] == untouched, "%s pos=%d: null overwritten", f.Data.Name, pos)
				continue
			}
			switch f.Data.Name {
			case "scaled":
				assert(t, int32(out[pos]) == scaledVal(src),
					"scaled pos=%d (id-%d): got %d want %d", pos, src, int32(out[pos]), scaledVal(src))
			case "enum":
				assert(t, int32(out[pos]) == enumVal(src),
					"enum pos=%d (id-%d): got %d want %d", pos, src, int32(out[pos]), enumVal(src))
			case "float":
				got := math.Float64frombits(out[pos])
				assert(t, math.Float64bits(got) == math.Float64bits(floatVal(src)),
					"float pos=%d (id-%d): got %v want %v", pos, src, got, floatVal(src))
			}
		}
	}

	// пустой список ничего не пишет и не паникует
	out := []uint64{untouched}
	table.Fields[0].GetUnpackedValues(nil, out)
	assert(t, out[0] == untouched, "empty valid must not write")
}

func TestGetManyTokenized(t *testing.T) {
	const n, m = 300, 450
	words := []string{"the", "quick", "brown", "fox", "lazy", "dog"}

	isNull := func(i int) bool { return i%4 == 1 || i == 63 || i == 64 || i == 127 }
	val := func(i int) string {
		return words[i%6] + " " + words[(i/2)%6] + " " + words[(i/3)%6]
	}

	table := New[string]("test", n, []FieldData{
		{Name: "title", InitialType: TypeString, Tokenizer: TokenizerStandardText,
			NullValues: [][]byte{[]byte("N/A")}},
	})
	keyOf := func(i int) string { return fmt.Sprintf("id-%d", i) }
	for i := 0; i < n; i++ {
		v := val(i)
		if isNull(i) {
			v = "N/A"
		}
		table.PutSingleRecord(keyOf(i), map[string][][]byte{"title": {[]byte(v)}})
	}
	table.Compact(nil)

	f := &table.Fields[0]
	assert(t, f.CompactingStrategy == CompactingTokenized, "strategy=%v", f.CompactingStrategy)

	keys := make([]string, m)
	src := make([]int, m)
	for pos := range keys {
		src[pos] = (pos*37 + 11) % n
		keys[pos] = keyOf(src[pos])
	}

	view, ok := table.GetMany(keys, 0)
	assert(t, ok, "GetMany failed")
	col := &view.Columns[0]

	for pos, s := range src {
		assert(t, col.IsNull(pos) == isNull(s), "pos=%d (id-%d): null=%v want %v",
			pos, s, col.IsNull(pos), isNull(s))
		if isNull(s) {
			assert(t, col.Strings[pos] == "", "pos=%d: null must stay empty", pos)
			continue
		}
		assert(t, col.Strings[pos] == val(s), "pos=%d (id-%d): got %q want %q",
			pos, s, col.Strings[pos], val(s))
	}
}
