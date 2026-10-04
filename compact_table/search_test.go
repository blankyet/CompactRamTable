package compact_table

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"
)

func TestBuildEnumSearchIndex(t *testing.T) {
	enc32 := func(v int32) []byte {
		return binary.BigEndian.AppendUint32(nil, uint32(v))
	}

	table := New[UUIDT]("test", 20, []FieldData{
		{
			Name:            "value",
			InitialType:     TypeInt32,
			WithSearchIndex: true,
			EnumNames:       []string{"unknown", "active", "finished"},
		},
	})

	values := []int32{
		10,
		20,
		10,
		30,
		0, // null*
		20,
		40,
		10,
		30,
		20,
	}

	for i, value := range values {
		data := map[string][][]byte{
			"value": {enc32(value)},
		}
		if i == 4 {
			delete(data, "value")
		}
		table.PutSingleRecord(testUUID(byte(i)), data)
	}

	table.Compact(nil)
	field := &table.Fields[0]

	assert(t, field.CompactingStrategy == CompactingEnumed,
		"strategy=%v", field.CompactingStrategy)
	assert(t, field.SearchIndex.Kind == SearchIndexInverted,
		"search kind=%v", field.SearchIndex.Kind)

	code := func(value int32) uint32 {
		bits := toBits(value)
		for i, v := range field.Helpers {
			if v == bits {
				return uint32(i)
			}
		}
		panic(fmt.Sprintf("value %d not found in Helpers", value))
	}

	assertRows := func(value int32, want ...uint32) {
		c := code(value)
		start := field.SearchIndex.Offsets[c]
		end := field.SearchIndex.Offsets[c+1]
		got := field.SearchIndex.Rows[start:end]
		assert(t, len(got) == len(want),
			"value=%d rows=%v want=%v", value, got, want)
		for i := range want {
			assert(t, got[i] == want[i],
				"value=%d rows=%v want=%v", value, got, want)
		}
	}

	assertRows(10, 0, 2, 7)
	assertRows(20, 1, 5, 9)
	assertRows(30, 3, 8)
	assertRows(40, 6)
}

func TestCompactTableBuildSortedSearchIndex(t *testing.T) {
	enc32 := func(v int32) []byte {
		return binary.BigEndian.AppendUint32(nil, uint32(v))
	}

	values := []int32{
		10,
		20,
		5,
		20,
		100,
		10,
		50,
		5,
		30,
		20,
	}

	table := New[UUIDT]("test", 20, []FieldData{
		{
			Name:            "value",
			InitialType:     TypeInt32,
			WithSearchIndex: true,
		},
	})

	for i, value := range values {
		table.PutSingleRecord(testUUID(byte(i)), map[string][][]byte{
			"value": {enc32(value)},
		})
	}

	table.Compact(nil)
	field := &table.Fields[0]

	assert(t, field.CompactingStrategy == CompactingScaled,
		"strategy=%v", field.CompactingStrategy)
	assert(t, field.SearchIndex.Kind == SearchIndexSorted,
		"search kind=%v", field.SearchIndex.Kind)

	wantValues := []uint64{5, 5, 10, 10, 20, 20, 20, 30, 50, 100}
	wantRows := []uint32{2, 7, 0, 5, 1, 3, 9, 8, 6, 4}

	assert(t, len(field.SearchIndex.Values) == len(wantValues),
		"values len=%d want=%d",
		len(field.SearchIndex.Values), len(wantValues))
	assert(t, len(field.SearchIndex.Rows) == len(wantRows),
		"rows len=%d want=%d",
		len(field.SearchIndex.Rows), len(wantRows))

	for i := range wantValues {
		assert(t, field.SearchIndex.Values[i] == wantValues[i],
			"value[%d]=%d want=%d",
			i, field.SearchIndex.Values[i], wantValues[i])
		assert(t, field.SearchIndex.Rows[i] == wantRows[i],
			"row[%d]=%d want=%d",
			i, field.SearchIndex.Rows[i], wantRows[i])
	}
}

