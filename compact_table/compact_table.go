package compact_table

import (
	"cmp"
	"log"
	"math"
	"math/bits"
	"slices"
	"time"
)

type Field struct {
	Data  FieldData
	Empty bool

	FinalType          FinalTypeEnum
	CompactingStrategy CompactingStrategyEnum
	Helpers            []uint64
	HasNullBits        bool
	NullBits           []uint64
	NullPrefix         []uint32

	BitValues   []bool
	ByteValues  []uint8
	Byte2Values []uint16
	Byte4Values []uint32
	Byte8Values []uint64

	OriginalData [][][]byte

	IsList      bool
	ListOffsets []uint32

	SearchIndex SearchIndex
}

func (f *Field) DenseIndices(indices, dst []IndexGetPosition) []IndexGetPosition {
	if !f.HasNullBits {
		return indices
	}

	dst = dst[:0]
	nullBits, prefix := f.NullBits, f.NullPrefix

	for _, it := range indices {
		word, bit := it.index>>6, it.index&63
		w := nullBits[word]

		if w&(1<<bit) != 0 {
			continue // null: в Values его нет
		}

		dst = append(dst, IndexGetPosition{
			index:    it.index - prefix[word] - uint32(bits.OnesCount64(w&((1<<bit)-1))),
			position: it.position,
		})
	}
	return dst
}

type CompactTable[Key comparable] struct {
	Compacted bool
	Name      string

	ElementCount    uint32
	ElementCapacity uint32

	KeyToIndex map[Key]uint32
	IndexToKey []Key

	Fields []Field

	WordTokenizer    *PopularWordTokenizer
	AliasesTokenizer *PopularWordTokenizer
	URLsTokenizer    *PopularWordTokenizer

	externalTables map[string]*CompactTable[Key]
	fieldNameIndex map[string]int
}

func (f *Field) SetNull(index uint32) {
	if f.NullBits == nil {
		f.NullBits = make([]uint64, (uint32(len(f.OriginalData))+63)>>6)
	}
	f.HasNullBits = true
	f.NullBits[index>>6] |= uint64(1) << (index & 63)
}

func (f *Field) setNullAt(index uint32) {
	f.OriginalData[index] = nil
	f.SetNull(index)
}

func (t *CompactTable[K]) external(name string) *CompactTable[K] {
	if name == t.Name {
		return t
	}
	return t.externalTables[name]
}

func (f *Field) IsNull(index uint32) bool {
	return f.HasNullBits &&
		f.NullBits[index>>6]&(uint64(1)<<(index&63)) != 0
}

func (f *Field) BuildNullPrefix() {
	if !f.HasNullBits {
		f.NullPrefix = nil
		return
	}
	f.NullPrefix = make([]uint32, len(f.NullBits))
	var acc uint32
	for w, word := range f.NullBits {
		f.NullPrefix[w] = acc
		acc += uint32(bits.OnesCount64(word))
	}
}

func (g *CompactTable[key]) tokenizer(kind TokenizerTypeEnum) Tokenizer {
	switch kind {
	case TokenizerStandardText:
		return g.WordTokenizer
	case TokenizerAliases:
		return g.AliasesTokenizer
	case TokenizerUrls:
		return g.URLsTokenizer
	default:
		return nil
	}
}

func New[K comparable](name string, elementCapacity uint32, fieldDefinitions []FieldData) *CompactTable[K] {
	nameIndex := make(map[string]int, len(fieldDefinitions))
	fields := make([]Field, len(fieldDefinitions))

	for i, definition := range fieldDefinitions {
		nameIndex[definition.Name] = i
		fields[i] = Field{
			Data:         definition,
			OriginalData: make([][][]byte, elementCapacity),
		}
	}

	return &CompactTable[K]{
		Name:            name,
		ElementCapacity: elementCapacity,
		KeyToIndex:      make(map[K]uint32, elementCapacity),
		IndexToKey:      make([]K, elementCapacity),
		Fields:          fields,
		fieldNameIndex:  nameIndex,

		WordTokenizer:    NewPopularWordTokenizer(" ", 300000, 4),
		AliasesTokenizer: NewPopularWordTokenizer("-", 300000, 3),
		URLsTokenizer:    NewPopularWordTokenizer("/", 300000, 3),
	}
}

func (g *CompactTable[K]) PutSingleRecord(key K, data map[string][][]byte) {
	index := g.ElementCount
	if index >= g.ElementCapacity || g.Compacted {
		return
	}

	g.KeyToIndex[key] = index
	g.IndexToKey[index] = key
	g.ElementCount++

	for i := range g.Fields {
		field := &g.Fields[i]
		blobs := data[field.Data.Name] // nil if the field is missing

		if field.Data.IsList {
			kept := blobs[:0:0]
			for _, b := range blobs {
				if !field.Data.IsNullBlob(b) {
					kept = append(kept, b)
				}
			}
			if len(kept) == 0 {
				field.setNullAt(index)
				continue
			}
			blobs = kept
		} else if len(blobs) == 0 || field.Data.IsNullBlob(blobs[0]) {
			field.setNullAt(index)
			continue
		}

		field.OriginalData[index] = blobs
	}
}

