package compact_table

import (
	"encoding/binary"
	"fmt"
	"math"
)

type Number interface {
	~int8 | ~int16 | ~int32 | ~int64 |
		~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64
}

type Integer interface {
	~int8 | ~int16 | ~int32 | ~int64 |
		~uint8 | ~uint16 | ~uint32 | ~uint64
}

type FieldTypeMetaData[T Number] struct {
	Unique   map[T]uint32
	Min, Max T
	Base     uint32
}

func analyzeField[T Number](f *Field, count uint32) FieldTypeMetaData[T] {
	var meta FieldTypeMetaData[T]

	size := binary.Size(meta.Min) // 1, 2, 4 or 8 depending on T
	unique := make(map[T]uint32)
	seen := false
	base := uint32(1)

	f.HasNullBits = false
	f.NullBits = nil

	for i := uint32(0); i < count; i++ {
		raw := f.OriginalData[i]

		if raw == nil {
			f.SetNull(uint32(i))
			continue
		}

		if len(raw) == 0 || len(raw[0]) < size {
			panic(fmt.Sprintf("inindexes field %s at index %d: need %d bytes", f.Data.Name, i, size))
		}

		var v T
		if _, err := binary.Decode(raw[0], binary.BigEndian, &v); err != nil {
			panic(fmt.Sprintf("inindexes field %s at index %d: %v", f.Data.Name, i, err))
		}

		for base < 1_000_000_000 {
			scaled := float64(v) * float64(base)
			if math.Abs(scaled-math.Round(scaled)) < 1e-9 {
				break
			}
			base *= 10
		}

		if _, ok := unique[v]; !ok {
			unique[v] = uint32(len(unique))
		}

		if !seen {
			meta.Min, meta.Max, seen = v, v, true
		} else if v < meta.Min {
			meta.Min = v
		} else if v > meta.Max {
			meta.Max = v
		}
	}

	meta.Unique = unique
	meta.Base = base
	return meta
}

func compact[T any](values []T, count uint32, isNull func(uint32) bool) []T {
	write := 0

	for read := uint32(0); read < count; read++ {
		if isNull(uint32(read)) {
			continue
		}
		values[write] = values[read]
		write++
	}

	return values[:write:write] // values[low : high : max] save memory
}

func MakeTokenizedFieldDense(field *Field, count uint32) {
	if field.Empty || field.CompactingStrategy != CompactingTokenized {
		return
	}

	old := field.ListOffsets
	blob := field.ByteValues

	offsets := make([]uint32, 1, count+1)
	writePos := uint32(0)

	for row := uint32(0); row < count; row++ {
		if field.IsNull(row) {
			continue
		}

		start := old[row]
		end := old[row+1]

		blob = append(blob[:writePos], blob[start:end]...)
		writePos += end - start
		offsets = append(offsets, writePos)
	}

	field.ByteValues = blob[:writePos:writePos]
	field.ListOffsets = offsets[:len(offsets):len(offsets)]
}

func MakeFieldDense(field *Field, count uint32) {

	if field.Empty {
		return
	}

	switch field.CompactingStrategy {
	case CompactingTokenized:
		MakeTokenizedFieldDense(field, count)
		return
	case CompactingIndexed:
		MakeExternalDense(field, count)
		return
	}

	switch field.FinalType {
	case TypeByte:
		field.ByteValues = compact(field.ByteValues, count, field.IsNull)
	case Type2Byte:
		field.Byte2Values = compact(field.Byte2Values, count, field.IsNull)
	case Type4Byte:
		field.Byte4Values = compact(field.Byte4Values, count, field.IsNull)
	case Type8Byte:
		field.Byte8Values = compact(field.Byte8Values, count, field.IsNull)
	}
}

