package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/dorkitude/webctl/internal/config"
	"github.com/dorkitude/webctl/internal/dedupe"
	"github.com/dorkitude/webctl/internal/jev"
	"github.com/dorkitude/webctl/internal/keys"
	"github.com/dorkitude/webctl/internal/provider"
	"github.com/dorkitude/webctl/internal/scrape"
	"github.com/dorkitude/webctl/internal/summarize"
)

// searchFlags holds the flag values for the search (root) command.
type searchFlags struct {
	provider   string
	goal       string
	num        int
	minScore   float64
	minResults int
	jsonOut    bool
	urlsOnly   bool
	noFilter   bool
	verbose    bool
	batch      bool
	rubric     string
	noul       string
	multi      bool
	random     bool
	sources    int
	scrape     bool
	chunkChars int
	maxChars   int
	chunks     bool
	noDedupe   bool
	scrapeTop  int
	summarize  bool
	sumCommand string
	sumModel   string
	maxOutput  int
}

var sf searchFlags

// Output defaults.
const (
	// DefaultMaxOutput bounds the printed output so an agent's tool-result
	// window sees all of it: Claude Code shows only a preview past ~30 KB.
	DefaultMaxOutput = 20000
	// DefaultScrapeTop is how many of the best kept results --scrape fetches.
	DefaultScrapeTop = 3
)

func addSearchFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVarP(&sf.goal, "goal", "g", "", "HIGHLY RECOMMENDED: what you actually need; shown to every judge next to the query")
	f.StringVarP(&sf.provider, "provider", "p", "", "use exactly one provider (see `webctl docs providers`), e.g. brave, exa, ddg, searxng, ketch")
	f.IntVarP(&sf.num, "num", "n", 0, "results to request per provider (default 20)")
	f.IntVar(&sf.sources, "sources", 0, "providers to query and fuse (default 3)")
	f.BoolVar(&sf.multi, "multi", false, "query every available provider")
	f.BoolVar(&sf.random, "random", false, "query every available provider in random order")
	f.Float64VarP(&sf.minScore, "min-score", "m", -1, "keep results scoring at least this out of 10 (default 6)")
	f.IntVar(&sf.minResults, "min-results", -1, "if fewer pass the score cut, promote the best of the rest to reach this many (never off-topic)")
	f.StringVar(&sf.rubric, "rubric", "", "custom score levels, comma-separated, lowest first")
	f.StringVar(&sf.noul, "noul", "", "ask this yes/no question per result instead of scoring")
	f.BoolVar(&sf.batch, "batch", false, "score all results in one Jev request")
	f.BoolVar(&sf.noFilter, "no-filter", false, "skip Jev; print provider results")
	f.BoolVar(&sf.noDedupe, "no-dedupe", false, "skip the Jev near-duplicate pass")
	f.BoolVar(&sf.scrape, "scrape", false, "fetch page text for the top results; use with --filter-chunks whenever you would otherwise read a whole page")
	f.BoolVar(&sf.chunks, "filter-chunks", false, "with --scrape, return only the chunks Jev finds relevant to the goal")
	f.IntVar(&sf.maxChars, "max-chars", scrape.DefaultMaxChars, "with --scrape, cap text per page")
	f.IntVar(&sf.chunkChars, "chunk-chars", scrape.DefaultChunkChars, "with --filter-chunks, chunk size in characters; judged with 10% overlap")
	f.IntVar(&sf.scrapeTop, "scrape-top", DefaultScrapeTop, "with --scrape, fetch only the N best-scoring kept results; 0 = all")
	f.BoolVar(&sf.summarize, "summarize", false, "with --scrape, replace each page's kept text with a short summary from a small model (see `webctl docs summarize`)")
	f.StringVar(&sf.sumCommand, "summarize-command", "", "summarizer command for this run, e.g. 'claude -p --model haiku' (overrides summarize.command)")
	f.StringVar(&sf.sumModel, "summarize-model", "", "model for the configured summarize.endpoint for this run")
	f.IntVar(&sf.maxOutput, "max-output", DefaultMaxOutput, "cap printed output in characters, trimming scraped content top-down; 0 = unlimited")
	f.BoolVar(&sf.jsonOut, "json", false, "JSON output")
	f.BoolVar(&sf.urlsOnly, "urls-only", false, "one URL per line")
	f.BoolVarP(&sf.verbose, "verbose", "v", false, "show scores, dropped results, every cooldown notice")
}

// qualifier is the slice of *jev.Client the search pipeline depends on.
// It exists so tests can substitute a fake without an HTTP server.
type qualifier interface {
	Qualify(ctx context.Context, ask jev.Ask, results []provider.SearchResult, opts jev.QualifyOptions) ([]jev.Qualified, jev.Usage, error)
	FilterChunks(ctx context.Context, ask jev.Ask, chunks []jev.Chunk) ([]*jev.NoulAnswer, jev.Usage, error)
	ConfirmDuplicates(ctx context.Context, ask jev.Ask, results []provider.SearchResult, pairs []jev.DuplicatePair) ([]bool, jev.Usage, error)
}

// jevConfirmer adapts a qualifier to dedupe.Confirmer, accumulating usage.
type jevConfirmer struct {
	q     qualifier
	usage jev.Usage
}

func (j *jevConfirmer) ConfirmDuplicates(ctx context.Context, query, goal string, results []provider.SearchResult, pairs []dedupe.Pair) ([]bool, error) {
	jp := make([]jev.DuplicatePair, len(pairs))
	for i, p := range pairs {
		jp[i] = jev.DuplicatePair{A: p.A, B: p.B}
	}
	out, usage, err := j.q.ConfirmDuplicates(ctx, jev.Ask{Query: query, Goal: goal}, results, jp)
	j.usage.Add(usage)
	return out, err
}

// collapseDuplicates asks Jev which near-duplicate candidates are the same
// content and folds each group into its best-scored member, which keeps
// the union of engines and lists the others as Duplicates. It returns the
// survivors in their original order and how many were folded.
func collapseDuplicates(ctx context.Context, q qualifier, ask jev.Ask, qualified []jev.Qualified, engines map[string][]string) ([]jev.Qualified, int, jev.Usage, error) {
	results := make([]provider.SearchResult, len(qualified))
	for i, item := range qualified {
		results[i] = item.Result
	}
	jc := &jevConfirmer{q: q}
	groups, _, err := dedupe.Run(ctx, jc, ask.Query, ask.Goal, results)
	if err != nil {
		return qualified, 0, jc.usage, err
	}
	drop := map[int]bool{}
	for _, g := range groups {
		best := g[0]
		for _, i := range g[1:] {
			if qualified[i].Value() > qualified[best].Value() {
				best = i
			}
		}
		keeper := &qualified[best]
		for _, i := range g {
			if i == best {
				continue
			}
			keeper.Duplicates = append(keeper.Duplicates, qualified[i].Result)
			drop[i] = true
			if engines != nil {
				engines[keeper.Result.URL] = unionStrings(engines[keeper.Result.URL], engines[qualified[i].Result.URL])
			}
		}
	}
	out := make([]jev.Qualified, 0, len(qualified))
	for i, item := range qualified {
		if !drop[i] {
			out = append(out, item)
		}
	}
	return out, len(drop), jc.usage, nil
}

