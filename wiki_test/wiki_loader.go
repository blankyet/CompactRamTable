package main

import (
	"bufio"
	"compress/bzip2"
	"encoding/binary"
	"encoding/xml"
	"flag"
	"fmt"
	"hash/fnv"
	"html"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	ct "github.com/blankyet/CompactRamTable/compact_table"
)

const (
	tableName = "wiki"
	numParas  = 5
)

type Key uint64

func (Key) FromBytes(b []byte) (Key, bool) {
	if len(b) != 8 {
		return 0, false
	}
	return Key(binary.BigEndian.Uint64(b)), true
}

func (k Key) ToBytes() ([]byte, bool) { return binary.BigEndian.AppendUint64(nil, uint64(k)), true }

func keyOf(s string) Key {
	h := fnv.New64a()
	h.Write([]byte(s))
	return Key(h.Sum64())
}

func encKey(s string) []byte { b, _ := keyOf(s).ToBytes(); return b }

func encU32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func encI32(v int32) []byte  { return encU32(uint32(v)) }
func encI64(v int64) []byte  { return binary.BigEndian.AppendUint64(nil, uint64(v)) }

func strField(name string) ct.FieldData {
	return ct.FieldData{Name: name, InitialType: ct.TypeString, Tokenizer: ct.TokenizerStandardText}
}

func strFieldIdx(name string) ct.FieldData {
	f := strField(name)
	f.WithSearchIndex = true
	return f
}

type options struct {
	dump, store, dumpText string
	limit                 int
	maxLinks, maxRest     int
	redirects             bool
	nsSet                 map[int]bool
}

func (o *options) allowNS(ns int) bool { return o.nsSet == nil || o.nsSet[ns] }

func parseNS(s string) map[int]bool {
	if s == "" || s == "all" {
		return nil
	}
	set := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			log.Fatalf("-ns: %q не число", part)
		}
		set[n] = true
	}
	return set
}

type xmlPage struct {
	Title    string `xml:"title"`
	NS       int    `xml:"ns"`
	ID       uint32 `xml:"id"`
	Redirect *struct {
		Title string `xml:"title,attr"`
	} `xml:"redirect"`
	Revision struct {
		Timestamp string `xml:"timestamp"`
		Text      string `xml:"text"`
	} `xml:"revision"`
}

type xmlNamespace struct {
	Key  int    `xml:"key,attr"`
	Name string `xml:",chardata"`
}

type record struct {
	id        uint32
	ns        int
	title     string
	ts        int64
	textLen   uint32
	redirect  string
	infoKind  string
	info      map[string]string
	cats      []string
	links     []string
	paras     [numParas]string
	rest      string
	members   uint32
	synthetic bool
}

type infoSpec struct {
	aliases  []string
	params   [][2]string
	yearFrom string
}

