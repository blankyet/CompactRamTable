package compact_table

import (
	"encoding/binary"
	"fmt"
)

const compactTableMagic = "CMCTTBL"

type Serializer struct {
	data []byte
}

func (s *Serializer) U8(v uint8) {
	s.data = append(s.data, v)
}

func (s *Serializer) U16(v uint16) {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	s.data = append(s.data, b[:]...)
}

func (s *Serializer) U32(v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	s.data = append(s.data, b[:]...)
}

func (s *Serializer) U64(v uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	s.data = append(s.data, b[:]...)
}

func (s *Serializer) Bytes(v []byte) {
	s.data = append(s.data, v...)
}

func (s *Serializer) String(v string) {
	s.U32(uint32(len(v)))
	s.Bytes([]byte(v))
}

func (s *Serializer) ByteSlice(v []byte) {
	s.U32(uint32(len(v)))
	s.Bytes(v)
}

func (s *Serializer) U16Slice(values []uint16) {
	s.U32(uint32(len(values)))
	for _, v := range values {
		s.U16(v)
	}
}

func (s *Serializer) U32Slice(values []uint32) {
	s.U32(uint32(len(values)))
	for _, v := range values {
		s.U32(v)
	}
}

func (s *Serializer) U64Slice(values []uint64) {
	s.U32(uint32(len(values)))
	for _, v := range values {
		s.U64(v)
	}
}

func (s *Serializer) BoolSlice(values []bool) {
	s.U32(uint32(len(values)))
	for _, v := range values {
		if v {
			s.U8(1)
		} else {
			s.U8(0)
		}
	}
}

func (s *Serializer) Byte2D(values [][]byte) {
	s.U32(uint32(len(values)))
	for _, v := range values {
		s.ByteSlice(v)
	}
}

type Deserializer struct {
	data []byte
	pos  int
}

func NewDeserializer(data []byte) *Deserializer {
	return &Deserializer{data: data}
}

func (d *Deserializer) remaining(n int) {
	if n < 0 || d.pos+n > len(d.data) {
		panic(fmt.Sprintf(
			"deserialize: unexpected EOF at %d, need %d bytes, remaining %d",
			d.pos,
			n,
			len(d.data)-d.pos,
		))
	}
}

func (d *Deserializer) U8() uint8 {
	d.remaining(1)
	v := d.data[d.pos]
	d.pos++
	return v
}

func (d *Deserializer) U16() uint16 {
	d.remaining(2)
	v := binary.LittleEndian.Uint16(d.data[d.pos:])
	d.pos += 2
	return v
}

func (d *Deserializer) U32() uint32 {
	d.remaining(4)
	v := binary.LittleEndian.Uint32(d.data[d.pos:])
	d.pos += 4
	return v
}

func (d *Deserializer) U64() uint64 {
	d.remaining(8)
	v := binary.LittleEndian.Uint64(d.data[d.pos:])
	d.pos += 8
	return v
}

func (d *Deserializer) Bytes(n uint32) []byte {
	if uint64(n) > uint64(len(d.data)-d.pos) {
		panic(fmt.Sprintf(
			"deserialize: invalid byte length %d at %d",
			n,
			d.pos,
		))
	}

	v := d.data[d.pos : d.pos+int(n)]
	d.pos += int(n)
	return v
}

func (d *Deserializer) String() string {
	return string(d.Bytes(d.U32()))
}

func (d *Deserializer) ByteSlice() []byte {
	n := d.U32()
	v := d.Bytes(n)
	return append([]byte(nil), v...)
}

func (d *Deserializer) U16Slice() []uint16 {
	n := d.U32()
	v := make([]uint16, n)

	for i := range v {
		v[i] = d.U16()
	}

	return v
}

func (d *Deserializer) U32Slice() []uint32 {
	n := d.U32()
	v := make([]uint32, n)

	for i := range v {
		v[i] = d.U32()
	}

	return v
}

func (d *Deserializer) U64Slice() []uint64 {
	n := d.U32()
	v := make([]uint64, n)

	for i := range v {
		v[i] = d.U64()
	}

	return v
}

