package compact_table

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type SearchIndexKind uint8

const (
	SearchIndexNone SearchIndexKind = iota
	SearchIndexInverted
	SearchIndexSorted
)

type SearchIndex struct {
	Kind SearchIndexKind

	Values  []uint64
	Offsets []uint32
	Rows    []uint32
}

func (f *Field) BuildEnumSearchIndex(count uint32) {
	if f.CompactingStrategy != CompactingEnumed {
		return
	}

	offsets := make([]uint32, len(f.Helpers)+1)

	for row := uint32(0); row < count; row++ {
		if f.IsNull(row) {
			continue
		}

		var enum uint32

		switch f.FinalType {
		case TypeByte:
			enum = uint32(f.ByteValues[row])
		case Type2Byte:
			enum = uint32(f.Byte2Values[row])
		case Type4Byte:
			enum = f.Byte4Values[row]
		}

		offsets[enum+1]++
	}

	for i := 1; i < len(offsets); i++ {
		offsets[i] += offsets[i-1]
	}

	rows := make([]uint32, count)

	next := append([]uint32(nil), offsets[:len(offsets)-1]...)

	for row := uint32(0); row < count; row++ {
		if f.IsNull(row) {
			continue
		}

		var enum uint32

		switch f.FinalType {
		case TypeByte:
			enum = uint32(f.ByteValues[row])
		case Type2Byte:
			enum = uint32(f.Byte2Values[row])
		case Type4Byte:
			enum = f.Byte4Values[row]
		}

		rows[next[enum]] = row
		next[enum]++
	}

	f.SearchIndex = SearchIndex{
		Kind:    SearchIndexInverted,
		Offsets: offsets,
		Rows:    rows,
	}
}

func (f *Field) BuildExternalIndexSearchIndex(count, externalCount uint32) {
	if f.CompactingStrategy != CompactingIndexed {
		return
	}

	offsets := make([]uint32, externalCount+1)

	for row := uint32(0); row < count; row++ {
		if f.IsNull(row) {
			continue
		}

		if f.Data.IsList {
			start := f.ListOffsets[row]
			end := f.ListOffsets[row+1]

			for i := start; i < end; i++ {
				offsets[f.Byte4Values[i]+1]++
			}
		} else {
			offsets[f.Byte4Values[row]+1]++
		}
	}

	for i := 1; i < len(offsets); i++ {
		offsets[i] += offsets[i-1]
	}

	rows := make([]uint32, offsets[externalCount])
	next := slices.Clone(offsets[:externalCount])

	for row := uint32(0); row < count; row++ {
		if f.IsNull(row) {
			continue
		}

		if f.Data.IsList {
			start := f.ListOffsets[row]
			end := f.ListOffsets[row+1]

			for i := start; i < end; i++ {
				externalRow := f.Byte4Values[i]
				rows[next[externalRow]] = row
				next[externalRow]++
			}
		} else {
			externalRow := f.Byte4Values[row]
			rows[next[externalRow]] = row
			next[externalRow]++
		}
	}

	f.SearchIndex = SearchIndex{
		Kind:    SearchIndexInverted,
		Offsets: offsets,
		Rows:    rows,
	}
}

func (f *Field) BuildSortedSearchIndex(numRows uint32) {
	if !f.Data.WithSearchIndex || f.CompactingStrategy != CompactingScaled {
		return
	}

	count := 0
	switch f.FinalType {
	case TypeByte:
		count = len(f.ByteValues)
	case Type2Byte:
		count = len(f.Byte2Values)
	case Type4Byte:
		count = len(f.Byte4Values)
	case Type8Byte:
		count = len(f.Byte8Values)
	default:
		panic(fmt.Sprintf("BuildSortedSearchIndex: unsupported final type %v", f.FinalType))
	}

	min := f.Helpers[0]
	at := func(pos int) uint64 {
		switch f.FinalType {
		case TypeByte:
			return min + uint64(f.ByteValues[pos])
		case Type2Byte:
			return min + uint64(f.Byte2Values[pos])
		case Type4Byte:
			return min + uint64(f.Byte4Values[pos])
		default:
			return min + f.Byte8Values[pos]
		}
	}

	perRow := count == int(numRows)

	type entry struct {
		v uint64
		r uint32
	}
	entries := make([]entry, 0, count)

	dense := 0
	for row := uint32(0); row < numRows; row++ {
		if f.IsNull(row) {
			continue
		}
		pos := dense
		if perRow {
			pos = int(row)
		}
		entries = append(entries, entry{at(pos), row})
		dense++
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].v < entries[j].v })

	values := make([]uint64, len(entries))
	rows := make([]uint32, len(entries))
	for i, e := range entries {
		values[i], rows[i] = e.v, e.r
	}

	f.SearchIndex = SearchIndex{Kind: SearchIndexSorted, Values: values, Rows: rows}
}