var infoboxSpecs = map[string]infoSpec{
	"film": {[]string{"film", "фильм"}, [][2]string{
		{"director", "director"}, {"режиссёр", "director"}, {"режиссер", "director"},
		{"producer", "producer"}, {"продюсер", "producer"},
		{"writer", "writer"}, {"автор_сценария", "writer"}, {"сценарист", "writer"},
		{"starring", "starring"}, {"в_главных_ролях", "starring"},
		{"music", "music"}, {"композитор", "music"},
		{"distributor", "distributor"}, {"студия", "distributor"},
		{"released", "released"}, {"год", "released"},
		{"runtime", "runtime"}, {"длительность", "runtime"},
		{"country", "country"}, {"страна", "country"},
		{"language", "language"}, {"язык", "language"},
		{"budget", "budget"}, {"бюджет", "budget"},
		{"gross", "gross"}, {"сборы", "gross"},
		{"genre", "genre"}, {"жанр", "genre"},
	}, "released"},
	"musician": {[]string{"musical artist", "музыкант", "музыкальный коллектив", "музыкальный исполнитель"}, [][2]string{
		{"background", "background"},
		{"genre", "genre"}, {"жанры", "genre"}, {"жанр", "genre"},
		{"occupation", "occupation"}, {"профессии", "occupation"}, {"профессия", "occupation"},
		{"instrument", "instrument"}, {"инструменты", "instrument"},
		{"years_active", "years_active"}, {"годы_активности", "years_active"},
		{"label", "label"}, {"лейбл", "label"}, {"лейблы", "label"},
		{"origin", "origin"}, {"страна", "origin"}, {"город", "origin"},
	}, "years_active"},
	"settlement": {[]string{"settlement", "населённый пункт", "населенный пункт", "нп"}, [][2]string{
		{"subdivision_name", "country"}, {"страна", "country"},
		{"subdivision_name1", "region"}, {"регион", "region"},
		{"population_total", "population"}, {"население", "population"},
		{"area_total_km2", "area_km2"}, {"площадь", "area_km2"},
		{"elevation_m", "elevation_m"}, {"высота_центра_нп", "elevation_m"},
		{"timezone", "timezone"}, {"часовой_пояс", "timezone"},
		{"coordinates", "coordinates"},
	}, ""},
	"album": {[]string{"album", "музыкальный альбом"}, [][2]string{
		{"artist", "artist"}, {"исполнитель", "artist"},
		{"released", "released"}, {"дата_выпуска", "released"}, {"год", "released"},
		{"recorded", "recorded"}, {"дата_записи", "recorded"},
		{"genre", "genre"}, {"жанр", "genre"},
		{"length", "length"}, {"длительность", "length"},
		{"label", "label"}, {"лейбл", "label"},
		{"producer", "producer"}, {"продюсер", "producer"},
	}, "released"},
}

var (
	aliasToKind = map[string]string{}
	srcToField  = map[string]map[string]string{}
)

func init() {
	for kind, sp := range infoboxSpecs {
		for _, a := range sp.aliases {
			aliasToKind[a] = kind
		}
		m := map[string]string{}
		for _, p := range sp.params {
			if _, dup := m[p[0]]; !dup {
				m[p[0]] = p[1]
			}
		}
		srcToField[kind] = m
	}
}

func (sp infoSpec) fieldNames() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, p := range sp.params {
		if _, ok := seen[p[1]]; !ok {
			seen[p[1]] = struct{}{}
			out = append(out, p[1])
		}
	}
	return out
}

func sortedKinds() []string {
	kinds := make([]string, 0, len(infoboxSpecs))
	for k := range infoboxSpecs {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

func normTplName(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(s, "_", " ")), " "))
	s = strings.TrimPrefix(s, "infobox ")
	return strings.TrimPrefix(s, "карточка ")
}

var (
	reComment     = regexp.MustCompile(`(?s)<!--.*?-->`)
	reRef         = regexp.MustCompile(`(?s)<ref[^>]*/>|<ref[^>]*>.*?</ref>`)
	reTable       = regexp.MustCompile(`(?s)\{\|.*?\|\}`)
	reLinkLbl     = regexp.MustCompile(`\[\[(?:[^\]|]*\|)?([^\]]*)\]\]`)
	reLinkTo      = regexp.MustCompile(`\[\[([^\]|#\[]+)`)
	reExtLink     = regexp.MustCompile(`\[(?:https?:)?//[^\s\]]+\s*([^\]]*)\]`)
	reTag         = regexp.MustCompile(`<[^>]+>`)
	reQuotes      = regexp.MustCompile(`'{2,}`)
	reYear        = regexp.MustCompile(`\b(1[0-9]{3}|20[0-9]{2})\b`)
	reTplInner    = regexp.MustCompile(`\{\{[^{}]*\}\}`)
	reTplName     = regexp.MustCompile(`\{\{\s*([^\n|{}<]+)`)
	reEmptyParens = regexp.MustCompile(`\s*\([\s,;]*\)`)
	reHeading     = regexp.MustCompile(`(?m)^=+[ \t]*(.*?)[ \t]*=+[ \t]*$`)
	reListMark    = regexp.MustCompile(`(?m)^[*#:;]+[ \t]*`)
)

