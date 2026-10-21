package jev

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestFilterChunksBatchesOnePage(t *testing.T) {
	js := newJevServer(t, func(req SystemOneRequest) (int, any) {
		answers := map[string]Answer{}
		for key := range req.Questions {
			p := 0.9
			if key == ChunkKey(1) {
				p = 0.1
			}
			answers[key] = Answer{Type: TypeNoul, Noul: &p}
		}
		// chunk_2 is deliberately unanswered.
		delete(answers, ChunkKey(2))
		return 200, SystemOneResponse{Answers: answers, Usage: Usage{InputTokens: 300, OutputTokens: 3}}
	})
	chunks := textChunks("Transformers use attention.", "Subscribe to our newsletter!", "Multi-head attention…")
	got, usage, err := js.client().FilterChunks(context.Background(), Ask{Query: "how do transformers work", Goal: "learn attention"}, chunks)
	if err != nil {
		t.Fatal(err)
	}
	if js.calls.Load() != 1 {
		t.Errorf("three small chunks should fit one request, got %d", js.calls.Load())
	}
	if len(got) != 3 || got[0] == nil || !got[0].Yes() || got[1] == nil || got[1].Yes() || got[2] != nil {
		t.Errorf("answers = %+v", got)
	}
	if usage.InputTokens != 300 {
		t.Errorf("usage = %+v", usage)
	}

	req, _ := js.last()
	if len(req.Questions) != 3 {
		t.Errorf("questions = %d, want one per chunk", len(req.Questions))
	}
	q := req.Questions[ChunkKey(1)]
	if q.Type != TypeNoul || !strings.Contains(q.Instructions, `"chunk_1"`) {
		t.Errorf("question = %+v", q)
	}
	for _, duplicated := range []string{"Subscribe to our newsletter!", "how do transformers work", "learn attention"} {
		if strings.Contains(q.Instructions, duplicated) {
			t.Errorf("question should point to state, not duplicate %q:\n%s", duplicated, q.Instructions)
		}
	}
	state, _ := req.State.(map[string]any)
	items, _ := state["chunks"].([]any)
	if state["query"] != "how do transformers work" || state["goal"] != "learn attention" || len(items) != 3 {
		t.Errorf("state = %+v", state)
	}
	first, _ := items[0].(map[string]any)
	if first["id"] != "chunk_0" || first["text"] != chunks[0].Text {
		t.Errorf("state chunk 0 = %+v", first)
	}
}

// A long page must not go out as one oversized request: that is what Jev
// rejects with max_tokens_exceeded.
func TestFilterChunksSplitsLongPage(t *testing.T) {
	js := newJevServer(t, func(req SystemOneRequest) (int, any) {
		answers := map[string]Answer{}
		for key := range req.Questions {
			p := 0.9
			answers[key] = Answer{Type: TypeNoul, Noul: &p}
		}
		return 200, SystemOneResponse{Answers: answers, Usage: Usage{InputTokens: 10}}
	})
	// 200 chunks of ~2,000 chars: the shape that used to blow the context.
	chunks := make([]Chunk, 200)
	for i := range chunks {
		chunks[i] = Chunk{Text: strings.Repeat("a", 2000)}
	}
	got, usage, err := js.client().FilterChunks(context.Background(), Ask{Query: "q"}, chunks)
	if err != nil {
		t.Fatal(err)
	}
	calls := int(js.calls.Load())
	if calls < 2 {
		t.Fatalf("expected the page to be split, got %d request(s)", calls)
	}
	for i, a := range got {
		if a == nil || !a.Yes() {
			t.Fatalf("chunk %d unanswered: %+v", i, a)
		}
	}
	if usage.InputTokens != 10*calls {
		t.Errorf("usage should accumulate across batches: %+v over %d calls", usage, calls)
	}

	// Every batch must fit the budget it was planned against.
	req, _ := js.last()
	if n := len(req.Questions); n > MaxChunksPerBatch {
		t.Errorf("batch carried %d chunks, over the %d cap", n, MaxChunksPerBatch)
	}
}

