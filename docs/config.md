# Configuration

Settings resolve in this order: command-line flag, `WEBCTL_*` environment variable, `~/webctl/config.yaml`, built-in default. API keys live in a separate file.

## Settings

| setting | env | default | meaning |
|---|---|---|---|
| `provider` | `WEBCTL_PROVIDER` | (auto) | provider to put first in the chain |
| `sources` | `WEBCTL_SOURCES` | 3 | providers queried and fused per search |
| `num` | `WEBCTL_NUM` | 20 | results requested per provider; Jev removes the noise |
| `min_score` | `WEBCTL_MIN_SCORE` | 6 | relevance cut on the 0–10 score |
| `jev.base_url` | `WEBCTL_JEV_BASE_URL` | `https://api.typesafe.ai` | Jev API root |
| `jev.model` | `WEBCTL_JEV_MODEL` | `jev-latest` | Jev model |
| `searxng_url` | `SEARXNG_URL` | (none) | SearXNG instance; the keys file's `searxng_url` wins |
| (keys file) `degoog_url` | `DEGOOG_URL` | (none) | Degoog instance |
| `keys_file` | `WEBCTL_KEYS_FILE` | `~/secrets/keys.json` | where keys live |
| `cooldown.enabled` | `WEBCTL_COOLDOWN_ENABLED` | true | skip rate-limited providers |
| `cooldown.steps` | `WEBCTL_COOLDOWN_STEPS` | `15m,1h,4h,12h,24h,72h` | window per consecutive failure |
| `cooldown.probe_interval` | `WEBCTL_COOLDOWN_PROBE_INTERVAL` | `24h` | one probe this often at the top step |
| `cooldown.quota_start` | `WEBCTL_COOLDOWN_QUOTA_START` | 3 | step a 402 starts at |
| `summarize.command` | `WEBCTL_SUMMARIZE_COMMAND` | | `--summarize` backend: shell command, prompt on stdin, summary on stdout |
| `summarize.endpoint` | `WEBCTL_SUMMARIZE_ENDPOINT` | | `--summarize` backend: OpenAI-compatible base URL |
| `summarize.model` | `WEBCTL_SUMMARIZE_MODEL` | | model for the endpoint |
| `summarize.api_key_env` | `WEBCTL_SUMMARIZE_API_KEY_ENV` | by host | env var holding the endpoint key |
| `summarize.api_key` | `WEBCTL_SUMMARIZE_API_KEY` | | the key itself (prefer the env var) |
| `summarize.max_tokens` | `WEBCTL_SUMMARIZE_MAX_TOKENS` | 500 | summary length cap |
| `summarize.reasoning_effort` | `WEBCTL_SUMMARIZE_REASONING_EFFORT` | `none` | sent to the endpoint; empty omits it |
| `summarize.timeout` | `WEBCTL_SUMMARIZE_TIMEOUT` | `60s` | per page |

`config.yaml` uses the same names, nested for dots:

```yaml
provider: parallel
sources: 2
min_score: 2.0
jev:
  model: jev-1.13.0
cooldown:
  steps: [30m, 2h, 8h, 24h, 72h]
```

## Commands

```
webctl config show            # every setting, effective value, and source
webctl config get sources
webctl config set sources 2   # validated, written to config.yaml
webctl config unset sources
webctl config list            # settings and their meanings
webctl config path
```

## Keys

`~/secrets/keys.json` (mode 0600), shared with other tools; fields this tool does not know are preserved. Fields: `jev_api_key`, `exa_api_key`, `parallel_api_key`, `sonar_api_key`, `youcom_api_key`, `brave_api_key`, `tavily_api_key`, `linkup_api_key`, `firecrawl_api_key`, `keenable_api_key`, `serpbase_api_key`, `serply_api_key`, `searxng_url`, `degoog_url`. Environment overrides: the same names upper-cased without the `_api` (`JEV_API_KEY`, `EXA_API_KEY`, ... `SEARXNG_URL`, `DEGOOG_URL`); see `providers` for the full table.

```
webctl setup                  # interactive wizard, validates each key
webctl keys list              # what is set and where it came from
webctl keys set exa           # masked prompt; --value for scripts
webctl keys unset exa
webctl keys validate          # one lightweight call per key
```

Setting a key clears that provider's cooldown. The Jev key is required; without it only `--no-filter` searches work. Search keys are optional.

## Files

| path | contents |
|---|---|
| `~/webctl/config.yaml` | settings |
| `~/webctl/cooldown.json` | provider cooldown state |
| `~/secrets/keys.json` | API keys |
| `~/webctl/evals/*.json` | eval runs (one file each) |