func normTitle(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "_", " "))
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func stripTemplates(s string, keepArgs bool) string {
	for i := 0; i < 8 && strings.Contains(s, "{{"); i++ {
		s = reTplInner.ReplaceAllStringFunc(s, func(m string) string {
			if !keepArgs {
				return ""
			}
			parts := strings.Split(m[2:len(m)-2], "|")
			if len(parts) < 2 {
				return ""
			}
			return strings.Join(parts[1:], ", ")
		})
	}
	return s
}

func cleanValue(s string) string {
	s = reComment.ReplaceAllString(s, "")
	s = reRef.ReplaceAllString(s, "")
	s = stripTemplates(s, true)
	s = reLinkLbl.ReplaceAllString(s, "$1")
	s = reTag.ReplaceAllString(s, " ")
	s = reQuotes.ReplaceAllString(s, "")
	s = strings.NewReplacer("\n", " ", "*", "").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func findEnd(s string, start int) int {
	depth := 0
	for i := start; i+1 < len(s); i++ {
		if s[i] == '{' && s[i+1] == '{' {
			depth++
			i++
		} else if s[i] == '}' && s[i+1] == '}' {
			depth--
			i++
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

func splitTop(s string) []string {
	var parts []string
	depth, last := 0, 0
	for i := 0; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "{{"), strings.HasPrefix(s[i:], "[["):
			depth++
			i++
		case strings.HasPrefix(s[i:], "}}"), strings.HasPrefix(s[i:], "]]"):
			depth--
			i++
		case s[i] == '|' && depth == 0:
			parts = append(parts, s[last:i])
			last = i + 1
		}
	}
	return append(parts, s[last:])
}

type parser struct {
	catPrefix  string
	maxLinks   int
	maxRest    int
	reCat      *regexp.Regexp
	reCatStrip *regexp.Regexp
	reFile     *regexp.Regexp
}

func newParser(ns map[int]string, o *options) *parser {
	cat, file := ns[14], ns[6]
	if cat == "" {
		cat = "Category"
	}
	if file == "" {
		file = "File"
	}
	catAlt := regexp.QuoteMeta(cat) + "|category"
	fileAlt := regexp.QuoteMeta(file) + "|file|image"

	return &parser{
		catPrefix:  cat,
		maxLinks:   o.maxLinks,
		maxRest:    o.maxRest,
		reCat:      regexp.MustCompile(`\[\[\s*(?i:` + catAlt + `)\s*:\s*([^\]|]+)`),
		reCatStrip: regexp.MustCompile(`\[\[\s*(?i:` + catAlt + `)\s*:[^\]]*\]\]`),
		reFile:     regexp.MustCompile(`\[\[\s*(?i:` + fileAlt + `)\s*:(?:[^\[\]]|\[\[[^\]]*\]\])*\]\]`),
	}
}

func (p *parser) catTitle(name string) string { return p.catPrefix + ":" + normTitle(name) }

func parseTS(s string) int64 {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix()
	}
	return 0
}

func redirectTarget(x *xmlPage) string {
	t := x.Redirect.Title
	if i := strings.IndexByte(t, '#'); i >= 0 {
		t = t[:i]
	}
	return normTitle(t)
}

func (p *parser) redirectRecord(x *xmlPage) *record {
	return &record{
		id: x.ID, ns: x.NS, title: normTitle(x.Title),
		ts: parseTS(x.Revision.Timestamp), textLen: uint32(len(x.Revision.Text)),
		redirect: redirectTarget(x),
	}
}

func (p *parser) stripMarkup(s string) string {
	s = reComment.ReplaceAllString(s, "")
	s = reRef.ReplaceAllString(s, "")
	s = reTable.ReplaceAllString(s, "")
	s = stripTemplates(s, false)
	s = p.reFile.ReplaceAllString(s, "")
	s = p.reCatStrip.ReplaceAllString(s, "")
	return s
}