func TestBuildTokenSearchIndex(t *testing.T) {
	table := New[UUIDT]("test", 10, []FieldData{
		{
			Name:            "text",
			InitialType:     TypeString,
			Tokenizer:       TokenizerStandardText,
			WithSearchIndex: true,
		},
	})

	values := []string{
		"foo bar",
		"bar baz",
		"foo baz",
		"foo foo bar",
		"qux",
	}

	for i, value := range values {
		table.PutSingleRecord(testUUID(byte(i)), map[string][][]byte{
			"text": {[]byte(value)},
		})
	}

	table.Compact(nil)
	field := &table.Fields[0]

	assert(t, field.CompactingStrategy == CompactingTokenized,
		"strategy=%v", field.CompactingStrategy)
	assert(t, field.SearchIndex.Kind == SearchIndexInverted,
		"search kind=%v", field.SearchIndex.Kind)

	assertRows := func(token string, want ...uint32) {
		encoded := table.WordTokenizer.Tokenize(token)
		if len(encoded) == 0 {
			t.Fatalf("token %q produced empty encoding", token)
		}
		bytesPerToken := int(field.Helpers[0])
		if len(encoded) < bytesPerToken {
			t.Fatalf("token %q: encoded=%x, bytesPerToken=%d", token, encoded, bytesPerToken)
		}
		id := readToken(encoded, bytesPerToken)
		start := field.SearchIndex.Offsets[id]
		end := field.SearchIndex.Offsets[id+1]
		got := field.SearchIndex.Rows[start:end]
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("token %q: got %v, want %v", token, got, want)
		}
	}

	assertRows("foo", 0, 2, 3)
	assertRows("bar", 0, 1, 3)
	assertRows("baz", 1, 2)
	assertRows("qux", 4)
}