func TestPlanChunkBatchesRespectsBudget(t *testing.T) {
	big := strings.Repeat("b", 2000)
	chunks := make([]Chunk, 50)
	for i := range chunks {
		chunks[i] = Chunk{Text: big}
	}
	const fixed = 100
	batches := planChunkBatches(chunks, 300, fixed)
	if len(batches) < 2 {
		t.Fatalf("expected several batches, got %d", len(batches))
	}
	covered := 0
	for _, b := range batches {
		if b.hi <= b.lo {
			t.Fatalf("empty batch %+v", b)
		}
		if b.lo != covered {
			t.Fatalf("batches must be contiguous: %+v after %d", b, covered)
		}
		covered = b.hi
		if n := b.hi - b.lo; n > MaxChunksPerBatch {
			t.Errorf("batch of %d exceeds cap %d", n, MaxChunksPerBatch)
		}
		est := fixed
		for i := b.lo; i < b.hi; i++ {
			est += estimateTokens(chunks[i].Text) + estimateTokens(chunks[i].Before) + 300
		}
		if est > ChunkBatchTokenBudget && b.hi-b.lo > 1 {
			t.Errorf("batch %+v estimated at %d tokens, over budget %d", b, est, ChunkBatchTokenBudget)
		}
	}
	if covered != len(chunks) {
		t.Errorf("batches covered %d of %d chunks", covered, len(chunks))
	}
}

// A single chunk larger than the whole budget still has to be sent, not dropped.
func TestPlanChunkBatchesOversizedSingleChunk(t *testing.T) {
	batches := planChunkBatches(textChunks(strings.Repeat("c", 400000), "small"), 300, 100)
	if len(batches) != 2 || batches[0] != (chunkBatch{0, 1}) || batches[1] != (chunkBatch{1, 2}) {
		t.Errorf("batches = %+v", batches)
	}
}

// Jev rejecting a batch for size should halve it rather than fail the page.
func TestFilterChunksRetriesOversizedBatch(t *testing.T) {
	var seen int
	js := newJevServer(t, func(req SystemOneRequest) (int, any) {
		seen++
		if len(req.Questions) > 2 {
			return 400, `{"detail":{"error_type":"max_tokens_exceeded"}}`
		}
		answers := map[string]Answer{}
		for key := range req.Questions {
			p := 0.9
			answers[key] = Answer{Type: TypeNoul, Noul: &p}
		}
		return 200, SystemOneResponse{Answers: answers}
	})
	c := js.client()
	c.NoRetry = true
	chunks := textChunks("one", "two", "three", "four")
	got, _, err := c.FilterChunks(context.Background(), Ask{Query: "q"}, chunks)
	if err != nil {
		t.Fatalf("splitting should recover: %v", err)
	}
	for i, a := range got {
		if a == nil || !a.Yes() {
			t.Fatalf("chunk %d unanswered after split: %+v", i, a)
		}
	}
	if seen < 3 {
		t.Errorf("expected an oversized attempt plus halves, got %d requests", seen)
	}
}

// One failing batch must cost only its own chunks, and say which.
func TestFilterChunksPartialFailure(t *testing.T) {
	js := newJevServer(t, func(req SystemOneRequest) (int, any) {
		// Fail whichever batch carries chunk_0; answer the rest.
		if _, ok := req.Questions[ChunkKey(0)]; ok {
			return 500, `boom`
		}
		answers := map[string]Answer{}
		for key := range req.Questions {
			p := 0.9
			answers[key] = Answer{Type: TypeNoul, Noul: &p}
		}
		return 200, SystemOneResponse{Answers: answers}
	})
	c := js.client()
	c.NoRetry = true
	big := strings.Repeat("d", 2000)
	chunks := make([]Chunk, 60)
	for i := range chunks {
		chunks[i] = Chunk{Text: big}
	}
	got, _, err := c.FilterChunks(context.Background(), Ask{Query: "q"}, chunks)

	var partial *PartialFilterError
	if !errors.As(err, &partial) {
		t.Fatalf("want *PartialFilterError, got %v", err)
	}
	if partial.Failed == 0 || partial.Failed >= partial.Batches {
		t.Errorf("partial = %+v", partial)
	}
	if len(partial.Unjudged) == 0 || partial.Unjudged[0] != 0 {
		t.Errorf("unjudged should start at the failed batch: %v", partial.Unjudged)
	}
	for _, i := range partial.Unjudged {
		if got[i] != nil {
			t.Errorf("chunk %d reported unjudged but has an answer", i)
		}
	}
	// Chunks outside the failed batch keep their verdicts.
	var answered int
	for _, a := range got {
		if a != nil {
			answered++
		}
	}
	if answered == 0 {
		t.Error("a single failed batch should not lose every verdict")
	}
	if !strings.Contains(partial.Error(), "unjudged") {
		t.Errorf("error text = %q", partial.Error())
	}
}