func (d *Deserializer) BoolSlice() []bool {
	n := d.U32()
	v := make([]bool, n)

	for i := range v {
		switch d.U8() {
		case 0:
		case 1:
			v[i] = true
		default:
			panic("deserialize: invalid bool")
		}
	}

	return v
}

func (d *Deserializer) Byte2D() [][]byte {
	n := d.U32()
	v := make([][]byte, n)

	for i := range v {
		v[i] = d.ByteSlice()
	}

	return v
}

func (d *Deserializer) Done() bool {
	return d.pos == len(d.data)
}

func (d *Deserializer) Pos() int {
	return d.pos
}

const (
	fieldFlagIsList          uint8 = 1 << 0
	fieldFlagWithSearchIndex uint8 = 1 << 1
)

func (s *Serializer) Tokenizer(tokenizer *PopularWordTokenizer) {
	s.String(tokenizer.Splitter)
	s.U32(uint32(tokenizer.PopularWordLimit))
	s.U32(uint32(tokenizer.ChunkSize))
	s.U8(tokenizer.BytePerTokenCount)

	s.U32(uint32(len(tokenizer.TokenAlphabet)))

	for token, num := range tokenizer.TokenAlphabet {
		s.String(token)
		s.U32(num)
	}

	s.U32(uint32(len(tokenizer.TokenWords.data)))
	s.Bytes(tokenizer.TokenWords.data)
	s.U32Slice(tokenizer.TokenWords.offsets)

	s.U32Slice(tokenizer.order)
}

func (d *Deserializer) Tokenizer(tokenizer *PopularWordTokenizer) {
	tokenizer.Splitter = d.String()
	tokenizer.PopularWordLimit = int(d.U32())
	tokenizer.ChunkSize = int(d.U32())
	tokenizer.BytePerTokenCount = d.U8()

	count := d.U32()

	tokenizer.TokenAlphabet = make(TokenAlphabet, count)

	for range count {
		token := d.String()
		num := d.U32()
		tokenizer.TokenAlphabet[token] = num
	}

	countWords := d.U32()
	tokenizer.TokenWords.data = d.Bytes(countWords)
	tokenizer.TokenWords.offsets = d.U32Slice()
	tokenizer.order = d.U32Slice()
}

func (s *Serializer) FieldData(v FieldData) {
	s.String(v.Name)
	s.U8(v.InitialType)
	s.U8(v.Tokenizer)
	s.String(v.ExternalTableName)

	s.U32(uint32(len(v.EnumNames)))
	for _, name := range v.EnumNames {
		s.String(name)
	}

	s.Byte2D(v.NullValues)

	var flags uint8
	if v.IsList {
		flags |= fieldFlagIsList
	}
	if v.WithSearchIndex {
		flags |= fieldFlagWithSearchIndex
	}
	s.U8(flags)
}

func (d *Deserializer) FieldData() FieldData {
	fd := FieldData{
		Name:              d.String(),
		InitialType:       d.U8(),
		Tokenizer:         d.U8(),
		ExternalTableName: d.String(),
	}

	if n := d.U32(); n > 0 {
		fd.EnumNames = make([]string, n)
		for i := range fd.EnumNames {
			fd.EnumNames[i] = d.String()
		}
	}

	if nulls := d.Byte2D(); len(nulls) > 0 {
		fd.NullValues = nulls
	}

	flags := d.U8()
	fd.IsList = flags&fieldFlagIsList != 0
	fd.WithSearchIndex = flags&fieldFlagWithSearchIndex != 0

	return fd
}
func (s *Serializer) FieldReverseIndex(f *Field) {
	index := &f.SearchIndex
	s.U8(uint8(index.Kind))
	s.U64Slice(index.Values)
	s.U32Slice(index.Offsets)
	s.U32Slice(index.Rows)
}

func (d *Deserializer) FieldReverseIndex(f *Field) {
	kind := SearchIndexKind(d.U8())
	values := d.U64Slice()
	offsets := d.U32Slice()
	rows := d.U32Slice()

	f.SearchIndex = SearchIndex{
		Kind:    kind,
		Values:  values,
		Offsets: offsets,
		Rows:    rows,
	}
}

