# Gauge

A desktop-first, read-only dashboard for Gemini / Google Cloud quota and spend.
Ships as a macOS app (`make app`) and as a plain HTTP binary for headless use.
Four collectors poll upstream APIs in the background and write snapshots to
embedded SQLite; the page only reads local data, so it opens instantly and never
triggers extra billing or rate limits.

```
cmd/gauge-server/main.go          headless entrypoint (HTTP)
cmd/gauge/main.go    Wails desktop shell
internal/app                shared wiring: store, engine, routes, middleware
internal/store              SQLite snapshots, run state, events, AES-GCM settings
internal/engine             per-provider scheduling, cooldown, alert webhook
internal/providers          antigravity · gcp-billing · gcp-monitoring · colab
internal/browsercookie      read (and decrypt) cookies from the local Chrome profile
web                         index.html · app.css · app.js (no framework)
```

## Quick start

Desktop app (primary):

```sh
make app            # build/bin/Gauge.app (menubar app, no Dock icon)
make run-app
```

The mark is drawn by `scripts/make_icons.py` — one geometry in SVG that becomes
`AppIcon.icns`, the committed 22pt menu-bar template and `web/mark.svg`; run
`make icons` after changing it.
`scripts/screenshot.py` renders the popover in light and dark plus the menu-bar
glyph on both wallpapers into `build/screenshots/`.

Headless server:

```sh
make build          # CGO_ENABLED=0, ~11 MB binary
./bin/gauge        # http://127.0.0.1:8090
```

The menubar build keeps its data in `~/Library/Application Support/Gauge`
and lives in the status bar: click the icon for a 372px popover with the pools,
the small collectors and a compact settings form. The browser build keeps the
full page with top navigation. Both share the components in `app.css`.

Open the panel, expand **Credentials & settings**, paste what you have, then
press **Refresh all**. Nothing needs a restart.

## What it collects

| Provider | Cadence | Source |
| --- | --- | --- |
| `antigravity` | 5 min (configurable) | Cloud Code API — subscription tier, per-model remaining fraction, grouped 5 h / weekly pools |
| `gcp-billing` | 6 h | BigQuery billing export — incremental credit usage, partitioned by `_PARTITIONTIME` |
| `gcp-monitoring` | 10 min | Cloud Monitoring `api/request_count` — Gemini CLI calls today (Pacific) and paid API traffic |
| `colab` | 30 min + 1–5 min jitter | `colab.clients6.google.com` GetUserInfo — compute-unit balance and available accelerator classes |
| `google-flow` | 30 min | `aisandbox-pa.clients6.google.com` VideoFx GetCredits — video credit balance and reset date |

Billing never rescans the table: the last successful timestamp and the running
credit total live in `kv_store`, so each cycle reads at most one extra day of
partitions.

### Antigravity: local first

The IDE ships a language server that already knows the plan, the prompt/flow
credit balances and the model catalogue. While Antigravity is running, the panel
reads it directly — no OAuth, no credentials:

- discovery: `~/.gemini/antigravity/daemon/hub_<port>.json` (pid, ports, CSRF
  token), process liveness checked before use
- RPC: `POST http://127.0.0.1:<port>/exa.language_server_pb.LanguageServerService/GetUserStatus`
  with `X-Codeium-Csrf-Token` and `Connect-Protocol-Version: 1`
- quota windows: the same server's `RetrieveUserQuotaSummary` returns **both**
  groups with a weekly and a 5-hour bucket. The public REST variant of that
  method only returns the third-party group, which is why Gemini was missing
  its weekly window until this call was added.

That is the same path antigravity-panel and AntigravityQuotaWatcher use.

When the IDE is closed, the Cloud Code API is used instead. It only accepts
tokens minted for Antigravity's own OAuth client, so the panel offers a loopback
OAuth flow (PKCE, `access_type=offline`, scopes `cloud-platform`,
`userinfo.email`, `userinfo.profile`, `cclog`, `experimentsandconfigs`):

1. **Settings → Antigravity → Sign in with Google**
2. finish in the popup; the refresh token is encrypted into `kv_store`

Collector behaviour, verified against the OSS clients this was modelled on:

- hosts are tried in order: `daily-cloudcode-pa` → `daily-cloudcode-pa.sandbox`
  → `cloudcode-pa`; auth failures never fall through
- `loadCodeAssist` with `metadata.ideType=ANTIGRAVITY` yields the paid/current
  tier, translated to a readable name (`g1-ultra-lite-tier` → `Gemini Ultra 5x`)
- `fetchAvailableModels` supplies per-model buckets
- `retrieveUserQuotaSummary` supplies the grouped 5-hour / weekly pools
- only the two families that matter are kept (`gemini-*`, `claude-*`); models in
  a family share a pool, so they collapse into one card showing the lowest

## Configuration

Settings are stored in SQLite; anything that looks like a token, cookie, secret
or webhook URL is encrypted with AES-GCM before it is written.