func (f *Field) BuildTokenSearchIndex(tokenizer Tokenizer, count uint32) {
	if !f.Data.WithSearchIndex || f.CompactingStrategy != CompactingTokenized {
		return
	}

	tk := tokenizer.(*PopularWordTokenizer)
	tokenCount := tk.TokenWords.Len()
	bytesPerToken := int(f.Helpers[0])
	recordSize := bytesPerToken + 1

	counts := make([]uint32, tokenCount)
	seen := make([]uint32, tokenCount)

	for row := uint32(0); row < count; row++ {
		start, end := f.ListOffsets[row], f.ListOffsets[row+1]

		for pos := start; pos < end; pos += uint32(recordSize) {
			id := readToken(f.ByteValues[pos:], bytesPerToken)

			if seen[id] == row+1 {
				continue
			}
			seen[id] = row + 1
			counts[id]++
		}
	}

	offsets := make([]uint32, tokenCount+1)
	for i := range counts {
		offsets[i+1] = offsets[i] + counts[i]
	}

	rows := make([]uint32, offsets[tokenCount])
	next := append([]uint32(nil), offsets[:tokenCount]...)

	clear(seen)

	// Store original row IDs.
	for row := uint32(0); row < count; row++ {
		start, end := f.ListOffsets[row], f.ListOffsets[row+1]

		for pos := start; pos < end; pos += uint32(recordSize) {
			id := readToken(f.ByteValues[pos:], bytesPerToken)

			if seen[id] == row+1 {
				continue
			}
			seen[id] = row + 1

			rows[next[id]] = row
			next[id]++
		}
	}

	f.SearchIndex = SearchIndex{
		Kind:    SearchIndexInverted,
		Offsets: offsets,
		Rows:    rows,
	}
}

func readToken(data []byte, bytesPerToken int) uint32 {
	if bytesPerToken == 2 {
		return uint32(binary.LittleEndian.Uint16(data))
	}
	return binary.LittleEndian.Uint32(data)
}

const queryKey = "query"

type stepTimer struct {
	label string
	start time.Time
	last  time.Time
	parts []string
}

func newStepTimer(label string) *stepTimer {
	now := time.Now()
	return &stepTimer{label: label, start: now, last: now}
}

func (s *stepTimer) step(name string) {
	now := time.Now()
	s.parts = append(s.parts, fmt.Sprintf("%s=%s", name, now.Sub(s.last).Round(time.Microsecond)))
	s.last = now
}

func (s *stepTimer) notef(format string, args ...any) {
	s.parts = append(s.parts, fmt.Sprintf(format, args...))
}

func (s *stepTimer) done() {
	log.Printf("[%s] total=%s | %s",
		s.label, time.Since(s.start).Round(time.Microsecond), strings.Join(s.parts, " "))
}

type SearchOperation struct {
	Text     string
	Equals   *uint64 // enum / sorted: raw value (ints as uint64, floats as Float64bits)
	Min, Max *uint64 // enum / sorted: inclusive range, either bound optional
}

func (op SearchOperation) isRange() bool {
	return op.Equals != nil || op.Min != nil || op.Max != nil
}

func (op SearchOperation) bounds() (lo, hi uint64) {
	lo, hi = 0, math.MaxUint64
	if op.Equals != nil {
		lo, hi = *op.Equals, *op.Equals
	}
	if op.Min != nil {
		lo = *op.Min
	}
	if op.Max != nil {
		hi = *op.Max
	}
	return
}

func (t *CompactTable[K]) SearchQuery(query string, limit, depth int) (*DataView, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return &DataView{}, nil
	}
	start := time.Now()
	ops, relationOps, err := t.buildOps(query)
	if err != nil {
		return nil, err
	}
	log.Printf(
		"[search] query=%q parse=%s ops=%d relations=%d limit=%d depth=%d",
		query,
		time.Since(start).Round(time.Microsecond),
		len(ops),
		len(relationOps),
		limit,
		depth,
	)
	return t.Search(ops, relationOps, limit, depth)
}