func toPlain(s string) string {
	s = reLinkLbl.ReplaceAllString(s, "$1")
	s = reExtLink.ReplaceAllString(s, "$1")
	s = reTag.ReplaceAllString(s, "")
	s = reQuotes.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\u0301", "")
	s = reEmptyParens.ReplaceAllString(s, "")
	return s
}

func plainLines(s string) []string {
	s = reHeading.ReplaceAllString(s, "$1")
	s = reListMark.ReplaceAllString(s, "")
	out := make([]string, 0, 16)
	for _, line := range strings.Split(s, "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func extractInfobox(lead string, rec *record) string {
	for _, loc := range reTplName.FindAllStringSubmatchIndex(lead, -1) {
		kind, ok := aliasToKind[normTplName(lead[loc[2]:loc[3]])]
		if !ok {
			continue
		}
		end := findEnd(lead, loc[0])
		if end < 0 {
			continue
		}
		rec.infoKind = kind
		rec.info = map[string]string{}
		fields := srcToField[kind]
		parts := splitTop(lead[loc[0]+2 : end-2])
		for _, part := range parts[1:] {
			k, v, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			k = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(k)), " ", "_")
			field, ok := fields[k]
			if !ok {
				continue
			}
			col := kind + "_" + field
			if _, set := rec.info[col]; set {
				continue
			}
			if v = cleanValue(v); v != "" {
				rec.info[col] = truncate(v, 300)
			}
		}
		return lead[:loc[0]] + lead[end:]
	}
	return lead
}

func (p *parser) parsePage(x *xmlPage) *record {
	text := x.Revision.Text
	rec := &record{
		id: x.ID, ns: x.NS, title: normTitle(x.Title),
		ts: parseTS(x.Revision.Timestamp), textLen: uint32(len(text)),
	}

	seen := map[string]struct{}{}
	for _, m := range p.reCat.FindAllStringSubmatch(text, -1) {
		c := p.catTitle(m[1])
		if _, dup := seen[c]; !dup {
			seen[c] = struct{}{}
			rec.cats = append(rec.cats, c)
		}
	}

	lead, tail := text, ""
	if i := strings.Index(text, "\n=="); i >= 0 {
		lead, tail = text[:i], text[i:]
	}

	lead = extractInfobox(lead, rec)
	lead = p.stripMarkup(lead)
	tail = p.stripMarkup(tail)

	linkSeen := map[string]struct{}{}
	for _, m := range reLinkTo.FindAllStringSubmatch(lead, -1) {
		if strings.Contains(m[1], ":") {
			continue // File:, Category:, wikt: ...
		}
		l := normTitle(m[1])
		if l == "" || l == rec.title {
			continue
		}
		if _, dup := linkSeen[l]; !dup {
			linkSeen[l] = struct{}{}
			rec.links = append(rec.links, l)
		}
	}
	if len(rec.links) > p.maxLinks {
		rec.links = rec.links[:p.maxLinks]
	}

	lines := plainLines(toPlain(lead) + "\n" + toPlain(tail))
	for i := 0; i < numParas && i < len(lines); i++ {
		rec.paras[i] = lines[i]
	}
	if len(lines) > numParas {

		rest := strings.Join(lines[numParas:], " ¶ ")
		if p.maxRest > 0 {
			rest = truncate(rest, p.maxRest)
		}
		rec.rest = rest
	}
	return rec
}

type countingReader struct {
	r io.Reader
	n *int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

type buildStats struct {
	bz2Bytes, xmlBytes int64
	rows               int
	articles           int
	redirects          int
	catReal, catSynth  int
	otherNS            int
	infobox            map[string]int
	rawByCol           map[string]int64
}

func openDump(path string, st *buildStats) io.Reader {
	var src io.Reader = os.Stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			log.Fatal(err)
		}
		if fi, err := f.Stat(); err == nil {
			st.bz2Bytes = fi.Size()
		}
		src = f
	}

	var r io.Reader = bufio.NewReaderSize(src, 1<<20)
	if strings.HasSuffix(path, ".bz2") {
		r = bzip2.NewReader(r)
	}
	return bufio.NewReaderSize(&countingReader{r: r, n: &st.xmlBytes}, 1<<20)
}

