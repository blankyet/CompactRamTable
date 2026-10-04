package compact_table

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"math/bits"
	"slices"
	"strings"
)

type Tokenizer interface {
	AddTextToAnalyze(string)
	ComputeTokens()

	Tokenize(string) []byte
	Detokenize([]byte) string

	GetByteCountPerToken() uint8
}

func NewStringBlob(capHint int) StringBlob {
	return StringBlob{
		data:    make([]byte, 0, capHint*8),
		offsets: append(make([]uint32, 0, capHint+1), 0),
	}
}

// StringBlob stores many short strings contiguously with  start+end
type StringBlob struct {
	data    []byte
	offsets []uint32 // len = count+1; entry i spans data[offsets[i]:offsets[i+1]-1]
}

type TokenAlphabet map[string]uint32

type PopularWordTokenizer struct {
	// for building
	WordFreqs  map[string]uint32
	RawStrings StringBlob

	WordCount uint64
	WordBytes uint64

	PopularWordLimit int
	ChunkSize        int

	BytePerTokenCount uint8

	// runtime
	TokenAlphabet TokenAlphabet
	TokenWords    StringBlob

	Splitter string // " " "/" "-" ?
	order    []uint32
}

func (b *StringBlob) Bytes(id uint32) []byte {
	if int(id)+1 >= len(b.offsets) {
		return nil
	}
	start, end := b.offsets[id], b.offsets[id+1]-1 // -1 drops the NUL
	return b.data[start:end]
}

func NewPopularWordTokenizer(splitter string, popular_word_limit int, chunk_size int) *PopularWordTokenizer {
	return &PopularWordTokenizer{
		WordFreqs:     make(map[string]uint32),
		TokenAlphabet: make(TokenAlphabet),
		TokenWords:    NewStringBlob(0),
		RawStrings:    NewStringBlob(0),
		Splitter:      splitter,

		PopularWordLimit: popular_word_limit,
		ChunkSize:        chunk_size,
	}
}

func (t *PopularWordTokenizer) AddTextToAnalyze(text string) {
	for _, word := range strings.Split(text, t.Splitter) {
		if word == "" {
			continue
		}

		t.WordFreqs[word]++
		t.WordCount++
		t.WordBytes += uint64(len(word))
	}
}

func (t *PopularWordTokenizer) GetByteCountPerToken() uint8 {
	return t.BytePerTokenCount
}

func (b *StringBlob) Append(s string) uint32 {
	id := uint32(len(b.offsets) - 1)
	b.data = append(b.data, s...)
	b.data = append(b.data, 0)
	b.offsets = append(b.offsets, uint32(len(b.data)))
	return id
}

func (b *StringBlob) Len() int { return len(b.offsets) - 1 }

