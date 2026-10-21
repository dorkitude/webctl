package prompts

import (
	"strings"
	"testing"
	"testing/fstest"
)

const validScore = `---
name: test-score
description: A test prompt
type: score
model: jev-latest
criteria:
  - low
  - high
---

Query: {{.Query}}
Title: {{.Title}}
`

func TestParseScore(t *testing.T) {
	p, err := Parse("test-score.md", []byte(validScore))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "test-score" || p.Type != "score" || p.Model != "jev-latest" || len(p.Criteria) != 2 {
		t.Errorf("prompt = %+v", p)
	}
	if !strings.HasPrefix(p.Body, "Query:") || strings.HasSuffix(p.Body, "\n") {
		t.Errorf("body should be trimmed: %q", p.Body)
	}
	out, err := p.Render(Data{Query: "q1", Title: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "Query: q1\nTitle: t1" {
		t.Errorf("render = %q", out)
	}
}

func TestParseNameFallsBackToFilename(t *testing.T) {
	content := "---\ntype: noul\n---\nBody {{.Question}}"
	p, err := Parse("dir/my-prompt.md", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "my-prompt" {
		t.Errorf("name = %q", p.Name)
	}
}

func TestParseCRLFAndBOM(t *testing.T) {
	content := "\uFEFF---\r\ntype: noul\r\nname: crlf\r\n---   \r\nHello {{.Query}}\r\n"
	p, err := Parse("crlf.md", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "crlf" || p.Body != "Hello {{.Query}}" {
		t.Errorf("prompt = %+v", p)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]struct {
		content string
		want    string
	}{
		"no frontmatter":       {"just text", "missing YAML frontmatter"},
		"unterminated":         {"---\ntype: noul\nbody", "unterminated"},
		"bad yaml":             {"---\ntype: [\n---\nbody", "parse frontmatter"},
		"missing type":         {"---\nname: x\n---\nbody", `missing required field "type"`},
		"unknown type":         {"---\ntype: essay\n---\nbody", "unknown type"},
		"score needs criteria": {"---\ntype: score\ncriteria: [only]\n---\nbody", "at least 2 criteria"},
		"noul with criteria":   {"---\ntype: noul\ncriteria: [a, b]\n---\nbody", "must not declare criteria"},
		"empty body":           {"---\ntype: noul\n---\n\n  \n", "body is empty"},
		"bad template":         {"---\ntype: noul\n---\n{{.Query", "parse template"},
	}
	for name, tc := range cases {
		_, err := Parse(name+".md", []byte(tc.content))
		if err == nil {
			t.Errorf("%s: expected error", name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", name, err, tc.want)
		}
		if !strings.Contains(err.Error(), name+".md") {
			t.Errorf("%s: error should name the file: %v", name, err)
		}
	}
}

func TestRenderUnknownFieldErrors(t *testing.T) {
	p, err := Parse("x.md", []byte("---\ntype: noul\n---\n{{.Nope}}"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Render(Data{}); err == nil || !strings.Contains(err.Error(), "render prompt") {
		t.Errorf("expected render error for unknown field, got %v", err)
	}
}

func TestLoadFSAndListFS(t *testing.T) {
	fsys := fstest.MapFS{
		"b.md":      {Data: []byte(validScore)},
		"a.md":      {Data: []byte("---\ntype: noul\n---\nhi")},
		"notes.txt": {Data: []byte("ignored")},
		"sub/c.md":  {Data: []byte("ignored (not top-level)")},
	}
	names, err := ListFS(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "a,b" {
		t.Errorf("ListFS = %v", names)
	}
	for _, name := range []string{"b", "b.md"} {
		p, err := LoadFS(fsys, name)
		if err != nil {
			t.Fatalf("LoadFS(%q): %v", name, err)
		}
		if p.Name != "test-score" {
			t.Errorf("LoadFS(%q).Name = %q", name, p.Name)
		}
	}
	if _, err := LoadFS(fsys, "missing"); err == nil || !strings.Contains(err.Error(), `load prompt "missing"`) {
		t.Errorf("err = %v", err)
	}
}

func TestEmbeddedPrompts(t *testing.T) {
	names := List()
	want := []string{ChunkRelevance, DuplicatePair, RelevanceBatch, RelevanceNoul, RelevanceScore, SourceQuality, Summarize, ThemeCoverage}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("List = %v, want %v", names, want)
	}
	for _, name := range names {
		p, err := Load(name)
		if err != nil {
			t.Errorf("Load(%q): %v", name, err)
			continue
		}
		if p.Name != name {
			t.Errorf("Load(%q).Name = %q", name, p.Name)
		}
		if p.Description == "" || (p.Model == "" && p.Type != "text") {
			t.Errorf("%s: model/description should be set", name)
		}
		// Every shipped prompt must render cleanly with full Data.
		out, err := p.Render(Data{Query: "Q", Title: "T", URL: "U", Snippet: "S", Question: "?", Theme: "TH", Chunk: "CHUNK", ID: "result_0", Index: 0})
		if err != nil {
			t.Errorf("%s: render: %v", name, err)
		}
		needles := []string{"Q", "T", "U", "S"}
		switch name {
		case ThemeCoverage:
			needles = []string{"Q", "TH", "result_0"}
		case ChunkRelevance:
			// The query and chunk live in request state; repeating either in
			// every question wastes input tokens.
			needles = []string{"result_0"}
		case DuplicatePair:
			needles = []string{"Q", "S", "result_0"}
		}
		for _, needle := range needles {
			if !strings.Contains(out, needle) {
				t.Errorf("%s: rendered output missing %q:\n%s", name, needle, out)
			}
		}
	}

	score := MustLoad(RelevanceScore)
	batch := MustLoad(RelevanceBatch)
	if score.Type != "score" || batch.Type != "score" || len(score.Criteria) != 4 {
		t.Errorf("score=%+v batch=%+v", score.Type, batch.Type)
	}
	if strings.Join(score.Criteria, "|") != strings.Join(batch.Criteria, "|") {
		t.Error("score and batch prompts should share criteria")
	}
	if !batch.Batch || score.Batch {
		t.Error("batch flag should be set only on the batch prompt")
	}
	noul := MustLoad(RelevanceNoul)
	if noul.Type != "noul" || len(noul.Criteria) != 0 {
		t.Errorf("noul = %+v", noul)
	}
	cov := MustLoad(ThemeCoverage)
	if cov.Type != "noul" || !cov.Batch {
		t.Errorf("theme-coverage = type %q batch %v", cov.Type, cov.Batch)
	}
}

func TestLoadCaches(t *testing.T) {
	a, _ := Load(RelevanceScore)
	b, _ := Load(RelevanceScore)
	if a != b {
		t.Error("Load should return the cached *Prompt")
	}
	if _, err := Load("does-not-exist"); err == nil {
		t.Error("expected error for unknown prompt")
	}
}

func TestMustLoadPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustLoad should panic for a missing prompt")
		}
	}()
	MustLoad("does-not-exist")
}