func (t *CompactTable[K]) fieldByName(name string) *Field {
	for i := range t.Fields {
		if t.Fields[i].Data.Name == name {
			return &t.Fields[i]
		}
	}
	return nil
}

func nextValue(typ InitialTypeEnum, v uint64, greater bool) uint64 {
	switch typ {
	case TypeInt32:
		x := int32(uint32(v))
		if greater {
			x++
		} else {
			x--
		}
		return uint64(uint32(x))

	case TypeInt64:
		x := int64(v)
		if greater {
			x++
		} else {
			x--
		}
		return uint64(x)

	case TypeUInt32:
		x := uint32(v)
		if greater {
			x++
		} else {
			x--
		}
		return uint64(x)

	case TypeUInt64:
		if greater {
			return v + 1
		}
		return v - 1

	case TypeDouble:
		x := math.Float64frombits(v)
		if greater {
			x = math.Nextafter(x, math.Inf(1))
		} else {
			x = math.Nextafter(x, math.Inf(-1))
		}
		return math.Float64bits(x)
	}

	panic(fmt.Sprintf("nextValue: unsupported type %v", typ))
}

func (t *CompactTable[K]) splitMarker(w string) (f *Field, op, value string, ok bool) {
	i := strings.IndexAny(w, "=<>~")
	if i <= 0 {
		return nil, "", "", false
	}
	if f = t.fieldByName(w[:i]); f == nil {
		return nil, "", "", false
	}
	rest := w[i:]
	for _, o := range [...]string{">=", "=>", "<=", "=<", ">", "<", "=", "~"} {
		if strings.HasPrefix(rest, o) {
			val := rest[len(o):]
			switch o {
			case "=>":
				o = ">="
			case "=<":
				o = "<="
			}
			return f, o, val, true
		}
	}
	return nil, "", "", false
}

func buildFieldSearchOperation(
	f *Field,
	opName string,
	value string,
) (SearchOperation, error) {
	if value == "" {
		return SearchOperation{}, fmt.Errorf(
			"%s: empty value",
			f.Data.Name,
		)
	}

	if !f.Data.WithSearchIndex {
		return SearchOperation{}, fmt.Errorf(
			"%s: field is not searchable",
			f.Data.Name,
		)
	}

	var op SearchOperation
	tokenized := f.CompactingStrategy == CompactingTokenized

	switch opName {
	case "~":
		if !tokenized {
			return SearchOperation{}, fmt.Errorf(
				"%s: ~ works only on text fields",
				f.Data.Name,
			)
		}

		op.Text = value

	case "=":
		if tokenized {
			op.Text = value
			break
		}

		if loText, hiText, ok := strings.Cut(value, ".."); ok {
			lo, ok1 := parseSearchValue(f, loText)
			hi, ok2 := parseSearchValue(f, hiText)

			if !ok1 || !ok2 {
				return SearchOperation{}, fmt.Errorf(
					"%s: bad range %q",
					f.Data.Name,
					value,
				)
			}

			op.Min = &lo
			op.Max = &hi
			break
		}

		v, ok := parseSearchValue(f, value)
		if !ok {
			return SearchOperation{}, fmt.Errorf(
				"%s: bad value %q",
				f.Data.Name,
				value,
			)
		}

		op.Equals = &v

	case ">=", ">", "<=", "<":
		if tokenized {
			return SearchOperation{}, fmt.Errorf(
				"%s: %s is not supported on text fields",
				f.Data.Name,
				opName,
			)
		}

		v, ok := parseSearchValue(f, value)
		if !ok {
			return SearchOperation{}, fmt.Errorf(
				"%s: bad value %q",
				f.Data.Name,
				value,
			)
		}

		switch opName {
		case ">=":
			op.Min = &v
		case ">":
			v = nextValue(f.Data.InitialType, v, true)
			op.Min = &v
		case "<=":
			op.Max = &v
		case "<":
			v = nextValue(f.Data.InitialType, v, false)
			op.Max = &v
		}

	default:
		return SearchOperation{}, fmt.Errorf(
			"%s: unknown operator %q",
			f.Data.Name,
			opName,
		)
	}

	return op, nil
}

