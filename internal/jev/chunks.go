package jev

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/dorkitude/webctl/internal/prompts"
)

// Chunk-filter batching. A page is split into chunks of ~2,000 characters,
// and every chunk needs its own yes/no question. The chunk lives once in the
// request state and the question points to it by id, following the measured
// state-pointer design from github.com/dorkitude/skimmer. Sending every chunk
// in one request is what Jev rejects with max_tokens_exceeded on a long page,
// so chunks are packed into requests that stay under a token budget and run
// in parallel.
const (
	// ChunkBatchTokenBudget is the estimated-token ceiling for one
	// chunk-filter request. Each chunk appears once in the state, plus a
	// short question that refers to it by id. Measured failures against
	// jev-latest began between 54 and 81 chunks of ~2,000 characters, so
	// this leaves generous headroom and buys parallelism besides.
	ChunkBatchTokenBudget = 24000

	// MaxChunksPerBatch caps a batch regardless of the token estimate, so a
	// page of many tiny chunks still splits into parallelizable requests.
	MaxChunksPerBatch = 24

	// ChunkBatchConcurrency bounds parallel chunk-filter requests for one
	// page. Pages are already filtered concurrently by the caller, so this
	// stays modest to keep the total number of in-flight Jev calls sane.
	ChunkBatchConcurrency = 4
)

// ChunkKey returns the question key used for the i-th chunk in FilterChunks.
func ChunkKey(i int) string { return "chunk_" + strconv.Itoa(i) }

// Chunk is one piece of a page to judge. Before is optional preceding text
// (the tail of the previous chunk) shown to the judge for orientation only;
// it is not part of the chunk and is not returned with it.
type Chunk struct {
	Text   string
	Before string
}

type chunkItem struct {
	ID     string `json:"id"`
	Before string `json:"before,omitempty"`
	Text   string `json:"text"`
}

type chunkState struct {
	Ask
	Chunks []chunkItem `json:"chunks"`
}

// PartialFilterError reports that some chunk-filter batches failed while
// others succeeded. The answers returned alongside it are still usable;
// Unjudged lists the chunk indices Jev never ruled on, which callers should
// treat as "keep" rather than "drop" so a Jev hiccup never silently deletes
// page content.
type PartialFilterError struct {
	// Unjudged are chunk indices with no verdict, ascending.
	Unjudged []int
	// Batches is how many batches the page was split into.
	Batches int
	// Failed is how many of those batches failed.
	Failed int
	// Err is one representative underlying failure.
	Err error
}

func (e *PartialFilterError) Error() string {
	return fmt.Sprintf("jev chunk filter: %d of %d batch(es) failed, %d chunk(s) unjudged and kept unfiltered: %v",
		e.Failed, e.Batches, len(e.Unjudged), e.Err)
}

func (e *PartialFilterError) Unwrap() error { return e.Err }

// estimateTokens approximates the token cost of s. Byte length over rune
// length is deliberate: multi-byte text over-estimates, which errs toward
// smaller batches.
func estimateTokens(s string) int { return (len(s) + 3) / 4 }

// tooLarge reports whether err is Jev refusing a request for being too big,
// which is the one failure that splitting the batch can actually fix.
func tooLarge(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.Status != http.StatusBadRequest && apiErr.Status != http.StatusRequestEntityTooLarge {
		return false
	}
	body := strings.ToLower(apiErr.Body)
	return strings.Contains(body, "max_tokens") || strings.Contains(body, "too large") || strings.Contains(body, "context length")
}

// chunkBatch is a contiguous run of chunks sent as one request.
type chunkBatch struct{ lo, hi int } // chunks[lo:hi]

// planChunkBatches packs chunks into batches that each stay under the token
// budget. perChunkOverhead is the fixed cost of one rendered question;
// fixedOverhead is the query, goal, and request envelope paid once per
// batch. Every batch holds at least one chunk, however large it is.
func planChunkBatches(chunks []Chunk, perChunkOverhead, fixedOverhead int) []chunkBatch {
	var batches []chunkBatch
	lo, used := 0, fixedOverhead
	for i, ch := range chunks {
		cost := estimateTokens(ch.Text) + estimateTokens(ch.Before) + perChunkOverhead
		full := i > lo && (used+cost > ChunkBatchTokenBudget || i-lo >= MaxChunksPerBatch)
		if full {
			batches = append(batches, chunkBatch{lo, i})
			lo, used = i, fixedOverhead
		}
		used += cost
	}
	if lo < len(chunks) {
		batches = append(batches, chunkBatch{lo, len(chunks)})
	}
	return batches
}