func (s *Serializer) FieldValues(f *Field) {
	switch f.CompactingStrategy {
	case CompactingRaw:
		// Currently no CompactingRaw storage is produced by your
		// compaction code, but preserve BitValues if used.
		s.BoolSlice(f.BitValues)

	case CompactingScaled, CompactingEnumed:
		switch f.FinalType {
		case TypeByte:

			s.U32(uint32(len(f.ByteValues)))
			s.Bytes(f.ByteValues)

		case Type2Byte:

			s.U16Slice(f.Byte2Values)

		case Type4Byte:

			s.U32Slice(f.Byte4Values)

		case Type8Byte:

			s.U64Slice(f.Byte8Values)

		default:
			panic(fmt.Sprintf(
				"serialize: unsupported numeric FinalType %d",
				f.FinalType,
			))
		}

	case CompactingIndexed:
		s.U32Slice(f.Byte4Values)

		if f.Data.IsList {
			s.U32Slice(f.ListOffsets)
		}

	case CompactingTokenized:
		s.ByteSlice(f.ByteValues)
		s.U32Slice(f.ListOffsets)

	default:
		panic(fmt.Sprintf(
			"serialize: unsupported strategy %d",
			f.CompactingStrategy,
		))
	}
}

func (d *Deserializer) FieldValues(f *Field) {
	switch f.CompactingStrategy {
	case CompactingRaw:
		f.BitValues = d.BoolSlice()

	case CompactingScaled, CompactingEnumed:
		switch f.FinalType {
		case TypeByte:
			f.ByteValues = d.ByteSlice()
		case Type2Byte:
			f.Byte2Values = d.U16Slice()
		case Type4Byte:
			f.Byte4Values = d.U32Slice()
		case Type8Byte:
			f.Byte8Values = d.U64Slice()
		default:
			panic(fmt.Sprintf("deserialize: bad FinalType %d", f.FinalType))
		}

	case CompactingIndexed:
		f.Byte4Values = d.U32Slice()
		if f.Data.IsList {
			f.ListOffsets = d.U32Slice()
		}

	case CompactingTokenized:
		f.ByteValues = d.ByteSlice()
		f.ListOffsets = d.U32Slice()

	default:
		panic(fmt.Sprintf("deserialize: bad strategy %d", f.CompactingStrategy))
	}
}

func (t *CompactTable[Key]) Serialize(
	includeKeyIndex bool,
	includeReverseIndexes bool,
) []byte {
	_ = includeReverseIndexes // reserved for future format extension

	s := Serializer{
		data: make([]byte, 0),
	}

	mark := func(label string, start int) {
		fmt.Printf(
			"[Serialize] %-24s %8d bytes  (running total %8d)\n",
			label,
			len(s.data)-start,
			len(s.data),
		)
	}

	start := len(s.data)
	// Header.
	s.String(compactTableMagic)
	s.U8(0) // version major
	s.U8(0) // version minor
	s.U8(1) // version patch
	s.U32(t.ElementCount)

	mark("header", start)
	start = len(s.data)
	// Key index.
	if !includeKeyIndex {
		s.U8(0)
	} else {
		s.U8(1)

		if t.ElementCount == 0 {
			s.U32(0)
			s.U32(0)
		} else {
			codec, ok := any(t.IndexToKey[0]).(KeyToBytes[Key])
			if !ok {
				panic(fmt.Sprintf(
					"key type %T does not implement KeyToBytes",
					t.IndexToKey[0],
				))
			}

			first, ok := codec.ToBytes()
			if !ok {
				panic("cannot serialize first key")
			}

			keySize := len(first)

			s.U32(t.ElementCount)
			s.U32(uint32(keySize))

			for i := uint32(0); i < t.ElementCount; i++ {
				key := t.IndexToKey[i]

				b, ok := any(key).(KeyToBytes[Key]).ToBytes()
				if !ok {
					panic(fmt.Sprintf(
						"cannot serialize key at index %d",
						i,
					))
				}

				if len(b) != keySize {
					panic(fmt.Sprintf(
						"key at index %d has size %d, expected %d",
						i,
						len(b),
						keySize,
					))
				}

				s.Bytes(b)
			}
		}
	}
	mark("key index", start)

	// Fields.
	s.U32(uint32(len(t.Fields)))

	for i := range t.Fields {
		f := &t.Fields[i]
		start = len(s.data)
		s.FieldData(f.Data)

		if f.Empty {
			s.U8(1)
			continue
		}
		s.U8(0)

		s.U8(f.FinalType)
		s.U8(f.CompactingStrategy)

		if f.HasNullBits {
			s.U8(1)
			s.U64Slice(f.NullBits)
		} else {
			s.U8(0)
		}

		s.U64Slice(f.Helpers)

		s.FieldValues(f)
		mark(fmt.Sprintf("field %s", f.Data.Name), start)

		if f.Data.WithSearchIndex {
			start = len(s.data)
			s.U8(1)

			s.FieldReverseIndex(f)
			mark(fmt.Sprintf("Reverse index %s", f.Data.Name), start)
		} else {
			s.U8(0)
		}

	}
	// Tokenizers
	s.Tokenizer(t.WordTokenizer)
	s.Tokenizer(t.URLsTokenizer)
	s.Tokenizer(t.AliasesTokenizer)

	// External tables.
	s.U32(uint32(len(t.externalTables)))

	for name, ext := range t.externalTables {
		s.String(name)

		data := ext.Serialize(true, false)
		s.U32(uint32(len(data)))
		s.Bytes(data)
	}

	fmt.Printf("[Serialize] %-24s %8d bytes\n", "TOTAL", len(s.data))
	return s.data
}