func TestSearchIndexThreeFieldsExactRows(t *testing.T) {
	enc32 := func(v int32) []byte {
		return binary.BigEndian.AppendUint32(nil, uint32(v))
	}

	type record struct {
		value int32
		kind  string
		text  string
	}

	records := []record{
		{10, "active", "foo bar"},   // 0*
		{20, "active", "foo baz"},   // 1*
		{10, "finished", "foo bar"}, // 2*
		{20, "active", "bar baz"},   // 3*
		{30, "active", "foo bar"},   // 4*
		{10, "active", "bar foo"},   // 5*
		{20, "finished", "foo baz"}, // 6*
		{10, "active", "foo qux"},   // 7*
		{30, "finished", "foo bar"}, // 8*
		{20, "active", "foo bar"},   // 9*
	}

	table := New[UUIDT]("test", 20, []FieldData{
		{
			Name:            "value",
			InitialType:     TypeInt32,
			WithSearchIndex: true,
			EnumNames:       []string{"unknown", "active", "finished"},
		},
		{
			Name:            "kind",
			InitialType:     TypeString,
			Tokenizer:       TokenizerStandardText,
			WithSearchIndex: true,
		},
		{
			Name:            "text",
			InitialType:     TypeString,
			Tokenizer:       TokenizerStandardText,
			WithSearchIndex: true,
		},
	})

	for i, r := range records {
		table.PutSingleRecord(testUUID(byte(i)), map[string][][]byte{
			"value": {enc32(r.value)},
			"kind":  {[]byte(r.kind)},
			"text":  {[]byte(r.text)},
		})
	}

	table.Compact(nil)
	valueField := &table.Fields[0]
	kindField := &table.Fields[1]
	textField := &table.Fields[2]

	assert(t, valueField.SearchIndex.Kind == SearchIndexInverted,
		"value search kind=%v", valueField.SearchIndex.Kind)
	assert(t, kindField.SearchIndex.Kind == SearchIndexInverted,
		"kind search kind=%v", kindField.SearchIndex.Kind)
	assert(t, textField.SearchIndex.Kind == SearchIndexInverted,
		"text search kind=%v", textField.SearchIndex.Kind)

	// Return rows matching an enum value.*
	valueRows := func(value int32) []uint32 {
		bits := toBits(value)
		var code uint32
		for i, v := range valueField.Helpers {
			if v == bits {
				code = uint32(i)
				break
			}
		}
		start := valueField.SearchIndex.Offsets[code]
		end := valueField.SearchIndex.Offsets[code+1]
		return valueField.SearchIndex.Rows[start:end]
	}

	// Return rows matching an exact token.*
	tokenRows := func(field *Field, tokenizer Tokenizer, token string) []uint32 {
		encoded := tokenizer.Tokenize(token)
		if len(encoded) == 0 {
			t.Fatalf("token %q produced empty encoding", token)
		}
		bytesPerToken := int(field.Helpers[0])
		id := readToken(encoded, bytesPerToken)
		start := field.SearchIndex.Offsets[id]
		end := field.SearchIndex.Offsets[id+1]
		return field.SearchIndex.Rows[start:end]
	}

	kindRows := func(kind string) []uint32 {
		return tokenRows(kindField, table.WordTokenizer, kind)
	}

	textRows := func(token string) []uint32 {
		return tokenRows(textField, table.WordTokenizer, token)
	}

	// Intersect sorted row lists.*
	intersect := func(a, b []uint32) []uint32 {
		result := make([]uint32, 0)
		i, j := 0, 0
		for i < len(a) && j < len(b) {
			switch {
			case a[i] < b[j]:
				i++
			case a[i] > b[j]:
				j++
			default:
				result = append(result, a[i])
				i++
				j++
			}
		}
		return result
	}

	// All three conditions must match.*
	search := func(value int32, kind, token string) []uint32 {
		rows := valueRows(value)
		rows = intersect(rows, kindRows(kind))
		rows = intersect(rows, textRows(token))
		return rows
	}

	assertRows := func(name string, got []uint32, want ...uint32) {
		assert(t, len(got) == len(want),
			"%s: got %v, want %v", name, got, want)
		for i := range want {
			assert(t, got[i] == want[i],
				"%s: got %v, want %v", name, got, want)
		}
	}

	// value=10 AND kind=active AND text contains foo*
	// rows: 0, 5, 7*
	assertRows(
		"10 + active + foo",
		search(10, "active", "foo"),
		0, 5, 7,
	)

	// value=20 AND kind=active AND text contains foo*
	// rows: 1, 9*
	assertRows(
		"20 + active + foo",
		search(20, "active", "foo"),
		1, 9,
	)

	// value=30 AND kind=finished AND text contains foo*
	// row: 8*
	assertRows(
		"30 + finished + foo",
		search(30, "finished", "foo"),
		8,
	)

	// value=10 AND kind=finished AND text contains foo*
	// row: 2*
	assertRows(
		"10 + finished + foo",
		search(10, "finished", "foo"),
		2,
	)

	// No row satisfies all three.*
	assertRows(
		"20 + finished + bar",
		search(20, "finished", "bar"),
	)

	// Make sure individual indexes themselves contain the expected rows.*
	assertRows("value=10", valueRows(10), 0, 2, 5, 7)
	assertRows("kind=active", kindRows("active"),
		0, 1, 3, 4, 5, 7, 9)
	assertRows("text=bar", textRows("bar"),
		0, 2, 3, 4, 5, 8, 9)
}