func unionStrings(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// scraper is the slice of *scrape.Fetcher the pipeline depends on.
type scraper interface {
	FetchAll(ctx context.Context, urls []string, concurrency int) []scrape.Page
}

// Construction hooks. Tests override these to inject fakes.
var (
	newScraper = func(maxChars int) scraper {
		return &scrape.Fetcher{MaxChars: maxChars}
	}
	newProvider = func(cfg *config.Config, name string) (provider.Provider, error) {
		cred, err := cfg.ProviderKey(name)
		if err != nil {
			return nil, err
		}
		return provider.New(name, cred, provider.Options{})
	}
	newQualifier = func(cfg *config.Config, key string) qualifier {
		c := jev.NewClient(key)
		c.BaseURL = cfg.JevBaseURL
		c.Model = cfg.JevModel
		return c
	}
)

// noulDefaultThreshold is the P(yes) cutoff used with --noul when --min-score
// is not given. The score-mode default (1.0) would drop every result, since
// P(yes) never exceeds 1.
const noulDefaultThreshold = 0.5

// searchOptions is the fully-resolved, validated input to the pipeline.
type searchOptions struct {
	Query string
	// Goal is what the user actually needs; the judges see it next to the query.
	Goal string
	// Providers is the ordered chain to try (or, with modeMulti, to fuse).
	Providers []string
	Mode      searchMode
	// Sources is how many providers to gather from and fuse.
	Sources  int
	Num      int
	MinScore float64
	// MinResults, when > 0, backfills the kept set from the best-scoring
	// dropped results up to this count. Results under BackfillFloor are
	// never promoted.
	MinResults int
	NoFilter   bool
	Batch      bool
	Verbose    bool
	Rubric     []string
	Noul       string
	Format     outputFormat
	// Scrape fetches page content for kept results; MaxChars caps it per page.
	Scrape   bool
	MaxChars int
	// FilterChunks keeps only the scraped chunks Jev judges relevant.
	// ChunkChars is the chunk size; each chunk is judged with the tail of
	// the previous one prepended (scrape.OverlapFraction).
	FilterChunks bool
	ChunkChars   int
	// NoDedupe skips the Jev duplicate pass.
	NoDedupe bool
	// ScrapeTop limits --scrape to the N best kept results; 0 means all.
	ScrapeTop int
	// MaxOutput caps the printed output in runes by trimming scraped
	// content, never headers; 0 means unlimited.
	MaxOutput int
	// Summarizer, when set, rewrites each scraped page's kept text into a
	// short goal-focused summary (--summarize).
	Summarizer summarize.Summarizer
}

// newSummarizer builds the --summarize backend. Tests override it.
var newSummarizer = summarize.New

// ask is what every Jev judge is given.
func (o searchOptions) ask() jev.Ask { return jev.Ask{Query: o.Query, Goal: o.Goal} }

type outputFormat int

// searchMode selects how the provider chain is used.
type searchMode int

const (
	modeChain  searchMode = iota // first success in chain order
	modeMulti                    // all providers in parallel, fused with RRF
	modeRandom                   // chain in random order
)

const (
	formatPretty outputFormat = iota
	formatJSON
	formatURLs
)

func runSearch(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	opts, err := resolveSearchOptions(cfg, sf, args)
	if err != nil {
		return err
	}
	return runPipeline(cmd.Context(), cfg, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
}

// resolveSearchOptions merges flags with config defaults and validates them.
func resolveSearchOptions(cfg *config.Config, f searchFlags, args []string) (searchOptions, error) {
	query := strings.TrimSpace(strings.Join(args, " "))
	if query == "" {
		return searchOptions{}, errors.New("query is empty")
	}
	if f.jsonOut && f.urlsOnly {
		return searchOptions{}, errors.New("--json and --urls-only are mutually exclusive")
	}
	if f.rubric != "" && f.noul != "" {
		return searchOptions{}, errors.New("--rubric and --noul are mutually exclusive")
	}
	if f.multi && f.random {
		return searchOptions{}, errors.New("--multi and --random are mutually exclusive")
	}
	if f.provider != "" && (f.multi || f.random) {
		return searchOptions{}, errors.New("--provider cannot be combined with --multi or --random")
	}
	if f.scrape && f.maxChars <= 0 {
		return searchOptions{}, fmt.Errorf("--max-chars must be positive, got %d", f.maxChars)
	}
	if f.chunks && !f.scrape {
		return searchOptions{}, errors.New("--filter-chunks requires --scrape")
	}
	if f.chunkChars <= 0 {
		return searchOptions{}, fmt.Errorf("--chunk-chars must be positive, got %d", f.chunkChars)
	}
	if f.scrapeTop < 0 {
		return searchOptions{}, fmt.Errorf("--scrape-top must be 0 or more, got %d", f.scrapeTop)
	}
	if (f.summarize || f.sumCommand != "" || f.sumModel != "") && !f.scrape {
		return searchOptions{}, errors.New("--summarize requires --scrape")
	}
	if f.maxOutput < 0 {
		return searchOptions{}, fmt.Errorf("--max-output must be 0 or more, got %d", f.maxOutput)
	}

	opts := searchOptions{
		Query:        query,
		Goal:         strings.TrimSpace(f.goal),
		Num:          cfg.Num,
		MinScore:     cfg.MinScore,
		NoFilter:     f.noFilter,
		Batch:        f.batch,
		Verbose:      f.verbose,
		Noul:         strings.TrimSpace(f.noul),
		Scrape:       f.scrape,
		MaxChars:     f.maxChars,
		FilterChunks: f.chunks,
		ChunkChars:   f.chunkChars,
		NoDedupe:     f.noDedupe,
		ScrapeTop:    f.scrapeTop,
		MaxOutput:    f.maxOutput,
	}
	if f.num > 0 {
		opts.Num = f.num
	}
	if f.summarize || f.sumCommand != "" || f.sumModel != "" {
		sc := cfg.Summarize
		if f.sumCommand != "" {
			sc.Command = f.sumCommand
		}
		if f.sumModel != "" {
			sc.Model = f.sumModel
			if f.sumCommand == "" {
				sc.Command = "" // a model names the endpoint backend
			}
		}
		if !sc.Configured() {
			return searchOptions{}, errors.New("--summarize needs a backend: `webctl config set summarize.command '...'` or summarize.endpoint + summarize.model; see `webctl docs summarize`")
		}
		sm, err := newSummarizer(sc)
		if err != nil {
			return searchOptions{}, err
		}
		opts.Summarizer = sm
	}
	opts.MinResults = cfg.MinResults
	if f.minResults >= 0 {
		opts.MinResults = f.minResults
	}
	switch {
	case f.jsonOut:
		opts.Format = formatJSON
	case f.urlsOnly:
		opts.Format = formatURLs
	}

	explicitMin := f.minScore >= 0
	if explicitMin {
		opts.MinScore = f.minScore
	} else if opts.Noul != "" {
		opts.MinScore = noulDefaultThreshold
	}
	if opts.Noul != "" && opts.MinScore > 1 {
		return searchOptions{}, fmt.Errorf("--min-score %.2f is impossible with --noul: P(yes) is at most 1", opts.MinScore)
	}
	if opts.Noul == "" && opts.MinScore > jev.ScaleMax {
		return searchOptions{}, fmt.Errorf("--min-score %.2f exceeds the top score of %g", opts.MinScore, jev.ScaleMax)
	}

	if f.rubric != "" {
		rubric, err := parseRubric(f.rubric)
		if err != nil {
			return searchOptions{}, err
		}
		opts.Rubric = rubric
		if !explicitMin {
			opts.MinScore = jev.DefaultCut(len(rubric))
		}
	}

	chain, err := cfg.Chain(f.provider)
	if err != nil {
		return searchOptions{}, err
	}
	opts.Providers = chain
	opts.Sources = cfg.Sources
	if f.sources > 0 {
		opts.Sources = f.sources
	}
	if f.provider != "" {
		opts.Sources = 1
	}
	switch {
	case f.multi:
		opts.Mode = modeMulti
		opts.Sources = len(chain)
	case f.random:
		opts.Mode = modeRandom
	}
	return opts, nil
}

// parseRubric splits a comma-separated criteria list, lowest to highest.
func parseRubric(s string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	if len(out) < 2 {
		return nil, fmt.Errorf("--rubric needs at least 2 comma-separated criteria, got %d", len(out))
	}
	return out, nil
}

// runPipeline executes search → qualify → filter → output.
func runPipeline(ctx context.Context, cfg *config.Config, opts searchOptions, out, errOut io.Writer) error {
	// An explicitly chosen provider must be usable; fail before any network call.
	if len(opts.Providers) == 1 {
		if _, err := cfg.ProviderKey(opts.Providers[0]); err != nil {
			return err
		}
	}
	// Resolve the Jev key before searching so a missing key fails fast
	// instead of after a paid provider call.
	var jevKey string
	if !opts.NoFilter || opts.FilterChunks {
		var err error
		if jevKey, err = cfg.JevKey(); err != nil {
			return err
		}
	}
	// chunkFilter is available whenever a Jev key is: --filter-chunks uses
	// it on every page, and PDFs are always chunked through it because a
	// paper's text layer is far too long to hand over whole.
	var chunkFilter qualifier
	if opts.Scrape && jevKey != "" {
		chunkFilter = newQualifier(cfg, jevKey)
	}

	label, results, engines, err := runSearchStage(ctx, cfg, opts, errOut)
	if err != nil {
		return err
	}
	results = provider.Dedupe(results)

	if opts.NoFilter {
		if len(results) == 0 && opts.Format == formatPretty {
			fmt.Fprintf(errOut, "%s returned no results.\n", label)
		}
		raw := toRaw(results, engines)
		if opts.Scrape && opts.Format != formatURLs {
			// Without scores, the fused order stands in for rank.
			n := len(raw)
			if opts.ScrapeTop > 0 && opts.ScrapeTop < n {
				n = opts.ScrapeTop
			}
			urls := make([]string, n)
			fallback := make([]string, n)
			titles := make([]string, n)
			for i, r := range raw[:n] {
				urls[i], fallback[i], titles[i] = r.URL, r.Content, r.Title
			}
			pages := scrapePages(ctx, opts, chunkFilter, urls, fallback, titles)
			for i := range pages {
				raw[i].Page = &pages[i]
			}
			writeScrapeSummary(errOut, pages, opts)
		}
		return writeRaw(out, errOut, raw, opts)
	}

	if len(results) == 0 {
		if opts.Format == formatPretty {
			fmt.Fprintf(errOut, "%s returned no results.\n", label)
		}
		return writeQualified(out, errOut, nil, opts)
	}

	q := newQualifier(cfg, jevKey)
	qualified, usage, err := q.Qualify(ctx, opts.ask(), results, jev.QualifyOptions{
		Rubric: opts.Rubric,
		Noul:   opts.Noul,
		Batch:  opts.Batch,
	})
	if err != nil {
		return err
	}

	// Engines often index the same page at different addresses: one Jev
	// pass over the near-duplicate candidates folds those together.
	folded := 0
	if !opts.NoDedupe {
		var dupUsage jev.Usage
		var dedupeErr error
		if engines == nil {
			engines = map[string][]string{}
		}
		qualified, folded, dupUsage, dedupeErr = collapseDuplicates(ctx, q, opts.ask(), qualified, engines)
		usage.Add(dupUsage)
		if dedupeErr != nil && (opts.Format == formatPretty || opts.Verbose) {
			fmt.Fprintf(errOut, "duplicate check failed (%v); showing all results\n", dedupeErr)
		}
	}
	ranked := rank(qualified, opts.MinScore)
	backfill(ranked, opts.MinResults)
	for i := range ranked {
		ranked[i].Engines = engines[ranked[i].Result.URL]
	}
	if folded > 0 && (opts.Format == formatPretty || opts.Verbose) {
		fmt.Fprintf(errOut, "%d duplicate(s) folded into their best copy\n", folded)
	}
	if opts.Format == formatPretty || opts.Verbose {
		writeSummary(errOut, label, ranked, opts, usage)
	}
	if opts.Scrape && opts.Format != formatURLs {
		// Only the best kept results are worth fetching; ranked is in
		// score order, and backfilled results were under the cut.
		var idx []int
		var urls, fallback, titles []string
		for i, r := range ranked {
			if !r.Kept || r.Backfilled || (opts.ScrapeTop > 0 && len(idx) >= opts.ScrapeTop) {
				continue
			}
			idx = append(idx, i)
			urls = append(urls, r.Result.URL)
			fallback = append(fallback, r.Result.Content)
			titles = append(titles, r.Result.Title)
		}
		pages := scrapePages(ctx, opts, chunkFilter, urls, fallback, titles)
		for j, i := range idx {
			ranked[i].Page = &pages[j]
		}
		if opts.Format == formatPretty || opts.Verbose {
			writeScrapeSummary(errOut, pages, opts)
		}
	}
	return writeQualified(out, errOut, ranked, opts)
}

// pageContent is a result's scraped text plus what happened to it.
type pageContent struct {
	Content string
	Err     error
	// Fallback is set when Content is the provider's excerpt because the
	// page could not be fetched.
	Fallback bool
	// PDF is set when the page was a PDF; its text is always chunk-filtered.
	PDF bool
	// Filtered is set when --filter-chunks ran on this page. FilterErr
	// records a Jev failure. When every batch failed, Content is the
	// unfiltered text; when only some did, the chunks Jev never ruled on
	// are kept alongside the ones it approved and ChunksUnjudged counts them.
	Filtered       bool
	FilterErr      error
	ChunksTotal    int
	ChunksKept     int
	ChunksUnjudged int
	// RawChars is the content length before chunk filtering.
	RawChars int
	// Trimmed counts the runes cut from Content by --max-output.
	Trimmed int
	// Summarized is set when --summarize replaced Content with a summary;
	// SummaryErr records a summarizer failure (Content is then the kept
	// text); SummaryFrom is the kept text's length before summarizing.
	Summarized   bool
	SummaryErr   error
	SummaryFrom  int
	SummaryUsage summarize.Usage
}

// scrapePages fetches every URL (aligned with urls), caps each page's text,
// and, when q is non-nil, keeps only the chunks Jev judges relevant. Each
// page's chunks go to Jev in a single batch request; pages run concurrently.
// fallback (aligned with urls) is the provider's excerpt, used when a fetch
// fails.
func scrapePages(ctx context.Context, opts searchOptions, q qualifier, urls, fallback, titles []string) []pageContent {
	out := make([]pageContent, len(urls))
	if len(urls) == 0 {
		return out
	}
	pages := newScraper(opts.MaxChars).FetchAll(ctx, urls, scrape.DefaultConcurrency)
	for i, p := range pages {
		out[i] = pageContent{Content: p.Content, Err: p.Err, PDF: p.PDF}
		if p.Err != nil {
			// A page behind a wall still has the provider's excerpt.
			out[i].Content = scrape.Truncate(fallback[i], opts.MaxChars)
			out[i].Fallback = out[i].Content != ""
		}
		out[i].RawChars = len([]rune(out[i].Content))
	}
	if q == nil {
		return out
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, jev.DefaultConcurrency)
	for i := range out {
		if out[i].Content == "" || !(opts.FilterChunks || out[i].PDF) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(pc *pageContent) {
			defer wg.Done()
			defer func() { <-sem }()
			filterChunks(ctx, q, opts.ask(), pc, opts.ChunkChars)
		}(&out[i])
	}
	wg.Wait()
	if opts.Summarizer != nil {
		summarizePages(ctx, opts, urls, titles, out)
	}
	return out
}

// summarizePages rewrites each page's kept text into a short summary. A
// page the model calls irrelevant ends up with no content; a summarizer
// failure leaves the kept text in place and records the error.
func summarizePages(ctx context.Context, opts searchOptions, urls, titles []string, pages []pageContent) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, summarize.DefaultConcurrency)
	for i := range pages {
		if pages[i].Content == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			pc := &pages[i]
			title := ""
			if i < len(titles) {
				title = titles[i]
			}
			in := summarize.Input{Query: opts.Query, Goal: opts.Goal, Title: title, URL: urls[i], Chunks: []string{pc.Content}}
			text, usage, err := opts.Summarizer.Summarize(ctx, in)
			pc.SummaryUsage = usage
			if err != nil {
				pc.SummaryErr = err
				return
			}
			pc.SummaryFrom = len([]rune(pc.Content))
			pc.Summarized = true
			if summarize.IsNothing(text) {
				pc.Content = ""
				return
			}
			pc.Content = text
		}(i)
	}
	wg.Wait()
}