func readDump(r io.Reader, o *options) ([]*record, map[string]string) {
	dec := xml.NewDecoder(r)
	nsNames := map[int]string{}
	redirectOf := map[string]string{}

	var (
		p            *parser
		jobs         chan *xmlPage
		out          chan *record
		wg           sync.WaitGroup
		done         = make(chan struct{})
		recs         []*record
		redirectRecs []*record
		pages, sent  int
		start        = time.Now()
	)

	startPool := func() {
		p = newParser(nsNames, o)
		workers := runtime.NumCPU()
		jobs = make(chan *xmlPage, 4*workers)
		out = make(chan *record, 4*workers)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for x := range jobs {
					out <- p.parsePage(x)
				}
			}()
		}
		go func() {
			for rec := range out {
				recs = append(recs, rec)
			}
			close(done)
		}()
	}

loop:
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}

		switch se.Name.Local {
		case "namespace":
			var ns xmlNamespace
			if err := dec.DecodeElement(&ns, &se); err == nil {
				nsNames[ns.Key] = strings.TrimSpace(ns.Name)
			}

		case "page":
			var x xmlPage
			if err := dec.DecodeElement(&x, &se); err != nil {
				log.Fatal(err)
			}
			if p == nil {
				startPool()
			}
			pages++
			if pages%100000 == 0 {
				log.Printf("pages=%d sent=%d redirects=%d elapsed=%s",
					pages, sent, len(redirectOf), time.Since(start).Round(time.Second))
			}
			if !o.allowNS(x.NS) {
				continue
			}
			if x.Redirect != nil {
				redirectOf[normTitle(x.Title)] = redirectTarget(&x)
				if o.redirects {
					redirectRecs = append(redirectRecs, p.redirectRecord(&x))
				}
				continue
			}
			if o.limit > 0 && sent >= o.limit {
				break loop
			}
			sent++
			jobs <- &x
		}
	}

	if jobs != nil {
		close(jobs)
		wg.Wait()
		close(out)
		<-done
	}
	log.Printf("дамп прочитан: страниц=%d, не-редиректов=%d, редиректов=%d, пространства имён: %v",
		pages, sent, len(redirectOf), nsNames)
	return append(recs, redirectRecs...), redirectOf
}

var paragraphCols = func() []string {
	c := make([]string, numParas)
	for i := range c {
		c[i] = fmt.Sprintf("paragraph_%d", i+1)
	}
	return c
}()

func buildFields() []ct.FieldData {
	f := []ct.FieldData{
		{Name: "id", InitialType: ct.TypeUInt32},
		{Name: "ns", InitialType: ct.TypeInt32},
		strFieldIdx("title"),
		{Name: "timestamp", InitialType: ct.TypeInt64},
		{Name: "text_len", InitialType: ct.TypeUInt32},
		{Name: "redirect_to", InitialType: ct.TypeExternalIndex, ExternalTableName: tableName},
		strField("infobox"),
	}
	for _, c := range paragraphCols {
		f = append(f, strField(c))
	}
	f = append(f,
		strField("text_rest"),
		ct.FieldData{Name: "categories", InitialType: ct.TypeExternalIndex, ExternalTableName: tableName, IsList: true},
		ct.FieldData{Name: "links", InitialType: ct.TypeExternalIndex, ExternalTableName: tableName, IsList: true},
		ct.FieldData{Name: "members", InitialType: ct.TypeUInt32},
	)
	for _, kind := range sortedKinds() {
		spec := infoboxSpecs[kind]
		for _, name := range spec.fieldNames() {
			f = append(f, strField(kind+"_"+name))
		}
		if spec.yearFrom != "" {
			f = append(f, ct.FieldData{Name: kind + "_year", InitialType: ct.TypeInt32})
		}
	}
	return f
}

