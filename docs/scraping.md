# Scraping

`--scrape` fetches the best kept results' pages and reduces them to text. `--filter-chunks` keeps only the parts worth quoting.

When to use it: whenever you would otherwise read a whole page. The snippets alone answer most questions (benchmarks: quality 9.40 without scraping versus 9.47 with, at a third of the payload). Scraping earns its cost on long documents, earnings transcripts, and comment threads, where the answer is a paragraph deep inside the page: webctl does the fetch and only the chunks Jev judges relevant to the query and goal are returned. Read a page directly only when you need it whole.

## Fetching

Only the `--scrape-top` highest-scoring kept results are fetched (default 3; 0 = all). The rest print their snippet, which is usually enough to decide whether to ask for more. Results backfilled by `--min-results` were under the score cut and are never fetched.

Pages are fetched with a 10s timeout, 4 at a time, and converted from HTML to text (scripts, styles, navigation elements dropped; block structure kept as line breaks). Plain text passes through. A page that cannot be fetched (403 bot wall, timeout) falls back to the provider's own excerpt, marked in the output.

A body that is JSON (by content type, or because it starts with `{` or `[` and parses), such as a docs page served as data, is not prose: it is reported as the fetch error `page is JSON, not prose` and the provider's excerpt is used instead.

Boilerplate at the edges of a page is stripped before chunking. A run of four or more short lines (≤ 40 characters) or link rows (`new | past | comments | ask`, tickers joined by `·` or `•`) at the very start or very end of the text is removed: site headers, "Skip to main content", menus, footers. Runs in the middle are left alone, so lists and headings survive; a short line right before the first prose is kept as its heading; a trailing run must contain a link row, so a closing list is not mistaken for a footer; and a page that is mostly short lines is kept whole rather than emptied.

## PDFs

A result that is a PDF (by content type, or by the `%PDF-` signature when a host serves it as octet-stream) has its text layer extracted with a pure-Go parser; a scanned PDF with no text layer is reported as a fetch error and falls back to the excerpt. Because a paper's text runs to tens of thousands of characters, a PDF is always chunk-filtered through Jev when a Jev key is present, even without `--filter-chunks`; only `--no-filter` leaves it whole. The terminal header reads `PDF text (k/n chunks kept, c chars)` and JSON carries `"pdf": true`.

Reddit: `www.reddit.com` answers with a JavaScript challenge page (HTTP 200, no content). The challenge is solved, the resulting cookies are kept for the run, and the comment tree, which ships inside a `<template>` element, is read. Threads come back with post and comments.

## Chunk filtering

Each page is split into chunks of `--chunk-chars` characters (default 2,000) on paragraph boundaries, then sentences, then words. Jev is asked whether each chunk is worth quoting for the query and goal; chunks with P(yes) ≥ 0.5 are kept and rejoined. The defaults follow [Skimmer's measured chunk-filter design](https://github.com/dorkitude/skimmer/tree/main/evals): 2,000-character chunks, 10% boundary context, and a conservative 0.5 threshold. Typical outcome: 20 to 30 percent of text removed (navigation, footers, tangents), themes fully retained. `--max-chars` caps text per page before chunking (default 50,000).

Chunks do not overlap in the output. They do overlap when judged: each chunk is sent to Jev with the last 10% of the previous chunk attached as labeled preceding context, cut at a sentence boundary (or a word boundary if there is none), so a heading or sentence split at a chunk edge is judged with what follows it. The prompt tells Jev the context is not part of the chunk. The kept text is the bare chunk, so nothing is duplicated in your context. Smaller `--chunk-chars` means finer filtering and more Jev questions; the overlap scales with it.

Chunks are packed into batches that stay under an estimated-token budget and sent in parallel, so a long page does not become one request too large for Jev to accept. Each chunk is sent once in request state; its short question points to the chunk by id. This state-pointer design measured 19% fewer input tokens with equal-or-better accuracy in [Skimmer's head-to-head eval](https://github.com/dorkitude/skimmer/tree/main/evals/variants). A batch is capped at 24 chunks regardless. A batch Jev still rejects for size is halved and retried down to a single chunk.

Failure is contained. If some batches fail, the chunks they covered are kept unfiltered rather than dropped — losing a verdict must never silently delete page content — and the run reports `k/n chunks kept, u unjudged` with `chunks_unjudged` in JSON. Only when every batch fails does the page fall back to its whole unfiltered text, reported as before.

## Summarize

`--summarize` hands each page's kept text to a small model (Haiku, Luna, DeepSeek Flash) and prints its goal-focused summary instead. Pluggable: a shell command or an OpenAI-compatible endpoint. See `summarize`.

## Output budget

`--max-output` caps the whole printed output (default 20,000 characters; 0 = unlimited), because an agent's tool-result window is small: Claude Code shows only a 2 KB preview of output past about 30 KB. Every kept result's header (title, URL, score, snippet) always prints; scraped content is then allotted to results in score order, and a page that does not fit is cut at a paragraph boundary with a marker, `… (12,400 more chars trimmed by --max-output)`. No result is dropped for the budget, only its content. In JSON the `content` fields are bounded the same way and `chars_trimmed` reports the cut. A summary line on stderr says how much was trimmed.

## Output

Terminal: a `--- content (k/n chunks kept, c chars) ---` header per result. JSON: `content`, `scrape_error`, `chunks_total`, `chunks_kept`, `filter_error`, `chars_trimmed`.