// filterChunks splits pc.Content, asks Jev about every chunk, and reassembles
// the ones it says yes to. Jev failures degrade rather than abort: when only
// some batches failed, the chunks they covered are kept unfiltered and the
// rest are still filtered normally; only a total failure falls back to the
// whole unfiltered page.
func filterChunks(ctx context.Context, q qualifier, ask jev.Ask, pc *pageContent, chunkChars int) {
	chunks := scrape.Split(pc.Content, chunkChars)
	pc.Filtered = true
	pc.ChunksTotal = len(chunks)
	// Judge each chunk with the tail of the previous one as context; the
	// kept text is the bare chunk.
	answers, _, err := q.FilterChunks(ctx, ask, withContext(chunks, chunkChars))

	var partial *jev.PartialFilterError
	if err != nil {
		pc.FilterErr = err
		if !errors.As(err, &partial) {
			// Nothing was judged: keep the page whole rather than blank it.
			pc.ChunksKept = len(chunks)
			return
		}
	}

	unjudged := make(map[int]bool, len(partialUnjudged(partial)))
	for _, i := range partialUnjudged(partial) {
		unjudged[i] = true
	}

	kept := make([]string, 0, len(chunks))
	for i, c := range chunks {
		switch {
		case unjudged[i]:
			// Jev never ruled on this one; keeping it is the safe default.
			kept = append(kept, c)
			pc.ChunksUnjudged++
		case i < len(answers) && answers[i] != nil && answers[i].Yes():
			kept = append(kept, c)
		}
	}
	pc.ChunksKept = len(kept)
	pc.Content = scrape.Join(kept)
}