| Key | Purpose |
| --- | --- |
| `google_refresh_token` | Shared OAuth refresh token fallback |
| `antigravity_refresh_token` | Dedicated token for the Antigravity collector |
| `antigravity_creds_path` | Gemini CLI credential file, default `~/.gemini/oauth_creds.json` |
| `gcp_adc_path` | Application Default Credentials, default `~/.config/gcloud/application_default_credentials.json` |
| `google_client_id` / `google_client_secret` | Optional OAuth client override (defaults to the Gemini CLI client) |
| `gcp_sa_json` | Service-account JSON; wins over the refresh token when present |
| `gcp_project_id` | Project used for BigQuery and Monitoring |
| `gcp_billing_table` | `project.dataset.gcp_billing_export_v1_XXXXXX` |
| `gcp_initial_credits` | Starting credit amount, e.g. `447.27` |
| `gcp_currency` | Display currency, e.g. `HKD` (overridden by BigQuery when it reports one) |
| `gemini_cli_daily_limit` | Daily CLI ceiling, default `2000` |
| `colab_endpoint` | JSON endpoint used by the Colab web UI (copy from the Network tab) |
| `browser_cookies` | `chrome` (default) reads and decrypts the local Chrome profile; `off` uses the pasted cookie |
| `colab_cookie` | Fallback cookie when browser reading is off |
| `colab_idle_threshold_min` | Warn when an accelerator runs longer than this, default `90` |
| `webhook_url` | Receives a JSON alert when a provider changes state |
| `antigravity_endpoint` | Optional override of the Cloud Code quota endpoint |

### Credential discovery

Two independent token chains are kept, because Antigravity and Cloud APIs
usually belong to different Google accounts.

| Source | Precedence |
| --- | --- |
| Antigravity | `antigravity_refresh_token` → `google_refresh_token` → Gemini CLI credential file |
| GCP | `gcp_sa_json` → ADC file (service account) → `gcp_refresh_token` → `google_refresh_token` → ADC file (user token) |

Local files are read, never written: the CLI that owns a token keeps rotating
it. `x-goog-user-project` is sent for user credentials so quota is attributed to
`gcp_quota_project` (defaults to `gcp_project_id`).

The encryption key comes from `GAUGE_SECRET_KEY` (hashed) or, when unset, from
`gauge.key` next to the database, generated on first start with mode `0600`.

## Environment

| Variable | Default | Meaning |
| --- | --- | --- |
| `GAUGE_ADDR` | `127.0.0.1:8090` | Listen address |
| `GAUGE_DB` | `quota.db` | SQLite file |
| `GAUGE_KEY` | `<db dir>/gauge.key` | Encryption key file |
| `GAUGE_AUTH` | — | `user:password` for HTTP Basic auth |
| `GAUGE_ALLOW_PUBLIC` | — | Set to `1` to allow a non-loopback address without auth |
| `HTTPS_PROXY` / `NO_PROXY` | — | Honoured by every collector |

The process refuses to bind a non-loopback address unless `GAUGE_AUTH` is set
(or `GAUGE_ALLOW_PUBLIC=1` is explicitly given).

## Refresh cadence

Each collector has a floor so the panel cannot be pointed at the upstream APIs at
an abusive rate. Override under **Settings → Refresh**:

| Collector | Default | Floor |
| --- | --- | --- |
| Antigravity | 5 min | 5 min |
| Colab | 30 min | 30 min |
| Google Flow | 30 min | 30 min |
| Cloud Monitoring | 10 min | 10 min |
| GCP billing | 6 h | 60 min |

## API

| Route | Description |
| --- | --- |
| `GET /api/overview` | Everything the page needs: quotas, runs, events, configured keys |
| `GET /api/quotas` | Snapshot list only |
| `GET/POST /api/settings` | Read field state (secrets masked) / write values |
| `POST /api/refresh/{provider}` | Collect one provider, 60 s cooldown, `429` when hot |
| `POST /api/refresh-all` | Collect every provider that is not cooling down |
| `GET /api/oauth/start` | Returns the Google consent URL for the sign-in flow |
| `GET /oauth/callback` | Loopback OAuth redirect, stores the refresh token |
| `GET /api/health` | Uptime probe, no upstream calls |

## Cookie-authenticated collectors

Colab and Google Flow are gRPC-Web APIs that accept the browser session instead
of OAuth. Both are called the same way:

```
POST https://<service>.clients6.google.com/$rpc/<proto.Service>/<Method>
  content-type: application/json+protobuf
  authorization: SAPISIDHASH <ts>_<sha1("<ts> <SAPISID> <origin>")> ...
  x-goog-api-key: <the key the frontend ships>
  body: [null,1]
```

Two details matter and both cost real debugging time:

- duplicate cookie names across host keys make Google reject the whole header;
  the browser reader keeps the most specific live entry per name
- each product has its own API key; Colab's key against Flow answers 403
  "blocked"

## Browser cookies

Colab (and any future cookie-authenticated source) can borrow the browser
session instead of a pasted string. On macOS the panel reads
`~/Library/Application Support/Google/Chrome/<profile>/Cookies`, decrypts the
`v10` values with AES-128-CBC, and derives the key from the `Chrome Safe
Storage` keychain item via PBKDF2-SHA1. One keychain prompt, then the key stays
in memory.

Nothing is written to disk: values live in memory for the duration of a request
and the clipboard-friendly `colab_cookie` field remains as the fallback.

## Notes

- Colab has no public API. The collector reads the same endpoint the browser
  uses, authenticated with your cookie, and parses the JSON structurally. If the
  endpoint changes, update `colab_endpoint`; nothing else needs rebuilding.
  A rejected cookie pauses Colab polling (`expired_auth`) until a new cookie is
  saved.
- `antigravity` uses the OAuth client that ships with Gemini CLI by default.
  Supply your own `google_client_id` / `google_client_secret` if you prefer.
- The dashboard loads no third-party assets; the CSP is `default-src 'self'`.
- Google Flow is **not** integrated. It moved to `flow.google.com` (Angular SPA),
  the old `labs.google/fx/api/auth/session` endpoint answers 410, and the
  public Flow MCP projects drive generation without exposing a balance call.
  Wiring it up needs one real request captured from the browser.