func TryCompactStrings(field *Field, count uint32, tokenizer Tokenizer) {
	if tokenizer == nil || field.IsList {
		return
	}

	blob := make([]byte, 0, count*4)
	offsets := make([]uint32, 1, count+1)

	for i := uint32(0); i < count; i++ {
		raw := field.OriginalData[i]

		if len(raw) == 0 || len(raw[0]) == 0 {
			field.SetNull(i)
			offsets = append(offsets, uint32(len(blob)))
			continue
		}

		blob = append(blob, tokenizer.Tokenize(string(raw[0]))...)
		offsets = append(offsets, uint32(len(blob)))
	}

	if len(blob) == 0 {
		field.Empty = true
		return
	}

	field.FinalType = TypeByte
	field.CompactingStrategy = CompactingTokenized
	field.ByteValues = blob[:len(blob):len(blob)]
	field.ListOffsets = offsets
	field.Helpers = []uint64{uint64(tokenizer.GetByteCountPerToken())}
	field.BuildNullPrefix()
}

func TryCompactField(field *Field, count uint32) {
	switch field.Data.InitialType {
	case TypeInt32:
		TryCompactInteger[int32](field, count)
	case TypeInt64:
		TryCompactInteger[int64](field, count)
	case TypeUInt32:
		TryCompactInteger[uint32](field, count)
	case TypeUInt64:
		TryCompactInteger[uint64](field, count)
	case TypeFloat:
		TryCompactFloat[float32](field, count)
	case TypeDouble:
		TryCompactFloat[float64](field, count)

	case TypeString, TypeBool, TypeChar, TypeExternalIndex:
		return
	}
}

// Raw fallback: the float's bits stored as-is, Scaled with min=0.
// NaN, -0 and ±Inf survive exactly.
func rawFloat[T float32 | float64](field *Field, count uint32) {
	var zero T
	field.CompactingStrategy = CompactingScaled
	field.Helpers = []uint64{0}

	if binary.Size(zero) == 4 {
		field.FinalType = Type4Byte
		field.Byte4Values = make([]uint32, count)
		for i := uint32(0); i < count; i++ {
			if !field.IsNull(i) {
				field.Byte4Values[i] = binary.BigEndian.Uint32(field.OriginalData[i][0])
			}
		}
		return
	}
	field.FinalType = Type8Byte
	field.Byte8Values = make([]uint64, count)
	for i := uint32(0); i < count; i++ {
		if !field.IsNull(i) {
			field.Byte8Values[i] = binary.BigEndian.Uint64(field.OriginalData[i][0])
		}
	}
}

func TryCompactFloat[T float32 | float64](field *Field, count uint32) {
	meta := analyzeField[T](field, count)

	if len(meta.Unique) == 0 {
		field.Empty = true
		return
	}

	if len(meta.Unique) == 1 && !field.HasNullBits {
		field.Empty = true
		return
	}

	switch {
	case len(meta.Unique) <= math.MaxUint8:
		field.ByteValues = make([]uint8, count)
		Enum(field, count, TypeByte, meta.Unique)
	case len(meta.Unique) <= math.MaxUint16:
		field.Byte2Values = make([]uint16, count)
		Enum(field, count, Type2Byte, meta.Unique)
	default:
		rawFloat[T](field, count)

	}
}

func TryCompactInteger[T Integer](field *Field, count uint32) {
	meta := analyzeField[T](field, count)

	if len(meta.Unique) == 0 {
		field.Empty = true
		return
	}

	if len(meta.Unique) == 1 && !field.HasNullBits {
		field.Empty = true
		return
	}

	if len(field.Data.EnumNames) > 0 {
		switch {
		case len(meta.Unique) <= math.MaxUint8:
			field.ByteValues = make([]uint8, count)
			Enum(field, count, TypeByte, meta.Unique)
		case len(meta.Unique) <= math.MaxUint16:
			field.Byte2Values = make([]uint16, count)
			Enum(field, count, Type2Byte, meta.Unique)
		default:
			panic(fmt.Sprintf(
				"field %q has %d enum values, too many for enum compaction",
				field.Data.Name,
				len(meta.Unique),
			))
		}
		return
	}

	delta := uint64(meta.Max) - uint64(meta.Min)

	switch {
	case delta <= math.MaxUint8:
		field.ByteValues = make([]uint8, count)
		Scale(field, count, TypeByte, meta.Min)
	case delta <= math.MaxUint16:
		field.Byte2Values = make([]uint16, count)
		Scale(field, count, Type2Byte, meta.Min)
	case len(meta.Unique) <= math.MaxUint8:
		field.ByteValues = make([]uint8, count)
		Enum(field, count, TypeByte, meta.Unique)
	case len(meta.Unique) <= math.MaxUint16:
		field.Byte2Values = make([]uint16, count)
		Enum(field, count, Type2Byte, meta.Unique)
	case delta <= math.MaxUint32 && binary.Size(meta.Min) > 4:
		field.Byte4Values = make([]uint32, count)
		Scale(field, count, Type4Byte, meta.Min)
	default:
		field.Byte8Values = make([]uint64, count)
		Scale(field, count, Type8Byte, meta.Min)
	}
}

