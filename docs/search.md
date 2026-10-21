# Search

Recommended form:

```
webctl search "final score san francisco giants september 19th baseball" \
  --goal "The score of the Giants game on the night of September 19th."
```

The query is what goes to the search engines: phrase it the way a search box wants. The goal is what you actually need: phrase it the way you would tell a colleague. Every Jev judge (relevance scoring, chunk filtering, duplicate confirmation) sees both, as `Query:` and `Goal:` lines, and judges against the goal. A bare `webctl "<query>"` is the same pipeline without a goal.

The pipeline: search providers → fold exact duplicates → Jev scores each result → fold near-duplicates → apply the threshold → optionally scrape and chunk-filter → print.

## Pipeline

1. **Search.** Up to `sources` providers (default 3) are queried in concurrent waves and their lists fused by reciprocal rank. See `providers`.
2. **Exact dedupe.** Results with the same normalized URL or title collapse to one. See `dedupe`.
3. **Score.** Jev scores each result 0–10 on topic and source quality. See `filtering`.
4. **Near-dedupe.** MinHash proposes look-alike pairs; Jev confirms them in one request; groups keep the best copy.
5. **Threshold.** Results below `min_score` (default 6) are dropped. `--verbose` shows them anyway, marked.
6. **Scrape** (optional). The top kept pages are fetched; `--filter-chunks` returns only the chunks relevant to the goal. Use it whenever you would otherwise read a whole page. See `scraping`.
7. **Print.** Terminal text, `--json`, or `--urls-only`, within `--max-output` characters.

## Flags

| flag | effect |
|---|---|
| `-g, --goal TEXT` | what you actually need; shown to every judge next to the query |
| `-n, --num N` | results to request from each provider (default 20; setting `num`) |
| `--sources N` | providers to query and fuse (default 3; setting `sources`); `-p` forces 1 |
| `-p, --provider NAME` | exactly one provider: exa, parallel, sonar, youcom, ddg, searxng |
| `--multi` | every available provider |
| `--random` | every available provider, tried in random order |
| `-m, --min-score X` | keep results scoring ≥ X out of 10 (default 6); with `--noul`, minimum P(yes) (default 0.5) |
| `--min-results N` | if fewer than N pass the cut, promote the best of the rest (never off-topic); promoted results are marked `backfilled` |
| `--rubric "a,b,c"` | custom score levels, lowest to highest; default cut is 0.2 below the second-highest level |
| `--noul "question?"` | ask a yes/no question per result instead of scoring |
| `--batch` | score every result in one Jev request instead of one request per result |
| `--no-filter` | skip Jev; print provider results as fused |
| `--no-dedupe` | skip the Jev near-duplicate pass (exact dedupe still runs) |
| `--scrape` | fetch page text for the top results; use with `--filter-chunks` whenever you would otherwise read a whole page |
| `--filter-chunks` | with `--scrape`, return only the chunks Jev judges relevant to the goal |
| `--scrape-top N` | with `--scrape`, fetch only the N best-scoring kept results (default 3; 0 = all); the rest print their snippet; backfilled results are never fetched |
| `--max-output N` | cap the printed output at N characters (default 20000; 0 = unlimited); headers always print, scraped content is allotted top-down and cut at a paragraph boundary with a marker |
| `--max-chars N` | with `--scrape`, cap text per page (default 50000) |
| `--summarize` | with `--scrape`, replace each page's kept text with a short summary from a small model; see `summarize` |
| `--summarize-command CMD`, `--summarize-model NAME` | one-run overrides of `summarize.command` and `summarize.model` |
| `--chunk-chars N` | with `--filter-chunks`, chunk size in characters (default 2000); each chunk is judged with 10% overlap from the previous one |
| `--json` | JSON array on stdout; diagnostics stay on stderr |
| `--urls-only` | one URL per line |
| `-v, --verbose` | show scores, probabilities, dropped results, and every cooldown notice |
| `--config-dir DIR` | config directory (default `~/webctl`) |
| `--keys-file FILE` | keys file (default `~/secrets/keys.json`) |

## Output

Terminal: one block per kept result with title, host, URL, score out of 10, engines, snippet, and `Duplicate:` lines for folded copies. `--verbose` adds Jev's confidence and the per-level probabilities. A summary line on stderr: `exa+parallel: 15 results → 6 kept (min score 1.8)`, then `N duplicate(s) folded` and Jev token usage; with `--scrape`, a scrape summary and, when content was cut, `--max-output 20000: trimmed 31,200 chars of scraped content from 2 page(s)`.

`--json`: an array of objects with `title`, `url`, `snippet`, `score` (0–10), `kept`, `engines`, `duplicates`, and with `--verbose` `confidence` and `probabilities`, and with `--scrape` `content`, `scrape_error`, `chunks_total`, `chunks_kept`, `filter_error`, and `chars_trimmed` when `--max-output` cut the content. With `--noul`: `yes` and `probability` instead of score fields. With `--no-filter`: `title`, `url`, `snippet`, `content`, `engines`.

## Exit codes

0 success (including zero kept results), 1 any failure: no provider answered, Jev unreachable or key rejected, bad flags. Fall-throughs and cooldown skips are not failures.

## Examples

```
webctl search "postgres autovacuum tuning high update tables" --goal "Concrete autovacuum settings for a table that takes thousands of updates a minute"
webctl search "DPO vs RLHF PPO" --goal "Where DPO falls short of PPO-based RLHF" -n 20 -m 8.5 --json | jq '.[].url'
webctl search "sparse autoencoders interpretability" --noul "Is this a peer-reviewed paper?"
webctl search "kubernetes OOMKilled below memory limit" --goal "Why a pod is OOMKilled while under its limit, and how to tell" --scrape --filter-chunks
webctl "why is my Go http server leaking goroutines"     # bare form, no goal
```
