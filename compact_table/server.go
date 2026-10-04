package compact_table

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed frontend.html
var frontendHTML []byte

type Hint struct {
	Text  string `json:"text"`
	Kind  string `json:"kind"`            // "field" | "enum"
	Field string `json:"field,omitempty"` // owning field (for enums)
	Value *int   `json:"value,omitempty"` // enum ordinal
}

type FieldInfo struct {
	Name       string      `json:"name"`
	Type       string      `json:"type"`
	Tokenizer  string      `json:"tokenizer,omitempty"`
	Searchable bool        `json:"searchable"`
	IsList     bool        `json:"is_list,omitempty"`
	External   string      `json:"external,omitempty"`
	ExtTitle   string      `json:"ext_title,omitempty"`
	Format     string      `json:"format,omitempty"`
	Enums      []string    `json:"enums,omitempty"`
	Nested     []FieldInfo `json:"nested,omitempty"`
}

type ImageConfig struct {
	Field     string `json:"field"`
	Key       string `json:"key,omitempty"`
	URLPrefix string `json:"url_prefix,omitempty"`
}

type Display struct {
	Title       string       `json:"title,omitempty"`
	Subtitle    string       `json:"subtitle,omitempty"`
	Description string       `json:"description,omitempty"`
	Image       *ImageConfig `json:"image,omitempty"`
}

type TableDefs struct {
	Name         string      `json:"name"`
	Title        string      `json:"title"`
	ElementCount uint32      `json:"element_count"`
	Display      Display     `json:"display"`
	Fields       []FieldInfo `json:"fields"`
	Hints        []Hint      `json:"hints"`
}

type ServerOptions struct {
	Title            string
	TitleField       string
	SubtitleField    string
	DescriptionField string
	Image            *ImageConfig
	Formats          map[string]string
	DefaultLimit     int
	MaxLimit         int
	Extra            func(mux *http.ServeMux)
}

func newFieldInfo(name string, d *FieldData) FieldInfo {
	return FieldInfo{
		Name:       name,
		Type:       initialTypeName(d.InitialType),
		Tokenizer:  tokenizerName(d.Tokenizer),
		Searchable: d.WithSearchIndex,
		IsList:     d.IsList,
		External:   d.ExternalTableName,
		Enums:      d.EnumNames,
	}
}

func (t *CompactTable[K]) findField(names ...string) string {
	for _, n := range names {
		for i := range t.Fields {
			f := &t.Fields[i]
			if !f.Empty && strings.EqualFold(f.Data.Name, n) {
				return f.Data.Name
			}
		}
	}
	return ""
}

func (t *CompactTable[K]) titleField() string {
	if n := t.findField("name", "title", "label", "headline"); n != "" {
		return n
	}
	for i := range t.Fields {
		f := &t.Fields[i]
		if !f.Empty && f.Data.InitialType == TypeString && !f.Data.IsList && f.Data.ExternalTableName == "" {
			return f.Data.Name
		}
	}
	return ""
}

func (t *CompactTable[K]) display(o ServerOptions) Display {
	d := Display{
		Title:       o.TitleField,
		Subtitle:    o.SubtitleField,
		Description: o.DescriptionField,
		Image:       o.Image,
	}
	if d.Title == "" {
		d.Title = t.titleField()
	}
	if d.Subtitle == "" {
		d.Subtitle = t.findField("original_name", "original_title", "subtitle", "alias")
	}
	if d.Description == "" {
		d.Description = t.findField("description", "abstract", "summary", "overview", "text", "body")
	}
	if d.Subtitle == d.Title {
		d.Subtitle = ""
	}
	if d.Description == d.Title || d.Description == d.Subtitle {
		d.Description = ""
	}
	return d
}

func (t *CompactTable[K]) GetTableDefs() TableDefs {
	return t.getTableDefs(ServerOptions{})
}