func buildAll(o *options) (*ct.CompactTable[Key], *buildStats) {
	st := &buildStats{infobox: map[string]int{}, rawByCol: map[string]int64{}}

	recs, redirectOf := readDump(openDump(o.dump, st), o)

	titles := make(map[string]struct{}, len(recs))
	for _, r := range recs {
		titles[r.title] = struct{}{}
	}
	catCount := map[string]uint32{}
	for _, r := range recs {
		for _, c := range r.cats {
			catCount[c]++
		}
	}
	var synth []string
	for c := range catCount {
		if _, ok := titles[c]; !ok {
			synth = append(synth, c)
		}
	}
	sort.Strings(synth)
	for _, c := range synth {
		recs = append(recs, &record{ns: 14, title: c, synthetic: true})
		titles[c] = struct{}{}
	}
	for _, r := range recs {
		if r.ns == 14 {
			r.members = catCount[r.title]
		}
	}

	resolve := func(t string) string {
		for hop := 0; hop < 3; hop++ {
			next, ok := redirectOf[t]
			if !ok {
				break
			}
			t = next
		}
		return t
	}
	var linksBefore, linksAfter int
	for _, r := range recs {
		kept := r.links[:0]
		seen := map[string]struct{}{}
		for _, l := range r.links {
			linksBefore++
			l = resolve(l)
			if _, ok := titles[l]; !ok || l == r.title {
				continue
			}
			if _, dup := seen[l]; dup {
				continue
			}
			seen[l] = struct{}{}
			kept = append(kept, l)
		}
		r.links = kept
		linksAfter += len(kept)
	}
	log.Printf("ссылки из lead: %d -> %d после резолва", linksBefore, linksAfter)

	fields := buildFields()
	rows := len(recs)
	log.Printf("строк: %d, колонок: %d; заголовки OriginalData на время сборки ≈ %d MB (колонки × строки × 24 B)",
		rows, len(fields), int64(len(fields))*int64(rows)*24>>20)

	t := ct.New[Key](tableName, uint32(rows), fields)

	var dumpText *bufio.Writer
	if o.dumpText != "" {
		f, err := os.Create(o.dumpText)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		dumpText = bufio.NewWriterSize(f, 1<<20)
		defer dumpText.Flush()
	}

	for i, r := range recs {
		putRow(t, r, titles, resolve, st)
		if dumpText != nil {
			for _, p := range r.paras {
				if p != "" {
					dumpText.WriteString(p)
					dumpText.WriteByte('\n')
				}
			}
			if r.rest != "" {
				dumpText.WriteString(strings.ReplaceAll(r.rest, " ¶ ", "\n"))
				dumpText.WriteByte('\n')
			}
			dumpText.WriteByte('\n')
		}
		recs[i] = nil
	}
	recs = nil

	if len(t.KeyToIndex) != int(t.ElementCount) {
		log.Printf("ВНИМАНИЕ: коллизии ключей (%d строк, %d ключей)", t.ElementCount, len(t.KeyToIndex))
	}

	start := time.Now()
	t.Compact(nil)
	log.Printf("compact %s: n=%d  %s", tableName, t.ElementCount, time.Since(start).Round(time.Millisecond))

	return t, st
}