// withContext pairs each chunk with the overlap tail of the one before it.
func withContext(chunks []string, chunkChars int) []jev.Chunk {
	tails := scrape.OverlapTails(chunks, scrape.Overlap(chunkChars))
	out := make([]jev.Chunk, len(chunks))
	for i, c := range chunks {
		out[i] = jev.Chunk{Text: c, Before: tails[i]}
	}
	return out
}

// partialUnjudged returns the unjudged chunk indices, or nil when there was
// no partial failure.
func partialUnjudged(e *jev.PartialFilterError) []int {
	if e == nil {
		return nil
	}
	return e.Unjudged
}

func writeScrapeSummary(w io.Writer, pages []pageContent, opts searchOptions) {
	if opts.Format != formatPretty && !opts.Verbose {
		return
	}
	var failed, fallback, rawChars, chars, total, kept, unjudged, filterFailed, filterPartial int
	for _, p := range pages {
		if p.Err != nil {
			failed++
			if p.Fallback {
				fallback++
			}
		}
		rawChars += p.RawChars
		chars += len([]rune(p.Content))
		total += p.ChunksTotal
		kept += p.ChunksKept
		unjudged += p.ChunksUnjudged
		if p.FilterErr != nil {
			if p.ChunksUnjudged > 0 {
				filterPartial++
			} else {
				filterFailed++
			}
		}
	}
	fmt.Fprintf(w, "scraped %d page(s)", len(pages))
	if failed > 0 {
		fmt.Fprintf(w, " (%d failed", failed)
		if fallback > 0 {
			fmt.Fprintf(w, ", %d using the provider excerpt", fallback)
		}
		fmt.Fprint(w, ")")
	}
	if opts.FilterChunks || total > 0 {
		fmt.Fprintf(w, "; chunks %d → %d kept; %d → %d chars", total, kept, rawChars, chars)
		if filterFailed > 0 {
			fmt.Fprintf(w, "; %d page(s) unfiltered (Jev error)", filterFailed)
		}
		if filterPartial > 0 {
			fmt.Fprintf(w, "; %d page(s) partly filtered (%d chunk(s) unjudged, kept)", filterPartial, unjudged)
		}
	} else {
		fmt.Fprintf(w, ", %d chars", chars)
	}
	if opts.Summarizer != nil {
		var n, from, to, failed, empty, in, outTok int
		for _, p := range pages {
			if p.SummaryErr != nil {
				failed++
			}
			if !p.Summarized {
				continue
			}
			n++
			from += p.SummaryFrom
			to += len([]rune(p.Content))
			if p.Content == "" {
				empty++
			}
			in += p.SummaryUsage.InputTokens
			outTok += p.SummaryUsage.OutputTokens
		}
		fmt.Fprintf(w, "; summarized %d page(s): %d → %d chars", n, from, to)
		if empty > 0 {
			fmt.Fprintf(w, ", %d with nothing relevant", empty)
		}
		if in+outTok > 0 {
			fmt.Fprintf(w, " (%d in / %d out tokens)", in, outTok)
		}
		if failed > 0 {
			fmt.Fprintf(w, "; %d page(s) not summarized (error, showing kept text)", failed)
		}
	}
	fmt.Fprintln(w)
	if opts.Format == formatPretty {
		fmt.Fprintln(w)
	}
}