func DeserializeCompactTable[Key comparable](
	data []byte,
	name string,
) *CompactTable[Key] {
	d := NewDeserializer(data)

	if got := d.String(); got != compactTableMagic {
		panic(fmt.Sprintf("deserialize: invalid magic %q", got))
	}

	major := d.U8()
	minor := d.U8()
	patch := d.U8()

	if major != 0 || minor != 0 || patch != 1 {
		panic(fmt.Sprintf(
			"deserialize: unsupported version %d.%d.%d",
			major, minor, patch,
		))
	}

	elementCount := d.U32()

	t := New[Key](name, elementCount, nil)
	t.Compacted = true
	t.ElementCount = elementCount

	// Key index.
	if d.U8() != 0 {
		keyCount := d.U32()
		keySize := d.U32()

		if keyCount != elementCount {
			panic(fmt.Sprintf(
				"deserialize: key count %d != element count %d",
				keyCount,
				elementCount,
			))
		}

		var decoder Key
		for i := uint32(0); i < keyCount; i++ {
			b := d.Bytes(keySize)

			key, ok := any(decoder).(interface {
				FromBytes([]byte) (Key, bool)
			}).FromBytes(b)
			if !ok {
				panic(fmt.Sprintf(
					"deserialize: cannot decode key at index %d",
					i,
				))
			}

			t.IndexToKey[i] = key
			t.KeyToIndex[key] = i
		}
	}

	// Fields.
	fieldCount := d.U32()
	t.Fields = make([]Field, fieldCount)
	t.fieldNameIndex = make(map[string]int, fieldCount)

	for i := range t.Fields {
		f := &t.Fields[i]

		f.Data = d.FieldData()
		t.fieldNameIndex[f.Data.Name] = i

		f.Empty = d.U8() != 0
		if f.Empty {
			continue
		}

		f.FinalType = d.U8()
		f.CompactingStrategy = d.U8()

		if d.U8() != 0 {
			f.HasNullBits = true
			f.NullBits = d.U64Slice()
			f.BuildNullPrefix()
		}

		f.Helpers = d.U64Slice()
		d.FieldValues(f)
		if d.U8() != 0 {
			d.FieldReverseIndex(f)
		}
	}

	d.Tokenizer(t.WordTokenizer)
	d.Tokenizer(t.URLsTokenizer)
	d.Tokenizer(t.AliasesTokenizer)

	externalCount := d.U32()

	external := make(map[string]*CompactTable[Key], externalCount)

	for i := uint32(0); i < externalCount; i++ {
		tableName := d.String()
		size := d.U32()
		tableData := d.Bytes(size)

		external[tableName] = DeserializeCompactTable[Key](
			tableData,
			tableName,
		)
	}

	t.externalTables = external

	if !d.Done() {
		panic(fmt.Sprintf(
			"deserialize: %d trailing bytes",
			len(data)-d.Pos(),
		))
	}

	return t
}
