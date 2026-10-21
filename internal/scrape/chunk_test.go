package scrape

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func maxRunes(chunks []string) int {
	m := 0
	for _, c := range chunks {
		if n := utf8.RuneCountInString(c); n > m {
			m = n
		}
	}
	return m
}

func TestSplitPacksParagraphs(t *testing.T) {
	text := "Para one.\n\nPara two is a bit longer.\n\nPara three.\n\n\n\nPara four."
	got := Split(text, 40)
	want := []string{"Para one.\n\nPara two is a bit longer.", "Para three.\n\nPara four."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
	// Reassembly preserves every paragraph.
	if Join(got) != "Para one.\n\nPara two is a bit longer.\n\nPara three.\n\nPara four." {
		t.Errorf("Join = %q", Join(got))
	}
}

func TestSplitNeverExceedsMax(t *testing.T) {
	long := strings.Repeat("word ", 100)                    // 500 chars, no paragraph breaks
	sentences := strings.Repeat("This is a sentence. ", 30) // sentence boundaries
	lines := strings.Repeat("a line of text\n", 20)         // line boundaries
	token := strings.Repeat("z", 150)                       // single oversized token
	text := long + "\n\n" + sentences + "\n\n" + lines + "\n\n" + token + "\n\nshort"
	for _, max := range []int{50, 100, 2000} {
		got := Split(text, max)
		if m := maxRunes(got); m > max {
			t.Errorf("max %d: chunk of %d runes", max, m)
		}
		joined := strings.Join(got, " ")
		for _, needle := range []string{"This is a sentence.", "a line of text", "short"} {
			if !strings.Contains(joined, needle) {
				t.Errorf("max %d: lost %q", max, needle)
			}
		}
		if strings.Count(joined, "z") != 150 {
			t.Errorf("max %d: oversized token mangled", max)
		}
	}
}

func TestSplitSentenceBoundaries(t *testing.T) {
	got := Split("First one. Second one! Third one? Fourth.", 22)
	want := []string{"First one. Second one!", "Third one? Fourth."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q", got)
	}
}

func TestSplitEdgeCases(t *testing.T) {
	if got := Split("", 10); got != nil {
		t.Errorf("empty → %v", got)
	}
	if got := Split("  \n\n  ", 10); got != nil {
		t.Errorf("blank → %v", got)
	}
	if got := Split("tiny", 0); len(got) != 1 || got[0] != "tiny" {
		t.Errorf("default max → %v", got)
	}
	multibyte := strings.Repeat("é", 25)
	got := Split(multibyte, 10)
	if len(got) != 3 || maxRunes(got) != 10 || strings.Join(got, "") != multibyte {
		t.Errorf("multibyte → %q", got)
	}
}

func TestSplitLargePageIsChunkSized(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString(strings.Repeat("Sentence number text. ", 12))
		b.WriteString("\n\n")
	}
	got := Split(b.String(), DefaultChunkChars)
	if len(got) < 20 || maxRunes(got) > DefaultChunkChars {
		t.Errorf("%d chunks, max %d", len(got), maxRunes(got))
	}
	// Chunks should be reasonably full, not one paragraph each.
	if utf8.RuneCountInString(got[0]) < DefaultChunkChars/2 {
		t.Errorf("first chunk only %d runes", utf8.RuneCountInString(got[0]))
	}
}

func TestOverlapIsTenPercent(t *testing.T) {
	if got := Overlap(2000); got != 200 {
		t.Errorf("Overlap(2000) = %d, want 200", got)
	}
	if got := Overlap(500); got != 50 {
		t.Errorf("Overlap(500) = %d, want 50", got)
	}
	if got := Overlap(0); got != Overlap(DefaultChunkChars) {
		t.Errorf("Overlap(0) = %d, want the default's", got)
	}
}

func TestOverlapTailsSnapToSentence(t *testing.T) {
	a := "First sentence here. Second sentence is longer. Third one ends it."
	b := "Next chunk begins."
	c := "Last chunk."
	got := OverlapTails([]string{a, b, c}, 30)
	want := []string{"", "Third one ends it.", "Next chunk begins."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("tails = %q, want %q", got, want)
	}
}

func TestOverlapTailsWordBoundaryAndEdges(t *testing.T) {
	prev := "alpha beta gamma delta epsilon"
	if got := OverlapTails([]string{prev, "x"}, 12); got[1] != "epsilon" {
		t.Errorf("word snap = %q", got[1])
	}
	if got := OverlapTails([]string{prev, "x"}, 0); got[1] != "" {
		t.Errorf("zero overlap = %q", got[1])
	}
	if got := OverlapTails([]string{"abcdefghij", "x"}, 4); got[1] != "ghij" {
		t.Errorf("no boundary = %q", got[1])
	}
	if got := OverlapTails([]string{"short", "x"}, 40); got[1] != "short" {
		t.Errorf("whole previous chunk = %q", got[1])
	}
	if out := OverlapTails(nil, 5); len(out) != 0 {
		t.Errorf("nil in, %v out", out)
	}
}

func TestOverlapTailsStayWithinBudget(t *testing.T) {
	chunks := Split(strings.Repeat("A sentence of some length that repeats. ", 400), 2000)
	tails := OverlapTails(chunks, Overlap(2000))
	if tails[0] != "" {
		t.Fatal("first chunk has no predecessor")
	}
	for i := 1; i < len(chunks); i++ {
		n := utf8.RuneCountInString(tails[i])
		if n == 0 || n > 200 {
			t.Fatalf("chunk %d: tail of %d runes, want 1..200", i, n)
		}
		if !strings.HasSuffix(chunks[i-1], tails[i]) {
			t.Fatalf("chunk %d: tail is not the end of the previous chunk", i)
		}
	}
}
