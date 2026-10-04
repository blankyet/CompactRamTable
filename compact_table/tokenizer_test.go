package compact_table

import (
	"testing"
)

func assert(t *testing.T, cond bool, format string, args ...any) {
	t.Helper()
	if !cond {
		t.Errorf(format, args...)
	}
}

func TestPopularWordTokenizerAddTextToAnalyze(t *testing.T) {
	t.Run("CountsWords", func(t *testing.T) {
		tokenizer := NewPopularWordTokenizer(" ", 100, 100)

		tokenizer.AddTextToAnalyze("hello world hello")

		assert(t, tokenizer.WordFreqs["hello"] == 2, "hello: got %d, want 2", tokenizer.WordFreqs["hello"])
		assert(t, tokenizer.WordFreqs["world"] == 1, "world: got %d, want 1", tokenizer.WordFreqs["world"])
	})

	t.Run("IgnoresEmptyWords", func(t *testing.T) {
		tokenizer := NewPopularWordTokenizer(" ", 100, 100)

		tokenizer.AddTextToAnalyze("hello  world   hello ")

		assert(t, tokenizer.WordFreqs["hello"] == 2, "hello: got %d, want 2", tokenizer.WordFreqs["hello"])
		assert(t, tokenizer.WordFreqs["world"] == 1, "world: got %d, want 1", tokenizer.WordFreqs["world"])
		assert(t, tokenizer.WordFreqs[""] == 0, "empty word: got %d", tokenizer.WordFreqs[""])
	})

	t.Run("MultipleCallsAccumulate", func(t *testing.T) {
		tokenizer := NewPopularWordTokenizer(" ", 100, 100)

		tokenizer.AddTextToAnalyze("hello world")
		tokenizer.AddTextToAnalyze("hello go")
		tokenizer.AddTextToAnalyze("world hello")

		want := map[string]uint32{
			"hello": 3,
			"world": 2,
			"go":    1,
		}

		for word, expected := range want {
			assert(
				t,
				tokenizer.WordFreqs[word] == expected,
				"%q: got %d, want %d",
				word,
				tokenizer.WordFreqs[word],
				expected,
			)
		}

		assert(
			t,
			len(tokenizer.WordFreqs) == len(want),
			"got %d unique words, want %d",
			len(tokenizer.WordFreqs),
			len(want),
		)
	})

	t.Run("CustomSplitter", func(t *testing.T) {
		tokenizer := NewPopularWordTokenizer("/", 100, 100)

		tokenizer.AddTextToAnalyze("foo/bar/foo//baz")

		assert(t, tokenizer.WordFreqs["foo"] == 2, "foo: got %d, want 2", tokenizer.WordFreqs["foo"])
		assert(t, tokenizer.WordFreqs["bar"] == 1, "bar: got %d, want 1", tokenizer.WordFreqs["bar"])
		assert(t, tokenizer.WordFreqs["baz"] == 1, "baz: got %d, want 1", tokenizer.WordFreqs["baz"])
		assert(t, len(tokenizer.WordFreqs) == 3, "got %d unique words, want 3", len(tokenizer.WordFreqs))
	})
}

func TestPopularWordTokenizerComputeTokens(t *testing.T) {
	tok := NewPopularWordTokenizer(" ", 3, 3)

	base := []string{
		"the quick brown fox jumps over the lazy dog",
		"the quick brown fox runs through the forest",
		"the dog jumps over the moon",
		"quick brown animals run through the forest",
		"the lazy fox sleeps under the tree",
		"brown fox and brown dog play together",
	}

	for i := 0; i < 200_000; i++ {
		for _, text := range base {
			tok.AddTextToAnalyze(text)
		}
	}

	tok.ComputeTokens()

	assert(t, tok.TokenWords.Len() > 0, "expected non-empty vocabulary")
	assert(t, tok.BytePerTokenCount == 2, "expected 2 bytes per token, got %d", tok.BytePerTokenCount)

	// Most frequent words should be first.
	for _, word := range []string{"the", "brown", "fox"} {
		_, ok := tok.TokenAlphabet[word]
		assert(t, ok, "expected popular word %q in alphabet", word)
	}

	for _, chunk := range []string{
		"tog",
		"eth",
		"er",
	} {
		_, ok := tok.TokenAlphabet[chunk]
		assert(t, ok, "expected chunk %q in alphabet", chunk)
	}

	for _, chunk := range []string{
		"the",
		"brown",
		"fox",
	} {
		_, ok := tok.TokenAlphabet[chunk]
		assert(t, ok, "expected popular word %q in alphabet", chunk)
	}

	for word, id := range tok.TokenAlphabet {
		got := string(tok.TokenWords.Bytes(id))
		assert(t, got == word, "token %q: got TokenWords[%d] = %q", word, id, got)
	}

	assert(t, len(tok.TokenAlphabet) == int(tok.TokenWords.Len()),
		"alphabet size=%d, vocabulary size=%d",
		len(tok.TokenAlphabet), tok.TokenWords.Len())

	for _, text := range base {
		encoded := tok.Tokenize(text)
		decoded := tok.Detokenize(encoded)

		assert(t, decoded == text,
			"round-trip mismatch: raw=%d, decoded=%d",
			len(text), len(decoded))
	}
}