func (t *CompactTable[K]) Compact(externalTables map[string]*CompactTable[K]) {
	t.externalTables = externalTables

	if t.Compacted {
		return
	}
	// Pass 1: feed every tokenized (non-list) field into its tokenizer,
	// then build the alphabets.
	for i := range t.Fields {
		field := &t.Fields[i]
		tk := t.tokenizer(field.Data.Tokenizer)
		if tk == nil || field.Data.IsList {
			continue
		}

		for e := uint32(0); e < t.ElementCount; e++ {
			if raw := field.OriginalData[e]; len(raw) != 0 {
				tk.AddTextToAnalyze(string(raw[0]))
			}
		}
	}

	t.WordTokenizer.ComputeTokens()
	t.AliasesTokenizer.ComputeTokens()
	t.URLsTokenizer.ComputeTokens()

	// Pass 2: compact every field.
	for i := range t.Fields {
		field := &t.Fields[i]

		if field.Data.InitialType == TypeExternalIndex {
			TryCompactExternal(field, t.ElementCount, t.external(field.Data.ExternalTableName))
			continue
		}

		if tk := t.tokenizer(field.Data.Tokenizer); tk != nil && !field.Data.IsList {
			TryCompactStrings(field, t.ElementCount, tk)
			continue
		}

		if !field.Data.IsList {
			TryCompactField(field, t.ElementCount)
		}

	}

	// Pass 3: drop null slots from the scaled/enumed arrays and build rank prefixes.
	for i := range t.Fields {
		field := &t.Fields[i]
		field.OriginalData = nil

		if field.Empty {
			continue
		}

		if field.Data.WithSearchIndex {
			field.BuildEnumSearchIndex(t.ElementCount)
			field.BuildSortedSearchIndex(t.ElementCount)
			if tk := t.tokenizer(field.Data.Tokenizer); tk != nil {
				field.BuildTokenSearchIndex(tk, t.ElementCount)
			}
			if field.CompactingStrategy == CompactingIndexed {
				ext := t.external(field.Data.ExternalTableName)
				field.BuildExternalIndexSearchIndex(t.ElementCount, ext.ElementCount)
			}
		}

		MakeFieldDense(field, t.ElementCount)
		field.BuildNullPrefix()
	}

	t.Compacted = true
}

type DataView struct {
	Count   int
	Columns []Column
	Scores  []float64
	Rows    []uint32
}

type IndexGetPosition struct {
	index    uint32 // index in db
	position int    // index in request
}

func (c *Column) IsNull(pos int) bool {
	return c.Nulls != nil && c.Nulls[pos>>6]&(1<<(uint(pos)&63)) != 0
}

type Column struct {
	Values  []uint64
	Strings []string

	Refs       []uint32   // indexed, одиночная ссылка
	Lists      [][]uint32 // indexed, список: под-слайсы Byte4Values (без копирования), null = nil
	Sub        *DataView
	SubOffsets []uint32 // списки + depth>0: строки Sub для position = [SubOffsets[pos], SubOffsets[pos+1])
	Nulls      []uint64
}

func (c *Column) ListRows(pos int) (from, to int) {
	return int(c.SubOffsets[pos]), int(c.SubOffsets[pos+1])
}

func (f *Field) UnpackIndexedList(valid []IndexGetPosition, out [][]uint32) {
	vals, offs := f.Byte4Values, f.ListOffsets
	for _, it := range valid {
		out[it.position] = vals[offs[it.index]:offs[it.index+1]:offs[it.index+1]]
	}
}

func (c *Column) SubRow(pos int) (int, bool) {
	if c.IsNull(pos) {
		return 0, false
	}
	row := pos
	for w := 0; w < pos>>6; w++ { // вычитаем null-ы в полных словах
		row -= bits.OnesCount64(c.Nulls[w])
	}
	if c.Nulls != nil {
		row -= bits.OnesCount64(c.Nulls[pos>>6] & ((1 << (uint(pos) & 63)) - 1))
	}
	return row, true
}

func decode(f *Field, x uint64) any {
	switch f.Data.InitialType {
	case TypeInt32:
		return int32(x)
	case TypeInt64:
		return int64(x)
	case TypeUInt32:
		return uint32(x)
	case TypeFloat:
		return math.Float32frombits(uint32(x))
	case TypeDouble:
		return math.Float64frombits(x)
	}
	return x
}