func (t *PopularWordTokenizer) ComputeTokens() {
	all_words := make([]string, 0, len(t.WordFreqs))
	for word := range t.WordFreqs {
		all_words = append(all_words, word)
	}

	slices.SortFunc(all_words, func(a, b string) int {
		if t.WordFreqs[a] != t.WordFreqs[b] {
			return cmp.Compare(t.WordFreqs[b], t.WordFreqs[a])
		}
		return cmp.Compare(a, b)
	})

	popularCount := min(t.PopularWordLimit, len(all_words))

	split_chunks_freqs := make(map[string]uint32)
	split_chunks := make([]string, 0, len(t.WordFreqs))
	for _, word := range all_words[popularCount:] {
		runes := []rune(word)
		if len(runes) == 0 {
			continue
		}

		for pos := 0; pos < len(runes); pos += t.ChunkSize {
			end := min(pos+t.ChunkSize, len(runes))
			word_chunk := string(runes[pos:end])
			if _, ok := split_chunks_freqs[word_chunk]; !ok {
				split_chunks = append(split_chunks, word_chunk)
			}
			split_chunks_freqs[word_chunk]++
		}
	}

	slices.SortFunc(split_chunks, func(a, b string) int {
		if split_chunks_freqs[a] != split_chunks_freqs[b] {
			return cmp.Compare(split_chunks_freqs[b], split_chunks_freqs[a])
		}
		return cmp.Compare(a, b)
	})

	t.TokenAlphabet = make(TokenAlphabet, popularCount+len(split_chunks_freqs))
	t.TokenWords = NewStringBlob(popularCount + len(split_chunks_freqs))

	for _, word := range all_words[:popularCount] {
		t.TokenAlphabet[word] = t.TokenWords.Append(word)
	}
	for _, chunk := range split_chunks {
		t.TokenAlphabet[chunk] = t.TokenWords.Append(chunk)
	}

	t.BytePerTokenCount = 4
	if t.TokenWords.Len() <= 65535 {
		t.BytePerTokenCount = 2
	}

	fmt.Printf("[ComputeTokens] popular=%d decomposed=%d/%d\n",
		popularCount, len(split_chunks), len(all_words))

	fmt.Printf("[ComputeTokens] vocab size=%d, bytesPerToken=%d, words=%d, chunks=%d\n",
		len(t.TokenAlphabet), t.BytePerTokenCount, popularCount, len(split_chunks))

	tokenBytes := t.WordCount * uint64(t.BytePerTokenCount)

	saved := t.WordBytes - tokenBytes

	fmt.Printf(
		"strings: raw=%.2f MiB token=%.2f MiB saved=%.2f MiB (%.1f%%)\n",
		float64(t.WordBytes)/(1024*1024),
		float64(t.WordCount*uint64(t.BytePerTokenCount))/(1024*1024),
		float64(saved)/(1024*1024),
		100*float64(saved)/float64(t.WordBytes),
	)
	varintBytes := uint64(0)

	for word, freq := range t.WordFreqs {
		runes := []rune(word)

		for pos := 0; pos < len(runes); pos += t.ChunkSize {
			end := min(pos+t.ChunkSize, len(runes))
			chunk := string(runes[pos:end])

			varintBytes += uint64(freq) * uint64((bits.Len32(t.TokenAlphabet[chunk])+6)/7)
		}
	}

	fmt.Printf(
		"strings: raw=%.2f MiB varint=%.2f MiB can_save=%.2f MiB (%.1f%%)\n",
		float64(t.WordBytes)/(1024*1024),
		float64(varintBytes)/(1024*1024),
		float64(t.WordBytes-varintBytes)/(1024*1024),
		100*float64(t.WordBytes-varintBytes)/float64(t.WordBytes),
	)
}

func (t *PopularWordTokenizer) Tokenize(text string) []byte {
	var buf []byte

	for _, word := range strings.Split(text, t.Splitter) {
		if word == "" {
			continue
		}

		if id, ok := t.TokenAlphabet[word]; ok && id < uint32(t.PopularWordLimit) {
			buf = t.appendToken(buf, id, false)
			continue
		}

		runes := []rune(word)
		for pos := 0; pos < len(runes); pos += t.ChunkSize {
			end := pos + t.ChunkSize
			if end > len(runes) {
				end = len(runes)
			}

			chunk := string(runes[pos:end])
			id, ok := t.TokenAlphabet[chunk]
			if !ok {
				continue
			}

			buf = t.appendToken(buf, id, pos > 0)
		}
	}

	return buf
}

func (t *PopularWordTokenizer) appendToken(buf []byte, id uint32, continuation bool) []byte {
	var flag byte
	if continuation {
		flag = 1
	}

	totalSize := int(t.BytePerTokenCount) + 1
	tokenBytes := make([]byte, totalSize)

	if t.BytePerTokenCount == 2 {
		binary.LittleEndian.PutUint16(tokenBytes[0:2], uint16(id))
	} else {
		binary.LittleEndian.PutUint32(tokenBytes[0:4], id)
	}
	tokenBytes[totalSize-1] = flag

	return append(buf, tokenBytes...)
}

func (t *PopularWordTokenizer) Detokenize(data []byte) string {
	var sb strings.Builder
	tokenSize := int(t.BytePerTokenCount)
	recordSize := tokenSize + 1

	for i := 0; i < len(data); i += recordSize {
		if i+recordSize > len(data) {
			break
		}

		var id uint32
		if tokenSize == 2 {
			id = uint32(binary.LittleEndian.Uint16(data[i : i+2]))
		} else {
			id = binary.LittleEndian.Uint32(data[i : i+4])
		}
		isContinuation := data[i+tokenSize] == 1

		// Retrieve text by ID from your string blob / vocabulary
		word := string(t.TokenWords.Bytes(id))

		if !isContinuation && sb.Len() > 0 {
			sb.WriteString(t.Splitter)
		}
		sb.WriteString(word)
	}

	return sb.String()
}
