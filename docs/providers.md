# Providers

Fifteen search backends. Jev, the filter, is the one key you must have; every search backend is optional.

## Order

Providers are taken in this order, skipping any that are cooling down (see `cooldowns`):

1. A `provider` set in config, if any.
2. Your own metasearch: `searxng`, then `degoog`, when their URLs are set.
3. Providers you set a key for, in the order brave, exa, parallel, sonar, youcom, tavily, linkup, firecrawl, keenable, serpbase, serply. Brave is the author's pick: 5,000 free searches a month with a key, about 130 ms per query.
4. Only when no key is set at all: the hosted keyless endpoints `parallel`, `exa`, `keenable`, `youcom`, `firecrawl`, then `ddg`. Each throttles by IP after a few dozen queries a day; cooldowns rotate past the throttled ones.

Setting a key is a choice of engine: as soon as one exists, the free tiers leave the chain. With one key and `sources: 3`, one provider answers.

`sources` (default 3) providers are queried at once; a provider that errors or answers empty is replaced by the next. Lists are fused by reciprocal rank (k=60) and each result carries the engines that returned it. `--sources 1` restores a plain fallback chain. Timeouts: 12s per attempt, 30s per search.

`ketch` is not in any default chain: it is a separate binary (`brew install ketch`) that rotates through the same keyless endpoints, so webctl calls them directly instead. Name it with `-p ketch` if you have it installed.

## Backends

| name | keyless | key / URL setting | env | notes |
|---|---|---|---|---|
| `ketch` | yes, if installed | none (ketch's own config) | | not in the default chain; shells out to `ketch search --json`; ketch tries Parallel, Exa, Keenable, You.com, Firecrawl, then DuckDuckGo. Install: `brew install ketch`. Matched this tool's keyless chain on the eval suite at about half the latency |
| `searxng` | your instance | `searxng` | `SEARXNG_URL` | no quota; results depend on the engines it aggregates; see `searxng` |
| `degoog` | your instance | `degoog` | `DEGOOG_URL` | self-hosted Google-style metasearch, `GET /api/search`; no result count parameter |
| `ddg` | yes | none | | DuckDuckGo HTML endpoint; unofficial, soft-blocks around 30/min per IP and sometimes refuses an address outright; cookie jar and 202 retry built in; short snippets |
| `exa` | yes, ~50/day per IP | `exa` | `EXA_API_KEY` | neural index; best on research and code queries; 2–8K-char excerpts. Keyed: $20 intro credit, $10/month free, $7 per 1,000, 10 QPS |
| `parallel` | yes, unpublished daily limit | `parallel` | `PARALLEL_API_KEY` | ~0.7s, 2.8K-char excerpts, 10 results; keyed $1 per 1,000 (fast), 600/min |
| `youcom` | yes, ~70/day observed | `youcom` | `YOUCOM_API_KEY` | keyword results; keyed $5 per 1,000, $100 signup credit, 10/s |
| `sonar` | no | `sonar` | `SONAR_API_KEY` | Perplexity; runs an LLM, 90s timeout |
| `brave` | no | `brave` | `BRAVE_API_KEY` | recommended; up to 20 results per query, 50/s; free tier of 5,000 searches a month, then $5 per 1,000 |
| `tavily` | no | `tavily` | `TAVILY_API_KEY` | agent-oriented, results carry extracted text; 1,000 credits a month free, a basic search is 1 credit, 1/s |
| `linkup` | no | `linkup` | `LINKUP_API_KEY` | agent search; results carry page text; `fast` + `searchResults` is $5 per 1,000 |
| `firecrawl` | hosted, IP-gated (often 429/403) | `firecrawl` | `FIRECRAWL_API_KEY` | v2 search; a key lifts the gate; a self-hosted URL is not supported here |
| `keenable` | yes, hourly cap | `keenable` | `KEENABLE_API_KEY` | index built for agents; ~1.8K-char page text per result; no result count parameter |
| `serpbase` | no | `serpbase` | `SERPBASE_API_KEY` | Google results; about 10 per request; business errors arrive as HTTP 200 with status 1001 (bad key), 1020 (credits), 1029 (rate limited) and are mapped to 401/402/429 |
| `serply` | no | `serply` | `SERPLY_API_KEY` | Google results; at most 10 per request |

Keyed use of a provider promotes it into the chain and lifts the keyless caps. Setting a key clears that provider's cooldown. `keys validate` makes one lightweight call per configured provider.

## Adding a key

```
webctl keys set brave            # masked prompt
webctl keys set tavily --value tvly-...
webctl keys set searxng --value http://localhost:8899
webctl keys validate
```

Or the environment variable from the table, or `setup` for a guided pass over all of them.

## What Jev sees

Each result reaches Jev as title, URL, and a snippet of up to 600 characters. When a provider returns page text (Exa, Parallel, Tavily, Linkup, Keenable, ketch), the snippet starts at the first line that reads like prose, skipping navigation and bylines, and the full excerpt is kept as `content`: it stands in for a page that cannot be scraped and feeds the near-duplicate pass.