func putRow(t *ct.CompactTable[Key], r *record, titles map[string]struct{},
	resolve func(string) string, st *buildStats) {

	d := make(map[string][][]byte, 24)

	str := func(col, v string) {
		if v == "" {
			return
		}
		d[col] = [][]byte{[]byte(v)}
		st.rawByCol[col] += int64(len(v))
	}

	if !r.synthetic {
		d["id"] = [][]byte{encU32(r.id)}
		d["timestamp"] = [][]byte{encI64(r.ts)}
		d["text_len"] = [][]byte{encU32(r.textLen)}
	}
	d["ns"] = [][]byte{encI32(int32(r.ns))}
	str("title", r.title)

	if r.redirect != "" {
		if target := resolve(r.redirect); target != r.title {
			if _, ok := titles[target]; ok {
				d["redirect_to"] = [][]byte{encKey(target)}
			}
		}
	}

	str("infobox", r.infoKind)
	for i, p := range r.paras {
		str(paragraphCols[i], p)
	}
	str("text_rest", r.rest)

	for _, c := range r.cats {
		d["categories"] = append(d["categories"], encKey(c))
	}
	for _, l := range r.links {
		d["links"] = append(d["links"], encKey(l))
	}
	if r.ns == 14 {
		d["members"] = [][]byte{encU32(r.members)}
	}

	for col, v := range r.info {
		str(col, v)
	}
	if r.infoKind != "" {
		if spec := infoboxSpecs[r.infoKind]; spec.yearFrom != "" {
			if y := reYear.FindString(r.info[r.infoKind+"_"+spec.yearFrom]); y != "" {
				n, _ := strconv.Atoi(y)
				d[r.infoKind+"_year"] = [][]byte{encI32(int32(n))}
			}
		}
		st.infobox[r.infoKind]++
	}

	switch {
	case r.synthetic:
		st.catSynth++
	case r.redirect != "":
		st.redirects++
	case r.ns == 0:
		st.articles++
	case r.ns == 14:
		st.catReal++
	default:
		st.otherNS++
	}
	st.rows++

	t.PutSingleRecord(keyOf(r.title), d)
}

func saveTable(dir string, t *ct.CompactTable[Key]) int64 {
	if dir == "" {
		return 0
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	start := time.Now()
	data := t.Serialize(true, false)
	if err := os.WriteFile(filepath.Join(dir, tableName+".bin"), data, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("saved %s: %d MB  %s", tableName, len(data)>>20, time.Since(start).Round(time.Millisecond))
	return int64(len(data))
}

func loadTable(dir string) (*ct.CompactTable[Key], bool) {
	if dir == "" {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(dir, tableName+".bin"))
	if err != nil {
		return nil, false
	}
	start := time.Now()
	t := ct.DeserializeCompactTable[Key](data, tableName)
	log.Printf("loaded %s: n=%d, %d MB  %s", tableName, t.ElementCount, len(data)>>20, time.Since(start).Round(time.Millisecond))
	return t, true
}

func mb(n int64) float64 { return float64(n) / (1 << 20) }

func printReport(st *buildStats, serialized int64) {
	fmt.Println("\n=========== ОТЧЁТ ===========")
	fmt.Printf("строк всего: %d (статьи ns=0: %d, редиректы: %d, категории: %d + синтетических %d, прочие ns: %d)\n",
		st.rows, st.articles, st.redirects, st.catReal, st.catSynth, st.otherNS)

	kinds := make([]string, 0, len(st.infobox))
	for k := range st.infobox {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Printf("  infobox %-11s %d\n", k, st.infobox[k])
	}

	var raw int64
	cols := make([]string, 0, len(st.rawByCol))
	for c, n := range st.rawByCol {
		raw += n
		cols = append(cols, c)
	}
	sort.Slice(cols, func(i, j int) bool { return st.rawByCol[cols[i]] > st.rawByCol[cols[j]] })

	fmt.Println("\nсырой размер строковых колонок (до токенизации), топ:")
	for i, c := range cols {
		if i >= 12 {
			break
		}
		fmt.Printf("  %-22s %9.1f MB\n", c, mb(st.rawByCol[c]))
	}

	fmt.Println("\nразмеры:")
	if st.bz2Bytes > 0 {
		fmt.Printf("  дамп .bz2                  %9.1f MB\n", mb(st.bz2Bytes))
	}
	fmt.Printf("  XML распакованный          %9.1f MB\n", mb(st.xmlBytes))
	fmt.Printf("  очищенный текст (raw)      %9.1f MB\n", mb(raw))
	if serialized > 0 {
		fmt.Printf("  сериализованная таблица    %9.1f MB\n", mb(serialized))
		fmt.Println("\nсжатие (во сколько раз меньше):")
		if st.bz2Bytes > 0 {
			fmt.Printf("  относительно .bz2:         x%.2f\n", float64(st.bz2Bytes)/float64(serialized))
		}
		fmt.Printf("  относительно XML:          x%.2f\n", float64(st.xmlBytes)/float64(serialized))
		fmt.Printf("  относительно чистого текста: x%.2f\n", float64(raw)/float64(serialized))
	}
	fmt.Println("(пофайловые размеры колонок — в строках [Serialize] выше; для честного сравнения с")
	fmt.Println(" универсальными архиваторами запусти с -dumptext и сожми файл xz/zstd/bzip2)")
}

func short(v any) string {
	s := fmt.Sprint(v)
	if n := utf8.RuneCountInString(s); n > 300 {
		return string([]rune(s)[:300]) + fmt.Sprintf("… (%d симв.)", n)
	}
	return s
}

func printRow(m map[string]any) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%s: %s\n", k, short(m[k]))
	}
}