func (t *CompactTable[K]) splitRelationMarker(w string) (relation, field, op, value string, ok bool) {
	i := strings.IndexAny(w, "=<>~")
	if i <= 0 {
		return
	}
	left := w[:i]
	dot := strings.IndexByte(left, '.')
	if dot <= 0 || dot == len(left)-1 {
		return
	}
	relation, field = left[:dot], left[dot+1:]

	rf := t.fieldByName(relation)
	if rf == nil || rf.Data.ExternalTableName == "" {
		return "", "", "", "", false
	}
	ext := t.external(rf.Data.ExternalTableName)
	if ext == nil || ext.fieldByName(field) == nil {
		return "", "", "", "", false
	}

	rest := w[i:]
	for _, o := range [...]string{">=", "=>", "<=", "=<", ">", "<", "=", "~"} {
		if strings.HasPrefix(rest, o) {
			val := rest[len(o):]
			switch o {
			case "=>":
				o = ">="
			case "=<":
				o = "<="
			}
			return relation, field, o, val, true
		}
	}
	return "", "", "", "", false
}

func mergeOp(dst *SearchOperation, src SearchOperation) {
	if src.Text != "" {
		dst.Text = src.Text
	}
	if src.Equals != nil {
		dst.Equals = src.Equals
	}
	if src.Min != nil {
		dst.Min = src.Min
	}
	if src.Max != nil {
		dst.Max = src.Max
	}
}

func (t *CompactTable[K]) buildOps(q string) (
	map[string]SearchOperation,
	map[string]SearchOperation,
	error,
) {
	type clause struct {
		key      string // "release_year" or "actors.name"
		op, val  string
		field    *Field
		relation bool
	}

	var (
		free []string
		cl   []clause
	)

	for _, w := range strings.Fields(q) {
		if rel, fn, op, v, ok := t.splitRelationMarker(w); ok {
			ext := t.external(t.fieldByName(rel).Data.ExternalTableName)
			cl = append(cl, clause{
				key: rel + "." + fn, op: op, val: v,
				field: ext.fieldByName(fn), relation: true,
			})
			continue
		}

		if f, op, v, ok := t.splitMarker(w); ok {
			cl = append(cl, clause{key: f.Data.Name, op: op, val: v, field: f})
			continue
		}

		if n := len(cl); n > 0 {
			cl[n-1].val = strings.TrimSpace(cl[n-1].val + " " + w)
		} else {
			free = append(free, w)
		}
	}

	ops := map[string]SearchOperation{}
	relations := map[string]SearchOperation{}

	if len(free) > 0 {
		ops[queryKey] = SearchOperation{Text: strings.Join(free, " ")}
	}

	for _, c := range cl {
		built, err := buildFieldSearchOperation(c.field, c.op, c.val)
		if err != nil {
			if c.relation {
				err = fmt.Errorf("%s: %w", c.key, err)
			}
			return nil, nil, err
		}

		target := ops
		if c.relation {
			target = relations
		}
		cur := target[c.key]
		mergeOp(&cur, built)
		target[c.key] = cur
	}

	return ops, relations, nil
}

func parseSearchValue(f *Field, s string) (uint64, bool) {
	if len(f.Data.EnumNames) > 0 {
		for i, n := range f.Data.EnumNames {
			if strings.EqualFold(n, s) {
				return uint64(i), true
			}
		}
	}

	switch f.Data.InitialType {
	case TypeDouble:
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return math.Float64bits(v), true

	case TypeInt32:
		v, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return 0, false
		}
		return uint64(uint32(int32(v))), true

	case TypeInt64:
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, false
		}
		return uint64(v), true

	case TypeUInt32:
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return 0, false
		}
		return v, true

	case TypeUInt64:
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}

	return 0, false
}

func (f *Field) externalParentRows(view *DataView) []uint32 {
	ix := &f.SearchIndex
	if ix.Kind != SearchIndexInverted {
		return nil
	}

	rows := make([]uint32, 0)

	for _, externalRow := range view.Rows {
		if int(externalRow)+1 >= len(ix.Offsets) {
			continue
		}

		start := ix.Offsets[externalRow]
		end := ix.Offsets[externalRow+1]

		rows = append(rows, ix.Rows[start:end]...)
	}

	slices.Sort(rows)

	if len(rows) == 0 {
		return nil
	}

	out := rows[:1]
	for _, row := range rows[1:] {
		if row != out[len(out)-1] {
			out = append(out, row)
		}
	}

	return out
}