func (t *CompactTable[K]) Row(v *DataView, row int) map[string]any {
	m := make(map[string]any, len(t.Fields))
	for i := range t.Fields {
		f, c := &t.Fields[i], &v.Columns[i]
		if f.Empty || c.IsNull(row) {
			continue
		}
		name := f.Data.Name
		switch f.CompactingStrategy {
		case CompactingEnumed, CompactingScaled:
			m[name] = decode(f, c.Values[row])
		case CompactingTokenized:
			m[name] = c.Strings[row]
		case CompactingIndexed:
			ext := t.external(f.Data.ExternalTableName)
			switch {
			case f.Data.IsList && c.Sub != nil:
				from, to := c.ListRows(row)
				rows := make([]map[string]any, 0, to-from)
				for j := from; j < to; j++ {
					rows = append(rows, ext.Row(c.Sub, j))
				}
				m[name] = rows
			case f.Data.IsList:
				keys := make([]K, len(c.Lists[row]))
				for j, r := range c.Lists[row] {
					keys[j] = ext.IndexToKey[r]
				}
				m[name] = keys
			case c.Sub != nil:
				if sr, ok := c.SubRow(row); ok {
					m[name] = ext.Row(c.Sub, sr)
				}
			default:
				m[name] = ext.IndexToKey[c.Refs[row]]
			}
		}
	}
	return m
}

func (t *CompactTable[K]) GetMany(keys []K, depth int) (*DataView, bool) {
	if !t.Compacted {
		return nil, false
	}
	idx := make([]IndexGetPosition, len(keys))
	for pos, k := range keys {
		i, ok := t.KeyToIndex[k]
		if !ok {
			return nil, false
		}
		idx[pos] = IndexGetPosition{i, pos}
	}
	return t.getManyByIndexes(idx, depth), true
}

func (t *CompactTable[K]) GetManyByIndexes(ids []uint32, depth int) (*DataView, bool) {
	if !t.Compacted {
		return nil, false
	}
	idx := make([]IndexGetPosition, len(ids))
	for pos, id := range ids {
		if id >= t.ElementCount {
			return nil, false
		}
		idx[pos] = IndexGetPosition{id, pos}
	}
	return t.getManyByIndexes(idx, depth), true
}

func (t *CompactTable[K]) getManyByIndexes(idx []IndexGetPosition, depth int) *DataView {
	slices.SortFunc(idx, func(a, b IndexGetPosition) int { return cmp.Compare(a.index, b.index) })

	n := len(idx)
	view := &DataView{
		Count:   n,
		Rows:    make([]uint32, n),
		Columns: make([]Column, len(t.Fields)),
	}
	scratch := make([]IndexGetPosition, 0, n)

	for _, it := range idx {
		view.Rows[it.position] = it.index
	}

	for i := range t.Fields {
		f := &t.Fields[i]
		if f.Empty {
			continue
		}
		col := &view.Columns[i]

		switch f.CompactingStrategy {
		case CompactingEnumed, CompactingScaled:
			col.Values = make([]uint64, n)
		case CompactingTokenized:
			col.Strings = make([]string, n)
		case CompactingIndexed:
			if f.Data.IsList {
				col.Lists = make([][]uint32, n)
			} else {
				col.Refs = make([]uint32, n)
			}
		default:
			continue
		}

		valid := f.DenseIndices(idx, scratch)
		if f.HasNullBits {
			scratch = valid
			col.Nulls = make([]uint64, (n+63)>>6)
			for _, it := range idx {
				if f.IsNull(it.index) {
					col.Nulls[it.position>>6] |= 1 << (uint(it.position) & 63)
				}
			}
		}

		switch f.CompactingStrategy {
		case CompactingTokenized:
			f.UnpackTokenized(valid, t.tokenizer(f.Data.Tokenizer), col.Strings)
		case CompactingIndexed:
			if f.Data.IsList {
				f.UnpackIndexedList(valid, col.Lists)
			} else {
				f.UnpackIndexed(valid, col.Refs)
			}
			if depth > 0 {
				t.resolveRefs(f, col, depth-1)
			}
		default:
			f.GetUnpackedValues(valid, col.Values)
		}
	}
	return view
}

// Один запрос во внешнюю таблицу за все непустые refs колонки.
func (t *CompactTable[K]) resolveRefs(f *Field, col *Column, depth int) {
	ext := t.external(f.Data.ExternalTableName)
	if ext == nil {
		return
	}
	var ids []IndexGetPosition
	add := func(r uint32) { ids = append(ids, IndexGetPosition{r, len(ids)}) }
	if f.Data.IsList {
		col.SubOffsets = make([]uint32, len(col.Lists)+1)
		for pos, l := range col.Lists {
			col.SubOffsets[pos] = uint32(len(ids))
			for _, r := range l {
				add(r)
			}
		}
		col.SubOffsets[len(col.Lists)] = uint32(len(ids))
	} else {
		for pos, r := range col.Refs {
			if !col.IsNull(pos) {
				add(r)
			}
		}
	}

	uniq := make(map[uint32]struct{}, len(ids))
	for _, it := range ids {
		uniq[it.index] = struct{}{}
	}
	start := time.Now()

	col.Sub = ext.getManyByIndexes(ids, depth)

	log.Printf("[resolve] field=%s refs=%d unique=%d load=%s",
		f.Data.Name, len(ids), len(uniq), time.Since(start).Round(time.Microsecond))
}