func (t *CompactTable[K]) getTableDefs(o ServerOptions) TableDefs {
	title := o.Title
	if title == "" {
		title = t.Name
	}

	defs := TableDefs{
		Name:         t.Name,
		Title:        title,
		ElementCount: t.ElementCount,
		Display:      t.display(o),
		Fields:       make([]FieldInfo, 0, len(t.Fields)),
	}

	for i := range t.Fields {
		f := &t.Fields[i]
		if f.Empty {
			continue
		}
		d := &f.Data
		info := newFieldInfo(d.Name, d)
		info.Format = o.Formats[d.Name]

		if d.ExternalTableName != "" {
			if ext := t.external(d.ExternalTableName); ext != nil {
				info.ExtTitle = ext.titleField()

				for j := range ext.Fields {
					ef := &ext.Fields[j]
					if ef.Empty || !ef.Data.WithSearchIndex {
						continue
					}

					full := d.Name + "." + ef.Data.Name
					nested := newFieldInfo(full, &ef.Data)
					nested.Format = o.Formats[full]
					info.Nested = append(info.Nested, nested)

					defs.Hints = append(defs.Hints, Hint{Text: full, Kind: "field"})

					for ord, name := range ef.Data.EnumNames {
						ord := ord
						defs.Hints = append(defs.Hints, Hint{Text: name, Kind: "enum", Field: full, Value: &ord})
					}
				}
			}
		}

		defs.Fields = append(defs.Fields, info)
		defs.Hints = append(defs.Hints, Hint{Text: d.Name, Kind: "field"})

		for ord, name := range d.EnumNames {
			ord := ord
			defs.Hints = append(defs.Hints, Hint{Text: name, Kind: "enum", Field: d.Name, Value: &ord})
		}
	}

	// Stable order: fields first, then enums, alphabetical within each.
	sort.SliceStable(defs.Hints, func(a, b int) bool {
		ha, hb := defs.Hints[a], defs.Hints[b]
		if ha.Kind != hb.Kind {
			return ha.Kind == "field"
		}
		return strings.ToLower(ha.Text) < strings.ToLower(hb.Text)
	})

	return defs
}

func initialTypeName(t InitialTypeEnum) string {
	switch t {
	case TypeString:
		return "string"
	case TypeBool:
		return "bool"
	case TypeChar:
		return "char"
	case TypeInt32:
		return "int32"
	case TypeInt64:
		return "int64"
	case TypeUInt32:
		return "uint32"
	case TypeUInt64:
		return "uint64"
	case TypeFloat:
		return "float"
	case TypeDouble:
		return "double"
	case TypeIndex:
		return "index"
	case TypeExternalIndex:
		return "external_index"
	}

	return "unknown"
}

func tokenizerName(t TokenizerTypeEnum) string {
	switch t {
	case TokenizerStandardText:
		return "text"
	case TokenizerAliases:
		return "aliases"
	case TokenizerUrls:
		return "urls"
	}

	return ""
}

type SearchResponse struct {
	Count   int       `json:"count"`
	Rows    []uint32  `json:"rows"`
	Keys    []string  `json:"keys"`
	Scores  []float64 `json:"scores"`
	Results []DataRow `json:"results"`
}

type DataRow map[string]any

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func intParam(r *http.Request, name string, def, min, max int) int {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < min {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func (t *CompactTable[K]) Handler(opts ServerOptions) http.Handler {
	if opts.DefaultLimit <= 0 {
		opts.DefaultLimit = 20
	}
	if opts.MaxLimit <= 0 {
		opts.MaxLimit = 500
	}

	defs := t.getTableDefs(opts)
	mux := http.NewServeMux()

	mux.HandleFunc("/api/defs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		writeJSON(w, http.StatusOK, defs)
	})

	mux.HandleFunc("/api/search", func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if query == "" {
			writeJSON(w, http.StatusOK, SearchResponse{})
			return
		}

		limit := intParam(r, "limit", opts.DefaultLimit, 1, opts.MaxLimit)
		depth := intParam(r, "depth", 0, 0, 3)

		view, err := t.SearchQuery(query, limit, depth)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		keys := make([]string, len(view.Rows))
		results := make([]DataRow, len(view.Rows))

		for i, row := range view.Rows {
			keys[i] = fmt.Sprint(t.IndexToKey[row])
			results[i] = t.Row(view, i)
		}

		writeJSON(w, http.StatusOK, SearchResponse{
			Count:   view.Count,
			Rows:    view.Rows,
			Keys:    keys,
			Scores:  view.Scores,
			Results: results,
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(frontendHTML)
	})

	if opts.Extra != nil {
		opts.Extra(mux)
	}

	return mux
}

func (t *CompactTable[K]) Server(addr string) error {
	return t.ServerWithOptions(addr, ServerOptions{})
}

func (t *CompactTable[K]) ServerWithOptions(addr string, opts ServerOptions) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           t.Handler(opts),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.ListenAndServe()
}