func (t *CompactTable[K]) Search(
	ops map[string]SearchOperation,
	relationOps map[string]SearchOperation,
	limit, depth int,
) (*DataView, error) {
	if len(ops) == 0 && len(relationOps) == 0 {
		return nil, errors.New("search: empty query")
	}
	tm := newStepTimer("search")
	defer tm.done()
	for name := range ops {
		if name != queryKey && t.fieldByName(name) == nil {
			return nil, fmt.Errorf("search: unknown field %q", name)
		}
	}

	type textOp struct {
		f  *Field
		op SearchOperation
	}
	var (
		lists   [][]uint32
		textOps []textOp
	)

	for i := range t.Fields {
		f := &t.Fields[i]
		op, ok := ops[f.Data.Name]
		if !ok {
			continue
		}
		if f.CompactingStrategy == CompactingTokenized {
			textOps = append(textOps, textOp{f, op})
			continue
		}
		rows, ok := f.filterRows(op)
		if !ok {
			return nil, fmt.Errorf("search: no usable index on %q for this operation", f.Data.Name)
		}
		if len(rows) == 0 {
			return t.emptyView(depth)
		}
		lists = append(lists, rows)
	}

	for relationField, op := range relationOps {
		relation, fieldName, ok := strings.Cut(relationField, ".")
		if !ok {
			return nil, fmt.Errorf(
				"search: bad relation %q",
				relationField,
			)
		}

		f := t.fieldByName(relation)
		if f == nil {
			return nil, fmt.Errorf(
				"search: unknown relation %q",
				relation,
			)
		}

		if f.CompactingStrategy != CompactingIndexed {
			return nil, fmt.Errorf(
				"search: relation %q is not indexed",
				relation,
			)
		}

		relationField := t.fieldByName(relation)
		ext := t.external(relationField.Data.ExternalTableName)

		if ext == nil {
			return nil, fmt.Errorf(
				"search: external table %q not found",
				relation,
			)
		}

		externalRows, err := ext.Search(
			map[string]SearchOperation{
				fieldName: op,
			},
			nil,
			0,
			0,
		)
		if err != nil {
			return nil, err
		}

		rows := f.externalParentRows(externalRows)
		if len(rows) == 0 {
			return t.emptyView(depth)
		}

		lists = append(lists, rows)
	}
	tm.step("filters")
	for _, l := range lists {
		tm.notef("list=%d", len(l))
	}

	var cand []uint32
	if len(lists) > 0 {
		slices.SortFunc(lists, func(a, b []uint32) int { return cmp.Compare(len(a), len(b)) })
		cand = lists[0]
		for _, l := range lists[1:] {
			if cand = intersect(cand, l); len(cand) == 0 {
				return t.emptyView(depth)
			}
		}
	}

	tm.step("intersect")
	tm.notef("cand=%d", len(cand))

	var matches map[uint32]float64
	if op, ok := ops[queryKey]; ok {
		matches = t.searchAllText(op.Text, cand)
	}
	for _, to := range textOps {
		if matches != nil && len(matches) == 0 {
			break
		}
		h := to.f.textHits(to.op.Text, t.tokenizer(to.f.Data.Tokenizer), cand)
		matches = andMerge(matches, h)
	}

	tm.step("text")
	if matches != nil {
		tm.notef("matches=%d", len(matches))
	}

	var (
		ids    []uint32
		scores []float64
	)
	if matches != nil {
		type hit struct {
			row   uint32
			score float64
		}
		hs := make([]hit, 0, len(matches))
		for r, s := range matches {
			hs = append(hs, hit{r, s})
		}
		slices.SortFunc(hs, func(a, b hit) int {
			if c := cmp.Compare(b.score, a.score); c != 0 {
				return c
			}
			return cmp.Compare(a.row, b.row)
		})
		if limit > 0 && len(hs) > limit {
			hs = hs[:limit]
		}
		ids = make([]uint32, len(hs))
		scores = make([]float64, len(hs))
		for i, h := range hs {
			ids[i], scores[i] = h.row, h.score
		}
	} else {
		ids = cand
		if limit > 0 && len(ids) > limit {
			ids = ids[:limit]
		}
	}

	tm.step("rank")

	view, ok := t.GetManyByIndexes(ids, depth)
	tm.step("load")
	tm.notef("ids=%d", len(ids))
	if !ok {
		return nil, errors.New("search: failed to load rows")
	}
	view.Scores = scores
	return view, nil
}

func (t *CompactTable[K]) emptyView(depth int) (*DataView, error) {
	view, ok := t.GetManyByIndexes(nil, depth)
	if !ok {
		return nil, errors.New("search: failed to load rows")
	}
	return view, nil
}