func TestSearch(t *testing.T) {
	table := New[UUIDT]("test", 20, []FieldData{
		{Name: "value", InitialType: TypeInt32, WithSearchIndex: true},
		{
			Name:            "text",
			InitialType:     TypeString,
			Tokenizer:       TokenizerStandardText,
			WithSearchIndex: true,
		},
	})

	type record struct {
		value int32
		text  string
	}

	records := []record{
		{10, "foo bar"}, // 0*
		{20, "foo baz"}, // 1*
		{10, "foo bar"}, // 2*
		{20, "bar baz"}, // 3*
		{30, "foo bar"}, // 4*
		{10, "bar foo"}, // 5*
		{20, "foo baz"}, // 6*
		{10, "foo qux"}, // 7*
		{30, "foo bar"}, // 8*
		{20, "foo bar"}, // 9*
	}

	for i, r := range records {
		table.PutSingleRecord(testUUID(byte(i)), map[string][][]byte{
			"value": {be32(r.value)},
			"text":  {[]byte(r.text)},
		})
	}

	table.Compact(nil)

	valueColumn := func(view *DataView) []uint64 {
		return view.Columns[0].Values
	}

	t.Run("sorted equals", func(t *testing.T) {
		value := uint64(20)
		view, err := table.Search(map[string]SearchOperation{
			"value": {Equals: &value},
		}, nil, 0, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Count == 4, "count=%d", view.Count)
		got := valueColumn(view)
		assert(t, reflect.DeepEqual(got,
			[]uint64{20, 20, 20, 20}),
			"values=%v", got)
	})

	t.Run("sorted range", func(t *testing.T) {
		min, max := uint64(10), uint64(20)
		view, err := table.Search(map[string]SearchOperation{
			"value": {
				Min: &min,
				Max: &max,
			},
		}, nil, 0, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Count == 8, "count=%d", view.Count)
		got := valueColumn(view)
		assert(t, reflect.DeepEqual(got,
			[]uint64{10, 20, 10, 20, 10, 20, 10, 20}),
			"values=%v", got)
	})

	t.Run("text", func(t *testing.T) {
		view, err := table.Search(map[string]SearchOperation{
			"text": {Text: "bar"},
		}, nil, 0, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Count == 7, "count=%d", view.Count)
		assert(t, view.Scores != nil, "scores should be present")
		assert(t, len(view.Scores) == view.Count,
			"scores=%v", view.Scores)
	})

	t.Run("text ranking", func(t *testing.T) {
		view, err := table.Search(map[string]SearchOperation{
			"text": {Text: "foo bar"},
		}, nil, 0, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Scores != nil, "scores should be present")
		for i := 1; i < len(view.Scores); i++ {
			assert(t, view.Scores[i-1] >= view.Scores[i],
				"scores=%v", view.Scores)
		}
	})

	t.Run("AND sorted and text", func(t *testing.T) {
		value := uint64(20)
		view, err := table.Search(map[string]SearchOperation{
			"value": {Equals: &value},
			"text":  {Text: "bar"},
		}, nil, 0, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Count == 2, "count=%d", view.Count)
		got := valueColumn(view)
		assert(t, reflect.DeepEqual(got,
			[]uint64{20, 20}),
			"values=%v", got)
		assert(t, view.Scores != nil, "scores should be present")
	})

	t.Run("limit", func(t *testing.T) {
		value := uint64(20)
		view, err := table.Search(map[string]SearchOperation{
			"value": {Equals: &value},
		}, nil, 2, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Count == 2, "count=%d", view.Count)
	})

	t.Run("unknown field", func(t *testing.T) {
		value := uint64(10)
		view, err := table.Search(map[string]SearchOperation{
			"missing": {Equals: &value},
		}, nil, 0, 0)
		assert(t, err != nil, "expected search to fail")
		assert(t, view == nil, "view=%v", view)
	})
}