func Scale[T Integer](field *Field, count uint32, finalType FinalTypeEnum, min T) {
	field.FinalType = finalType
	field.CompactingStrategy = CompactingScaled
	field.Helpers = []uint64{uint64(min)}

	for i := uint32(0); i < count; i++ {
		if field.IsNull(i) {
			continue
		}

		var v T
		binary.Decode(field.OriginalData[i][0], binary.BigEndian, &v)
		d := uint64(v) - uint64(min)

		switch finalType {
		case TypeByte:
			field.ByteValues[i] = uint8(d)
		case Type2Byte:
			field.Byte2Values[i] = uint16(d)
		case Type4Byte:
			field.Byte4Values[i] = uint32(d)
		case Type8Byte:
			field.Byte8Values[i] = d
		}
	}
}

func toBits[T Number](v T) uint64 {
	switch x := any(v).(type) {
	case float32:
		return uint64(math.Float32bits(x))
	case float64:
		return math.Float64bits(x)
	}
	return uint64(v) // целые, как раньше
}

func Enum[T Number](field *Field, count uint32, finalType FinalTypeEnum, unique map[T]uint32) {
	field.FinalType = finalType
	field.CompactingStrategy = CompactingEnumed
	field.Helpers = make([]uint64, len(unique))

	for value, enum := range unique {
		field.Helpers[enum] = toBits(value)
	}

	for i := uint32(0); i < count; i++ {
		if field.IsNull(i) {
			continue
		}

		var v T
		binary.Decode(field.OriginalData[i][0], binary.BigEndian, &v)

		switch finalType {
		case TypeByte:
			field.ByteValues[i] = uint8(unique[v])
		case Type2Byte:
			field.Byte2Values[i] = uint16(unique[v])
		}
	}
}

type KeyFromBytes[K any] interface {
	FromBytes([]byte) (K, bool)
}

type KeyToBytes[K any] interface {
	ToBytes() ([]byte, bool)
}

func TryCompactExternal[K comparable](
	field *Field,
	count uint32,
	ext *CompactTable[K],
) {
	if ext == nil {
		return
	}

	var zero K

	decoder, ok := any(zero).(KeyFromBytes[K])
	if !ok {
		panic(fmt.Sprintf(
			"key type %T must implement FromBytes([]byte) (K, bool)",
			zero,
		))
	}

	if _, ok := any(zero).(KeyToBytes[K]); !ok {
		panic(fmt.Sprintf(
			"key type %T must implement ToBytes() ([]byte, bool)",
			zero,
		))
	}

	if field.Data.IsList {
		values := make([]uint32, 0)
		offsets := make([]uint32, 1, count+1)

		for i := uint32(0); i < count; i++ {
			raw := field.OriginalData[i]
			start := len(values)

			for _, b := range raw {
				key, ok := decoder.FromBytes(b)
				if !ok {
					panic(fmt.Sprintf(
						"field %q, element %d: cannot decode external key from %d bytes",
						field.Data.Name, i, len(b),
					))
				}

				if idx, found := ext.KeyToIndex[key]; found {
					values = append(values, idx)
				}
			}

			if len(values) == start {
				field.SetNull(i)
			}

			offsets = append(offsets, uint32(len(values)))
		}

		if len(values) == 0 {
			field.Empty = true
			return
		}

		field.Byte4Values = values
		field.ListOffsets = offsets
	} else {
		values := make([]uint32, count)

		for i := uint32(0); i < count; i++ {
			raw := field.OriginalData[i]
			if len(raw) == 0 {
				continue
			}

			key, ok := decoder.FromBytes(raw[0])
			if !ok {
				panic(fmt.Sprintf(
					"field %q, element %d: cannot decode external key from %d bytes",
					field.Data.Name, i, len(raw[0]),
				))
			}

			if idx, found := ext.KeyToIndex[key]; found {
				values[i] = idx
			} else {
				field.SetNull(i)
			}
		}

		field.Byte4Values = values
	}

	clear(field.OriginalData[:count])

	field.FinalType = Type4Byte
	field.CompactingStrategy = CompactingIndexed
}