// shuffleChain randomizes the order of a copy of chain. Tests override it.
var shuffleChain = func(chain []string) []string {
	out := append([]string(nil), chain...)
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// runSearchStage runs the provider stage for opts.Mode. It returns a label
// naming the engine(s) that answered, the results, and (in multi mode) the
// engines that returned each URL.
func runSearchStage(ctx context.Context, cfg *config.Config, opts searchOptions, errOut io.Writer) (string, []provider.SearchResult, map[string][]string, error) {
	names := opts.Providers
	if opts.Mode == modeRandom {
		names = shuffleChain(names)
	}
	c := newChain(cfg, names, errOut, opts.Verbose)
	c.Sources = opts.Sources
	results, err := c.Search(ctx, opts.Query, opts.Num)
	if err != nil {
		return "", nil, nil, err
	}
	return c.Name(), results, c.Engines(), nil
}

// Chain timing and top-up, overridable by tests.
var (
	attemptTimeout = provider.DefaultAttemptTimeout
	chainBudget    = provider.DefaultChainBudget
	chainTopUp     = true
)

// newCooldown opens the persistent cooldown store for cfg. Tests override it.
var newCooldown = func(cfg *config.Config) provider.Cooldowns {
	return provider.NewCooldown(cfg.CooldownPath, cfg.Cooldown)
}

// cooldownKey tracks keyed and keyless use of a provider separately.
func cooldownKey(cfg *config.Config, name string) string {
	if n, err := keys.Parse(provider.Normalize(name)); err == nil && cfg.Keys.Get(n) != "" {
		return name + "+key"
	}
	return name
}

// newChain wraps chain in a lazily-constructed provider.Chain that reports
// fall-throughs and cooldown skips to errOut. Skips are mentioned once an
// hour per provider unless verbose.
func newChain(cfg *config.Config, chain []string, errOut io.Writer, verbose bool) *provider.Chain {
	return &provider.Chain{
		Names:          chain,
		New:            func(name string) (provider.Provider, error) { return newProvider(cfg, name) },
		AttemptTimeout: attemptTimeout,
		Budget:         chainBudget,
		NoTopUp:        !chainTopUp,
		Cooldowns:      newCooldown(cfg),
		CooldownKey:    func(name string) string { return cooldownKey(cfg, name) },
		OnFallthrough: func(failed string, err error, next string) {
			if next == "" {
				fmt.Fprintf(errOut, "%s failed: %v\n", failed, err)
				return
			}
			fmt.Fprintf(errOut, "%s failed (%v); trying %s\n", failed, err, next)
		},
		OnSkip: func(name, reason string, announce bool) {
			if announce || verbose {
				fmt.Fprintln(errOut, reason)
			}
		},
	}
}

// searchChain tries each provider in order and returns the first successful
// search, reporting fall-throughs to errOut.
func searchChain(ctx context.Context, cfg *config.Config, chain []string, query string, num int, errOut io.Writer) (string, []provider.SearchResult, error) {
	c := newChain(cfg, chain, errOut, false)
	results, err := c.Search(ctx, query, num)
	if err != nil {
		return "", nil, err
	}
	return c.Name(), results, nil
}

// rankedResult is a qualified result plus the pipeline's keep/drop decision.
type rankedResult struct {
	jev.Qualified
	Kept bool
	// Backfilled is set when the result was under the score cut but kept
	// to reach --min-results.
	Backfilled bool
	// Engines lists the backends that returned this URL (multi mode only).
	Engines []string
	// Page is the scraped content (--scrape), nil when not fetched.
	Page *pageContent
}

// rank sorts qualified results by relevance (highest first) and marks which
// clear the threshold. Results Jev failed on sort last and are never kept.
func rank(qualified []jev.Qualified, minScore float64) []rankedResult {
	out := make([]rankedResult, 0, len(qualified))
	for _, q := range qualified {
		kept := q.Err == nil && (q.Score != nil || q.Noul != nil) && q.Value() >= minScore
		out = append(out, rankedResult{Qualified: q, Kept: kept})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kept != out[j].Kept {
			return out[i].Kept
		}
		return out[i].Value() > out[j].Value()
	})
	return out
}