// FilterChunks asks Jev whether each chunk of a page is relevant to query.
// Chunks are packed into batches that fit Jev's context and sent in parallel.
// The returned slice is aligned with chunks; entries Jev did not answer are
// nil.
//
// Failure is graceful and contained: a batch that Jev rejects for size is
// split and retried, and a batch that fails for any other reason costs only
// its own chunks. If some batches succeeded, the error is a
// *PartialFilterError and the answers are still usable. The error is a plain
// one only when every batch failed.
func (c *Client) FilterChunks(ctx context.Context, ask Ask, chunks []Chunk) ([]*NoulAnswer, Usage, error) {
	out := make([]*NoulAnswer, len(chunks))
	if len(chunks) == 0 {
		return out, Usage{}, nil
	}
	if ask.Query == "" {
		return nil, Usage{}, errors.New("jev: query is empty")
	}
	p, err := prompts.Load(prompts.ChunkRelevance)
	if err != nil {
		return nil, Usage{}, err
	}

	// Render one question to measure the boilerplate, so the batch plan
	// budgets for the real prompt rather than a guess.
	probe, err := p.Render(prompts.Data{ID: ChunkKey(0), Index: 0})
	if err != nil {
		return nil, Usage{}, err
	}
	perChunkOverhead := estimateTokens(probe) + chunkEnvelopeTokens
	fixedOverhead := estimateTokens(ask.Query) + estimateTokens(ask.Goal) + chunkBatchEnvelopeTokens

	batches := planChunkBatches(chunks, perChunkOverhead, fixedOverhead)

	var (
		mu       sync.Mutex
		usage    Usage
		unjudged []int
		failed   int
		lastErr  error
		wg       sync.WaitGroup
	)
	conc := ChunkBatchConcurrency
	if conc > len(batches) {
		conc = len(batches)
	}
	sem := make(chan struct{}, conc)
	for _, b := range batches {
		wg.Add(1)
		sem <- struct{}{}
		go func(b chunkBatch) {
			defer wg.Done()
			defer func() { <-sem }()
			u, miss, err := c.filterChunkRange(ctx, ask, p, chunks, b, out)
			mu.Lock()
			defer mu.Unlock()
			usage.Add(u)
			if err != nil {
				failed++
				lastErr = err
				unjudged = append(unjudged, miss...)
			}
		}(b)
	}
	wg.Wait()

	if failed == 0 {
		return out, usage, nil
	}
	if failed == len(batches) {
		return out, usage, fmt.Errorf("jev chunk filter: %w", lastErr)
	}
	sort.Ints(unjudged)
	return out, usage, &PartialFilterError{Unjudged: unjudged, Batches: len(batches), Failed: failed, Err: lastErr}
}

// chunkEnvelopeTokens is the per-question JSON scaffolding around the
// instructions (key, type, quoting) plus the chunk's state entry.
const chunkEnvelopeTokens = 24

// chunkBatchEnvelopeTokens covers the state and questions objects around the
// query, goal, and item arrays. It is deliberately conservative.
const chunkBatchEnvelopeTokens = 32

// filterChunkRange sends chunks[b.lo:b.hi] as one request and writes the
// verdicts into out. A request rejected for size is split in half and
// retried, down to a single chunk. On failure it returns the indices left
// unjudged.
func (c *Client) filterChunkRange(ctx context.Context, ask Ask, p *prompts.Prompt, chunks []Chunk, b chunkBatch, out []*NoulAnswer) (Usage, []int, error) {
	state := chunkState{Ask: ask, Chunks: make([]chunkItem, 0, b.hi-b.lo)}
	questions := make(map[string]Question, b.hi-b.lo)
	for i := b.lo; i < b.hi; i++ {
		id := ChunkKey(i)
		state.Chunks = append(state.Chunks, chunkItem{ID: id, Before: chunks[i].Before, Text: chunks[i].Text})
		instructions, err := p.Render(prompts.Data{ID: id, Index: i})
		if err != nil {
			return Usage{}, missing(b), err
		}
		questions[id] = NoulQuestion(instructions)
	}

	resp, err := c.SystemOne(ctx, &SystemOneRequest{State: state, Questions: questions})
	if err != nil {
		// Too big even though it was planned to fit: halve and retry. The
		// budget is an estimate, so this is the backstop that makes the
		// estimate's accuracy a performance concern rather than a bug.
		if tooLarge(err) && b.hi-b.lo > 1 {
			mid := b.lo + (b.hi-b.lo)/2
			u1, miss1, err1 := c.filterChunkRange(ctx, ask, p, chunks, chunkBatch{b.lo, mid}, out)
			u2, miss2, err2 := c.filterChunkRange(ctx, ask, p, chunks, chunkBatch{mid, b.hi}, out)
			u1.Add(u2)
			miss := append(miss1, miss2...)
			switch {
			case err1 != nil && err2 != nil:
				return u1, miss, err1
			case err1 != nil:
				return u1, miss, err1
			case err2 != nil:
				return u1, miss, err2
			}
			return u1, nil, nil
		}
		return Usage{}, missing(b), err
	}

	for i := b.lo; i < b.hi; i++ {
		if raw, ok := resp.Answers[ChunkKey(i)]; ok {
			if ans, err := raw.AsNoul(); err == nil {
				out[i] = ans
			}
		}
	}
	return resp.Usage, nil, nil
}

// missing lists every index in b, for a batch that produced no verdicts.
func missing(b chunkBatch) []int {
	idx := make([]int, 0, b.hi-b.lo)
	for i := b.lo; i < b.hi; i++ {
		idx = append(idx, i)
	}
	return idx
}