func MakeExternalDense(field *Field, count uint32) {
	if field.Empty {
		return
	}

	if !field.Data.IsList {
		field.Byte4Values = compact(
			field.Byte4Values,
			count,
			field.IsNull,
		)
		return
	}

	values := make([]uint32, 0, len(field.Byte4Values))
	offsets := make([]uint32, 1, count+1)

	for row := uint32(0); row < count; row++ {
		if field.IsNull(row) {
			continue
		}

		start := field.ListOffsets[row]
		end := field.ListOffsets[row+1]

		values = append(values, field.Byte4Values[start:end]...)
		offsets = append(offsets, uint32(len(values)))
	}

	field.Byte4Values = values
	field.ListOffsets = offsets
}

func (f *Field) GetUnpackedValues(indexes []IndexGetPosition, out []uint64) {
	if len(indexes) == 0 {
		return
	}
	switch f.CompactingStrategy {
	case CompactingEnumed:
		f.UnpackEnum(indexes, out)
	case CompactingScaled:
		f.UnpackScaled(indexes, out)

	default:
		panic(fmt.Sprintf("GetUnpackedValues: unsupported strategy %v for field %q",
			f.CompactingStrategy, f.Data.Name))
	}
}

func (f *Field) UnpackEnum(indexes []IndexGetPosition, out []uint64) {
	switch f.FinalType {
	case TypeByte:
		unpackEnum(f.ByteValues, f.Helpers, indexes, out)
	case Type2Byte:
		unpackEnum(f.Byte2Values, f.Helpers, indexes, out)
	case Type4Byte:
		unpackEnum(f.Byte4Values, f.Helpers, indexes, out)
	default:
		panic(fmt.Sprintf("UnpackEnum: unsupported final type %v", f.FinalType))
	}
}

func unpackEnum[T Integer](
	codes []T,
	unique []uint64,
	indexes []IndexGetPosition,
	out []uint64,
) {
	for _, it := range indexes {
		out[it.position] = unique[codes[it.index]]
	}
}

func (f *Field) UnpackScaled(indexes []IndexGetPosition, out []uint64) {
	min := f.Helpers[0]
	switch f.FinalType {
	case TypeByte:
		unpackScaled(f.ByteValues, min, indexes, out)
	case Type2Byte:
		unpackScaled(f.Byte2Values, min, indexes, out)
	case Type4Byte:
		unpackScaled(f.Byte4Values, min, indexes, out)
	case Type8Byte:
		unpackScaled(f.Byte8Values, min, indexes, out)
	default:
		panic(fmt.Sprintf("UnpackScaled: unsupported final type %v", f.FinalType))
	}
}

func unpackScaled[T Number](
	codes []T, min uint64, indexes []IndexGetPosition, out []uint64,
) {
	for _, it := range indexes {
		out[it.position] = min + uint64(codes[it.index])
	}
}

func (f *Field) UnpackTokenized(indexes []IndexGetPosition, tk Tokenizer, out []string) {
	blob, offs := f.ByteValues, f.ListOffsets
	for _, it := range indexes {
		out[it.position] = tk.Detokenize(blob[offs[it.index]:offs[it.index+1]])
	}
}

func (f *Field) UnpackIndexed(valid []IndexGetPosition, out []uint32) {
	vals := f.Byte4Values
	for _, it := range valid {
		out[it.position] = vals[it.index]
	}
}