// backfillFloor is the lowest value --min-results may promote: level 1 of
// the rubric (3.3 of 10 for the built-in one), so off-topic results are
// never promoted. For yes/no mode it is P(yes) ≥ 0.25.
func backfillFloor(r *rankedResult) float64 {
	if r.Score != nil {
		return jev.LevelValue(1, r.Score.MaxScore()+1)
	}
	return 0.25
}

// backfill promotes the best-scoring dropped results until min are kept,
// skipping errors and anything under BackfillFloor. ranked must be in
// rank() order. It returns how many were promoted.
func backfill(ranked []rankedResult, min int) int {
	kept, _ := countKept(ranked)
	promoted := 0
	for i := range ranked {
		if kept >= min {
			break
		}
		r := &ranked[i]
		if r.Kept || r.Err != nil || (r.Score == nil && r.Noul == nil) || r.Value() < backfillFloor(r) {
			continue
		}
		r.Kept, r.Backfilled = true, true
		kept++
		promoted++
	}
	if promoted > 0 {
		sort.SliceStable(ranked, func(i, j int) bool {
			if ranked[i].Kept != ranked[j].Kept {
				return ranked[i].Kept
			}
			return ranked[i].Value() > ranked[j].Value()
		})
	}
	return promoted
}

func countKept(ranked []rankedResult) (kept, failed int) {
	for _, r := range ranked {
		if r.Kept {
			kept++
		}
		if r.Err != nil {
			failed++
		}
	}
	return kept, failed
}

func writeSummary(w io.Writer, providerName string, ranked []rankedResult, opts searchOptions, usage jev.Usage) {
	kept, failed := countKept(ranked)
	what := fmt.Sprintf("min score %g/10", opts.MinScore)
	if opts.Noul != "" {
		what = fmt.Sprintf("P(yes) ≥ %.2f", opts.MinScore)
	}
	fmt.Fprintf(w, "%s: %d results → %d kept (%s)", providerName, len(ranked), kept, what)
	if failed > 0 {
		fmt.Fprintf(w, "; %d not scored (Jev error)", failed)
	}
	if opts.MinResults > 0 {
		backfilled := 0
		for _, r := range ranked {
			if r.Backfilled {
				backfilled++
			}
		}
		if backfilled > 0 {
			fmt.Fprintf(w, "; %d backfilled toward --min-results %d", backfilled, opts.MinResults)
		}
		if kept < opts.MinResults {
			fmt.Fprintf(w, "; only %d were on topic, so --min-results %d was not reached", kept, opts.MinResults)
		}
	}
	fmt.Fprintln(w)
	if opts.Verbose {
		mode := "per-result"
		if opts.Batch {
			mode = "batch"
		}
		fmt.Fprintf(w, "jev: %s mode, %d input / %d output tokens\n", mode, usage.InputTokens, usage.OutputTokens)
	}
	if opts.Format == formatPretty {
		fmt.Fprintln(w)
	}
}

// --- Output ---------------------------------------------------------------

// outputResult is the JSON shape for a qualified result.
type outputResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`

	// Score mode: Score is 0–10. Confidence and Probabilities (per rubric
	// level) appear only with --verbose.
	Score         *float64           `json:"score,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`

	// Noul mode: Yes and P(yes). Confidence appears only with --verbose.
	Yes         *bool    `json:"yes,omitempty"`
	Probability *float64 `json:"probability,omitempty"`

	Kept bool `json:"kept"`
	// Backfilled marks a result kept only to reach --min-results.
	Backfilled bool     `json:"backfilled,omitempty"`
	Error      string   `json:"error,omitempty"`
	Engines    []string `json:"engines,omitempty"`
	// Duplicates are other addresses of the same content that were folded
	// into this result.
	Duplicates []string `json:"duplicates,omitempty"`

	// --scrape fields.
	Content     string `json:"content,omitempty"`
	ScrapeError string `json:"scrape_error,omitempty"`
	PDF         *bool  `json:"pdf,omitempty"`
	// --filter-chunks fields.
	ChunksTotal    *int   `json:"chunks_total,omitempty"`
	ChunksKept     *int   `json:"chunks_kept,omitempty"`
	ChunksUnjudged *int   `json:"chunks_unjudged,omitempty"`
	FilterError    string `json:"filter_error,omitempty"`
	// CharsTrimmed is how much content --max-output cut from this result.
	CharsTrimmed *int `json:"chars_trimmed,omitempty"`
	// Summarized marks content as a --summarize summary of the kept text.
	Summarized   *bool  `json:"summarized,omitempty"`
	SummaryError string `json:"summary_error,omitempty"`
}

// fillPage copies scraped content into the JSON fields.
func (o *outputResult) fillPage(p *pageContent) {
	if p == nil {
		return
	}
	o.Content, o.ScrapeError, o.ChunksTotal, o.ChunksKept, o.ChunksUnjudged, o.FilterError, o.CharsTrimmed = pageFields(p)
	o.PDF = pdfFlag(p)
	o.Summarized, o.SummaryError = summaryFields(p)
}

// pageFields flattens a pageContent into the shared JSON field values.
func pageFields(p *pageContent) (content, scrapeErr string, total, kept, unjudged *int, filterErr string, trimmed *int) {
	content = p.Content
	if p.Err != nil {
		scrapeErr = p.Err.Error()
		if p.Fallback {
			scrapeErr += " (content is the provider excerpt)"
		}
	}
	if p.Filtered {
		t, k := p.ChunksTotal, p.ChunksKept
		total, kept = &t, &k
	}
	if p.ChunksUnjudged > 0 {
		u := p.ChunksUnjudged
		unjudged = &u
	}
	if p.FilterErr != nil {
		filterErr = p.FilterErr.Error()
	}
	if p.Trimmed > 0 {
		t := p.Trimmed
		trimmed = &t
	}
	return content, scrapeErr, total, kept, unjudged, filterErr, trimmed
}

// summaryFields flattens the --summarize outcome for JSON.
func summaryFields(p *pageContent) (*bool, string) {
	var flag *bool
	if p.Summarized {
		t := true
		flag = &t
	}
	errText := ""
	if p.SummaryErr != nil {
		errText = p.SummaryErr.Error()
	}
	return flag, errText
}

// pdfFlag returns a pointer to true for PDF pages, nil otherwise, so the
// JSON field is omitted for ordinary pages.
func pdfFlag(p *pageContent) *bool {
	if p == nil || !p.PDF {
		return nil
	}
	yes := true
	return &yes
}