func printSamples(t *ct.CompactTable[Key], n int) {
	titleCol, textCol := -1, -1
	for i := range t.Fields {
		switch t.Fields[i].Data.Name {
		case "title":
			titleCol = i
		case "paragraph_1":
			textCol = i
		}
	}
	if titleCol < 0 || t.Fields[titleCol].Empty || t.ElementCount == 0 {
		return
	}

	probe := min(int(t.ElementCount), 4000)
	ids := make([]uint32, probe)
	for i := range ids {
		ids[i] = uint32(i * int(t.ElementCount) / probe)
	}
	view, ok := t.GetManyByIndexes(ids, 0)
	if !ok {
		return
	}

	tc := &view.Columns[titleCol]
	var pc *ct.Column
	if textCol >= 0 && !t.Fields[textCol].Empty {
		pc = &view.Columns[textCol]
	}

	fmt.Println("примеры статей с текстом:")
	shown := 0
	for i := 0; i < probe && shown < n; i++ {
		if tc.IsNull(i) || (pc != nil && pc.IsNull(i)) {
			continue
		}
		fmt.Println("  ", tc.Strings[i])
		shown++
	}
}

func main() {
	o := &options{}
	nsFlag := flag.String("ns", "all", "пространства имён через запятую (0 = статьи, 14 = категории) или all")
	flag.StringVar(&o.dump, "dump", "", "путь к .xml / .xml.bz2, '-' = stdin (уже распакованный XML)")
	flag.StringVar(&o.store, "store", "", "каталог с сериализованной таблицей: есть — грузим, нет — строим и сохраняем")
	flag.StringVar(&o.dumpText, "dumptext", "", "записать чистый текст в файл (для сравнения с xz/zstd/bzip2)")
	flag.IntVar(&o.limit, "limit", 0, "максимум не-редиректов (0 = все), удобно для отладки")
	flag.IntVar(&o.maxLinks, "maxlinks", 64, "максимум ссылок из lead на страницу")
	flag.IntVar(&o.maxRest, "maxrest", 0, "обрезать text_rest до N байт на страницу (0 = без ограничений)")
	flag.BoolVar(&o.redirects, "redirects", true, "редиректы тоже строки (они ~80% строк, но без текста)")
	demo := flag.String("demo", "", "title страницы для проверки GetMany")
	depth := flag.Int("depth", 1, "глубина раскрытия ссылок в -demo")
	flag.Parse()
	o.nsSet = parseNS(*nsFlag)

	t, loaded := loadTable(o.store)
	if !loaded {
		if o.dump == "" {
			log.Fatal("нужен -dump или -store с готовой таблицей")
		}
		var st *buildStats
		t, st = buildAll(o)
		size := saveTable(o.store, t)
		printReport(st, size)
	}
	t.Server(":8080")
	if *demo == "" {
		printSamples(t, 8)
		return
	}
	view, ok := t.GetMany([]Key{keyOf(normTitle(*demo))}, *depth)
	if !ok {
		log.Printf("%q нет в этой таблице", *demo)
		printSamples(t, 8)
		return
	}
	printRow(t.Row(view, 0))

}