func TestSearchBooksAndMovies(t *testing.T) {
	table := New[UUIDT]("test", 20, []FieldData{
		{
			Name:            "title",
			InitialType:     TypeString,
			Tokenizer:       TokenizerStandardText,
			WithSearchIndex: true,
		},
		{
			Name:            "description",
			InitialType:     TypeString,
			Tokenizer:       TokenizerStandardText,
			WithSearchIndex: true,
		},
		{
			Name:            "year",
			InitialType:     TypeInt32,
			WithSearchIndex: true,
		},
		{
			Name:            "type",
			InitialType:     TypeString,
			Tokenizer:       TokenizerStandardText,
			WithSearchIndex: true,
		},
	})

	type record struct {
		title       string
		description string
		year        int32
		typ         string
	}

	records := []record{
		{
			"The Last Horizon",
			"A science fiction movie about a crew traveling beyond the known galaxy in search of a new home.",
			2024,
			"movie",
		},
		{
			"The Last Horizon",
			"A science fiction book following an explorer who discovers an ancient civilization beyond the stars.",
			2019,
			"book",
		},
		{
			"Winter Kingdom",
			"A historical drama about a young queen defending her kingdom during a long and brutal winter.",
			2021,
			"movie",
		},
		{
			"Winter Kingdom",
			"A fantasy book about kings, war, ancient magic, and a family fighting to reclaim their lost kingdom.",
			2018,
			"book",
		},
		{
			"Ocean Deep",
			"A documentary movie following marine biologists as they explore mysterious creatures living in the deepest ocean.",
			2023,
			"movie",
		},
		{
			"Ocean Deep",
			"A non-fiction book describing deep sea exploration, underwater technology, and the biology of marine life.",
			2020,
			"book",
		},
		{
			"City of Glass",
			"A detective movie about a journalist investigating a series of mysterious disappearances in a modern city.",
			2022,
			"movie",
		},
		{
			"City of Glass",
			"A detective novel about a private investigator following clues through the dark streets of a fictional city.",
			2017,
			"book",
		},
	}

	for i, r := range records {
		table.PutSingleRecord(testUUID(byte(i)), map[string][][]byte{
			"title":       {[]byte(r.title)},
			"description": {[]byte(r.description)},
			"year":        {be32(r.year)},
			"type":        {[]byte(r.typ)},
		})
	}

	table.Compact(nil)

	getTitles := func(view *DataView) []string {
		return view.Columns[0].Strings
	}

	t.Run("search title", func(t *testing.T) {
		view, err := table.Search(map[string]SearchOperation{
			"title": {Text: "Horizon"},
		}, nil, 0, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Count == 2, "count=%d", view.Count)
		assert(t, reflect.DeepEqual(getTitles(view),
			[]string{"The Last Horizon", "The Last Horizon"}),
			"titles=%v", getTitles(view))
	})

	t.Run("search description", func(t *testing.T) {
		view, err := table.Search(map[string]SearchOperation{
			"description": {Text: "ancient civilization"},
		}, nil, 0, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Count == 2, "count=%d", view.Count)
		assert(t, getTitles(view)[0] == "The Last Horizon",
			"title=%v", getTitles(view))
	})

	t.Run("movie type", func(t *testing.T) {
		view, err := table.Search(map[string]SearchOperation{
			"type": {Text: "movie"},
		}, nil, 0, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Count == 4, "count=%d", view.Count)
	})

	t.Run("book type", func(t *testing.T) {
		view, err := table.Search(map[string]SearchOperation{
			"type": {Text: "book"},
		}, nil, 0, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Count == 4, "count=%d", view.Count)
	})

	t.Run("movie after 2021", func(t *testing.T) {
		min := uint64(2022)
		view, err := table.Search(map[string]SearchOperation{
			"year": {
				Min: &min,
			},
			"type": {
				Text: "movie",
			},
		}, nil, 0, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Count == 3, "count=%d", view.Count)
		titles := getTitles(view)
		assert(t, reflect.DeepEqual(titles,
			[]string{
				"The Last Horizon",
				"Ocean Deep",
				"City of Glass",
			}),
			"titles=%v", titles)
	})

	t.Run("book before 2020", func(t *testing.T) {
		max := uint64(2019)
		view, err := table.Search(map[string]SearchOperation{
			"year": {
				Max: &max,
			},
			"type": {
				Text: "book",
			},
		}, nil, 0, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Count == 3, "count=%d", view.Count)
	})

	t.Run("movie about ocean", func(t *testing.T) {
		view, err := table.Search(map[string]SearchOperation{
			"description": {Text: "ocean."},
			"type":        {Text: "movie"},
		}, nil, 0, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Count == 1, "count=%d", view.Count)
		if view.Count == 1 {
			assert(t, getTitles(view)[0] == "Ocean Deep",
				"titles=%v", getTitles(view))
		}
	})

	t.Run("search across title and description", func(t *testing.T) {
		view, err := table.Search(map[string]SearchOperation{
			"title":       {Text: "Kingdom"},
			"description": {Text: "magic, war"},
		}, nil, 0, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Count == 1, "count=%d", view.Count)
		assert(t, getTitles(view)[0] == "Winter Kingdom",
			"titles=%v", getTitles(view))
		assert(t, view.Scores != nil, "scores should be present")
	})

	t.Run("search movie with year range", func(t *testing.T) {
		min, max := uint64(2021), uint64(2024)
		view, err := table.Search(map[string]SearchOperation{
			"year": {
				Min: &min,
				Max: &max,
			},
			"type": {
				Text: "movie",
			},
		}, nil, 0, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Count == 4, "count=%d", view.Count)
	})

	t.Run("limit", func(t *testing.T) {
		view, err := table.Search(map[string]SearchOperation{
			"description": {Text: "movie"},
		}, nil, 2, 0)
		assert(t, err == nil, "search failed: %v", err)
		if err != nil {
			return
		}
		assert(t, view.Count == 2, "count=%d", view.Count)
	})
}