func toOutput(r rankedResult, verbose bool) outputResult {
	o := outputResult{
		Title:      r.Result.Title,
		URL:        r.Result.URL,
		Snippet:    r.Result.Snippet,
		Kept:       r.Kept,
		Backfilled: r.Backfilled,
		Engines:    r.Engines,
	}
	for _, d := range r.Duplicates {
		o.Duplicates = append(o.Duplicates, d.URL)
	}
	if r.Score != nil {
		score := math.Round(r.Score.Scaled()*10) / 10
		o.Score = &score
		if verbose {
			conf := r.Score.Confidence
			o.Confidence = &conf
			o.Probabilities = r.Score.Probabilities
		}
	}
	if r.Noul != nil {
		yes, p := r.Noul.Yes(), r.Noul.Probability
		o.Yes, o.Probability = &yes, &p
		if verbose {
			conf := r.Noul.Confidence()
			o.Confidence = &conf
		}
	}
	if r.Err != nil {
		o.Error = r.Err.Error()
	}
	o.fillPage(r.Page)
	return o
}

// rawResult is the output shape for an unqualified result (--no-filter).
type rawResult struct {
	provider.SearchResult
	Engines []string `json:"engines,omitempty"`

	Page *pageContent `json:"-"`
	// --scrape / --filter-chunks fields, filled from Page before encoding.
	Content        string `json:"content,omitempty"`
	ScrapeError    string `json:"scrape_error,omitempty"`
	PDF            *bool  `json:"pdf,omitempty"`
	ChunksTotal    *int   `json:"chunks_total,omitempty"`
	ChunksKept     *int   `json:"chunks_kept,omitempty"`
	ChunksUnjudged *int   `json:"chunks_unjudged,omitempty"`
	FilterError    string `json:"filter_error,omitempty"`
	CharsTrimmed   *int   `json:"chars_trimmed,omitempty"`
	Summarized     *bool  `json:"summarized,omitempty"`
	SummaryError   string `json:"summary_error,omitempty"`
}

func (r *rawResult) fillPage() {
	if r.Page == nil {
		return
	}
	r.Content, r.ScrapeError, r.ChunksTotal, r.ChunksKept, r.ChunksUnjudged, r.FilterError, r.CharsTrimmed = pageFields(r.Page)
	r.PDF = pdfFlag(r.Page)
	r.Summarized, r.SummaryError = summaryFields(r.Page)
}

func toRaw(results []provider.SearchResult, engines map[string][]string) []rawResult {
	out := make([]rawResult, 0, len(results))
	for _, r := range results {
		out = append(out, rawResult{SearchResult: r, Engines: engines[r.URL], Content: r.Content})
	}
	return out
}

// writeRaw prints unqualified provider results (--no-filter).
func writeRaw(w, errOut io.Writer, results []rawResult, opts searchOptions) error {
	if opts.Format == formatURLs {
		for _, r := range results {
			fmt.Fprintln(w, r.URL)
		}
		return nil
	}
	pages := make([]*pageContent, len(results))
	for i := range results {
		pages[i] = results[i].Page
	}
	render := func(i int) string {
		var b strings.Builder
		writeRawResult(&b, i, results[i])
		return b.String()
	}
	if opts.Format == formatJSON {
		render = func(i int) string {
			results[i].fillPage()
			return marshalIndent(results[i])
		}
	}
	rendered, pagesTrimmed, charsTrimmed := budgetOutput(opts.MaxOutput, pages, render)
	writeBudgetSummary(errOut, opts, pagesTrimmed, charsTrimmed)
	if opts.Format == formatJSON {
		for i := range results {
			results[i].fillPage()
		}
		return writeJSON(w, results)
	}
	for _, s := range rendered {
		io.WriteString(w, s)
	}
	return nil
}

func writeRawResult(w io.Writer, i int, r rawResult) {
	fmt.Fprintf(w, "[%d] %s — %s\n    %s\n", i+1, titleOf(r.SearchResult), hostOf(r.URL), r.URL)
	if len(r.Engines) > 0 {
		fmt.Fprintf(w, "    Engines: %s\n", strings.Join(r.Engines, ", "))
	}
	if r.Snippet != "" {
		fmt.Fprintf(w, "    %s\n", clipSnippet(r.Snippet, 240))
	}
	writeContent(w, r.Page)
	fmt.Fprintln(w)
}

// budgetOutput trims scraped content so the rendered results fit in max
// runes (0 = unlimited). Results are charged in order; a result whose
// rendering does not fit has its content cut at a paragraph boundary until
// it does, so headers always print and the best results keep their content.
// It returns the renderings and how many pages and runes were trimmed.
func budgetOutput(max int, pages []*pageContent, render func(i int) string) (out []string, pagesTrimmed, charsTrimmed int) {
	out = make([]string, len(pages))
	remaining := max
	for i, p := range pages {
		out[i] = render(i)
		n := utf8.RuneCountInString(out[i])
		for max > 0 && n > remaining && p != nil && p.Content != "" {
			have := utf8.RuneCountInString(p.Content)
			cut := cutAtBoundary(p.Content, have-(n-remaining))
			p.Trimmed += have - utf8.RuneCountInString(cut)
			p.Content = cut
			out[i] = render(i)
			n = utf8.RuneCountInString(out[i])
		}
		if p != nil && p.Trimmed > 0 {
			pagesTrimmed++
			charsTrimmed += p.Trimmed
		}
		remaining -= n
	}
	return out, pagesTrimmed, charsTrimmed
}

// cutAtBoundary returns at most n runes of s, ending at the last paragraph
// break in that window, else the last line break, else the last space.
func cutAtBoundary(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n])
	for _, sep := range []string{"\n\n", "\n", " "} {
		if i := strings.LastIndex(cut, sep); i > 0 {
			return strings.TrimSpace(cut[:i])
		}
	}
	return strings.TrimSpace(cut)
}

func writeBudgetSummary(w io.Writer, opts searchOptions, pages, chars int) {
	if pages == 0 || (opts.Format != formatPretty && !opts.Verbose) {
		return
	}
	fmt.Fprintf(w, "--max-output %d: trimmed %s chars of scraped content from %d page(s)\n", opts.MaxOutput, commas(chars), pages)
	if opts.Format == formatPretty {
		fmt.Fprintln(w)
	}
}