func intersect(a, b []uint32) []uint32 {
	if len(a) > len(b) {
		a, b = b, a
	}
	out := make([]uint32, 0, len(a))
	for _, x := range a {
		i, found := slices.BinarySearch(b, x)
		if found {
			out = append(out, x)
		}
		if b = b[i:]; len(b) == 0 {
			break
		}
	}
	return out
}

func andMerge(a, b map[uint32]float64) map[uint32]float64 {
	if a == nil {
		return b
	}
	for r, s := range a {
		if h, ok := b[r]; ok {
			a[r] = s + h
		} else {
			delete(a, r)
		}
	}
	return a
}

func addScores(a, b map[uint32]float64) map[uint32]float64 {
	if a == nil {
		a = make(map[uint32]float64, len(b))
	}

	for r, s := range b {
		a[r] += s
	}

	return a
}

func (f *Field) filterRows(op SearchOperation) (rows []uint32, ok bool) {
	if f.Empty {
		return nil, true
	}
	if !op.isRange() {
		return nil, false
	}
	ix := &f.SearchIndex
	lo, hi := op.bounds()

	switch {
	case f.CompactingStrategy == CompactingEnumed && ix.Kind == SearchIndexInverted:
		var parts [][]uint32
		for code, v := range f.Helpers {
			if v >= lo && v <= hi {
				parts = append(parts, ix.Rows[ix.Offsets[code]:ix.Offsets[code+1]])
			}
		}
		switch len(parts) {
		case 0:
			return nil, true
		case 1:
			return parts[0], true
		}
		total := 0
		for _, p := range parts {
			total += len(p)
		}
		out := make([]uint32, 0, total)
		for _, p := range parts {
			out = append(out, p...)
		}
		slices.Sort(out)
		return out, true

	case ix.Kind == SearchIndexSorted:
		from := sort.Search(len(ix.Values), func(i int) bool { return ix.Values[i] >= lo })
		to := sort.Search(len(ix.Values), func(i int) bool { return ix.Values[i] > hi })
		out := slices.Clone(ix.Rows[from:to])
		slices.Sort(out)
		return out, true
	}
	return nil, false
}

func (f *Field) wordHits(word string, tk Tokenizer, cand []uint32) (map[uint32]float64, bool) {
	ix := &f.SearchIndex
	if ix.Kind != SearchIndexInverted {
		return nil, false
	}
	q := tk.Tokenize(word)
	if len(q) == 0 {
		return nil, false
	}
	bpt := int(f.Helpers[0])
	rec := bpt + 1

	var res map[uint32]float64
	for p := 0; p+rec <= len(q); p += rec {
		id := int(readToken(q[p:], bpt))
		if id+1 >= len(ix.Offsets) {
			return map[uint32]float64{}, true
		}
		full := ix.Rows[ix.Offsets[id]:ix.Offsets[id+1]]
		if len(full) == 0 {
			return map[uint32]float64{}, true
		}
		rows := full
		if cand != nil {
			rows = intersect(full, cand)
		}
		w := 1 / float64(len(full))
		cur := make(map[uint32]float64, len(rows))
		for _, r := range rows {
			cur[r] = w
		}
		if res = andMerge(res, cur); len(res) == 0 {
			break
		}
	}
	return res, true
}

func (f *Field) textHits(text string, tk Tokenizer, cand []uint32) map[uint32]float64 {
	var res map[uint32]float64
	for _, w := range strings.Fields(text) {
		if h, ok := f.wordHits(w, tk, cand); ok {
			res = addScores(res, h)
		}
	}
	if res == nil {
		res = map[uint32]float64{}
	}
	return res
}

func (t *CompactTable[K]) searchAllText(text string, cand []uint32) map[uint32]float64 {
	var res map[uint32]float64
	for _, w := range strings.Fields(text) {
		perWord := map[uint32]float64{}
		known := false
		for i := range t.Fields {
			f := &t.Fields[i]
			if !f.Data.WithSearchIndex || f.CompactingStrategy != CompactingTokenized || f.Empty {
				continue
			}
			h, ok := f.wordHits(w, t.tokenizer(f.Data.Tokenizer), cand)
			if !ok {
				continue
			}
			known = true
			for r, s := range h {
				perWord[r] += s
			}
		}
		if known {
			res = addScores(res, perWord)
		}
	}
	if res == nil {
		res = map[uint32]float64{}
	}
	return res
}