// Every batch failing is a plain error, not a partial one.
func TestFilterChunksTotalFailure(t *testing.T) {
	js := newJevServer(t, func(SystemOneRequest) (int, any) { return 500, `boom` })
	c := js.client()
	c.NoRetry = true
	_, _, err := c.FilterChunks(context.Background(), Ask{Query: "q"}, textChunks("x"))
	if err == nil || !strings.Contains(err.Error(), "chunk filter") {
		t.Fatalf("server error should propagate: %v", err)
	}
	var partial *PartialFilterError
	if errors.As(err, &partial) {
		t.Errorf("total failure should not be partial: %v", err)
	}
}

func TestFilterChunksEdgeCases(t *testing.T) {
	js := newJevServer(t, func(SystemOneRequest) (int, any) { return 500, `boom` })
	c := js.client()
	c.NoRetry = true
	if got, _, err := c.FilterChunks(context.Background(), Ask{Query: "q"}, nil); err != nil || len(got) != 0 || js.calls.Load() != 0 {
		t.Errorf("no chunks should short-circuit: %v, %v", got, err)
	}
	if _, _, err := c.FilterChunks(context.Background(), Ask{Query: ""}, textChunks("x")); err == nil {
		t.Error("empty query should error")
	}
}

func TestTooLarge(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&APIError{Status: 400, Body: `{"detail":{"error_type":"max_tokens_exceeded"}}`}, true},
		{&APIError{Status: 413, Body: "request too large"}, true},
		{&APIError{Status: 400, Body: "context length exceeded"}, true},
		{&APIError{Status: 400, Body: "bad question type"}, false},
		{&APIError{Status: 500, Body: "max_tokens_exceeded"}, false},
		{errors.New("network"), false},
		{fmt.Errorf("wrapped: %w", &APIError{Status: 400, Body: "max_tokens_exceeded"}), true},
	}
	for _, tc := range cases {
		if got := tooLarge(tc.err); got != tc.want {
			t.Errorf("tooLarge(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func textChunks(texts ...string) []Chunk {
	out := make([]Chunk, len(texts))
	for i, t := range texts {
		out[i] = Chunk{Text: t}
	}
	return out
}

// Before reaches the request as labeled context, separate from the chunk, and
// neither is copied into the question.
func TestFilterChunksSendsBeforeAsContext(t *testing.T) {
	js := newJevServer(t, func(req SystemOneRequest) (int, any) {
		answers := map[string]Answer{}
		for key := range req.Questions {
			p := 0.9
			answers[key] = Answer{Type: TypeNoul, Noul: &p}
		}
		return 200, SystemOneResponse{Answers: answers}
	})
	chunks := []Chunk{{Text: "first"}, {Text: "second", Before: "tail of first"}}
	if _, _, err := js.client().FilterChunks(context.Background(), Ask{Query: "q"}, chunks); err != nil {
		t.Fatal(err)
	}
	req, _ := js.last()
	state, _ := req.State.(map[string]any)
	items, _ := state["chunks"].([]any)
	first, _ := items[0].(map[string]any)
	second, _ := items[1].(map[string]any)
	if _, has := first["before"]; has || second["before"] != "tail of first" {
		t.Errorf("state should carry before only where set: %+v", items)
	}
	q := req.Questions[ChunkKey(1)].Instructions
	if !strings.Contains(q, `"chunk_1"`) || strings.Contains(q, "tail of first") || strings.Contains(q, "second") {
		t.Errorf("question should point to the state without duplicating it:\n%s", q)
	}
	if second["text"] != "second" {
		t.Errorf("state should carry the judged text: %+v", second)
	}
}