// commas formats n with thousands separators.
func commas(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// marshalIndent renders v as writeJSON would, for measuring.
func marshalIndent(v any) string {
	var b strings.Builder
	_ = writeJSON(&b, v)
	return b.String()
}

// writeContent prints scraped page text under a separator, indented to sit
// inside the result block.
func writeContent(w io.Writer, p *pageContent) {
	if p == nil {
		return
	}
	if p.Err != nil {
		fmt.Fprintf(w, "    ! scrape failed: %v\n", p.Err)
		if !p.Fallback {
			return
		}
		fmt.Fprintln(w, "    (showing the provider's excerpt instead)")
	}
	if p.FilterErr != nil {
		if p.ChunksUnjudged > 0 {
			fmt.Fprintf(w, "    ! chunk filter partly failed: %v\n", p.FilterErr)
		} else {
			fmt.Fprintf(w, "    ! chunk filter failed: %v (showing unfiltered content)\n", p.FilterErr)
		}
	}
	kind := "content"
	if p.PDF {
		kind = "PDF text"
	}
	if p.SummaryErr != nil {
		fmt.Fprintf(w, "    ! summarize failed: %v (showing kept text)\n", p.SummaryErr)
	}
	switch {
	case p.Summarized && p.Content == "":
		fmt.Fprintf(w, "    --- summary: nothing relevant in %d chars of kept text ---\n", p.SummaryFrom)
		fmt.Fprintln(w, "    --- end ---")
		return
	case p.Summarized:
		fmt.Fprintf(w, "    --- summary (%d chars from %d kept; %d/%d chunks) ---\n", len([]rune(p.Content)), p.SummaryFrom, p.ChunksKept, p.ChunksTotal)
	case p.Filtered && p.ChunksUnjudged > 0:
		fmt.Fprintf(w, "    --- %s (%d/%d chunks kept, %d unjudged, %d chars) ---\n", kind, p.ChunksKept, p.ChunksTotal, p.ChunksUnjudged, len([]rune(p.Content)))
	case p.Filtered && p.FilterErr == nil:
		fmt.Fprintf(w, "    --- %s (%d/%d chunks kept, %d chars) ---\n", kind, p.ChunksKept, p.ChunksTotal, len([]rune(p.Content)))
	default:
		fmt.Fprintf(w, "    --- %s (%d chars) ---\n", kind, len([]rune(p.Content)))
	}
	if p.Content == "" && p.Trimmed == 0 {
		fmt.Fprintln(w, "    (no relevant chunks)")
	}
	for _, line := range strings.Split(p.Content, "\n") {
		if line == "" {
			fmt.Fprintln(w)
			continue
		}
		fmt.Fprintf(w, "    %s\n", line)
	}
	if p.Trimmed > 0 {
		fmt.Fprintf(w, "    … (%s more chars trimmed by --max-output)\n", commas(p.Trimmed))
	}
	fmt.Fprintln(w, "    --- end ---")
}

// writeQualified prints ranked results. Non-verbose output includes only kept
// results; verbose output includes everything with its keep/drop decision.
func writeQualified(w, errOut io.Writer, ranked []rankedResult, opts searchOptions) error {
	visible := ranked
	if !opts.Verbose {
		visible = visible[:0:0]
		for _, r := range ranked {
			if r.Kept {
				visible = append(visible, r)
			}
		}
	}
	if opts.Format == formatURLs {
		for _, r := range visible {
			if r.Kept {
				fmt.Fprintln(w, r.Result.URL)
			}
		}
		return nil
	}

	pages := make([]*pageContent, len(visible))
	for i, r := range visible {
		pages[i] = r.Page
	}
	render := func(i int) string {
		var b strings.Builder
		writeQualifiedResult(&b, i, visible[i], opts)
		return b.String()
	}
	if opts.Format == formatJSON {
		render = func(i int) string { return marshalIndent(toOutput(visible[i], opts.Verbose)) }
	}
	rendered, pagesTrimmed, charsTrimmed := budgetOutput(opts.MaxOutput, pages, render)
	writeBudgetSummary(errOut, opts, pagesTrimmed, charsTrimmed)
	if opts.Format == formatJSON {
		items := make([]outputResult, 0, len(visible))
		for _, r := range visible {
			items = append(items, toOutput(r, opts.Verbose))
		}
		return writeJSON(w, items)
	}
	for _, s := range rendered {
		io.WriteString(w, s)
	}
	return nil
}

func writeQualifiedResult(w io.Writer, i int, r rankedResult, opts searchOptions) {
	fmt.Fprintf(w, "[%d] %s — %s\n    %s\n", i+1, titleOf(r.Result), hostOf(r.Result.URL), r.Result.URL)
	switch {
	case r.Err != nil:
		fmt.Fprintf(w, "    ! Jev error: %v\n", r.Err)
	case r.Score != nil:
		fmt.Fprintf(w, "    Score: %.1f/10\n", r.Score.Scaled())
		if opts.Verbose {
			fmt.Fprintf(w, "    Confidence: %.2f", r.Score.Confidence)
			if len(r.Score.Probabilities) > 0 {
				fmt.Fprintf(w, "  Probabilities: %s", r.Score.FormatProbabilities())
			}
			fmt.Fprintln(w)
		}
	case r.Noul != nil:
		fmt.Fprintf(w, "    P(yes): %.2f\n", r.Noul.Probability)
		if opts.Verbose {
			fmt.Fprintf(w, "    Confidence: %.2f\n", r.Noul.Confidence())
		}
	}
	if len(r.Engines) > 0 {
		fmt.Fprintf(w, "    Engines: %s\n", strings.Join(r.Engines, ", "))
	}
	for _, d := range r.Duplicates {
		fmt.Fprintf(w, "    Duplicate: %s\n", d.URL)
	}
	if opts.Verbose {
		switch {
		case r.Backfilled:
			fmt.Fprintf(w, "    ✓ Kept (below the %g cut; backfilled to reach --min-results %d)\n", opts.MinScore, opts.MinResults)
		case r.Kept:
			fmt.Fprintln(w, "    ✓ Kept")
		case r.Err != nil:
			fmt.Fprintln(w, "    ✗ Filtered (not scored)")
		default:
			fmt.Fprintf(w, "    ✗ Filtered (below %g threshold)\n", opts.MinScore)
		}
	}
	if r.Result.Snippet != "" {
		limit := 240
		if opts.Verbose {
			limit = 600
		}
		fmt.Fprintf(w, "    %s\n", clipSnippet(r.Result.Snippet, limit))
	}
	writeContent(w, r.Page)
	fmt.Fprintln(w)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func titleOf(r provider.SearchResult) string {
	if t := strings.TrimSpace(r.Title); t != "" {
		return t
	}
	return "(untitled)"
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "?"
	}
	return strings.TrimPrefix(u.Host, "www.")
}

func clipSnippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}
