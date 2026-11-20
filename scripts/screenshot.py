#!/usr/bin/env python3
"""Headless screenshots of the popover, the icons and the menu bar glyph.

Starts a throwaway HTTP server that serves web/ with mocked API payloads, then
drives the local Chrome build. Output lands in build/screenshots/.

  python3 scripts/screenshot.py
"""

from __future__ import annotations

import contextlib
import http.server
import json
import os
import shutil
import signal
import socketserver
import struct
import subprocess
import threading
import time
import zlib
from datetime import datetime, timedelta, timezone
from pathlib import Path

import make_icons

ROOT = Path(__file__).resolve().parent.parent
WEB = ROOT / "web"
OUT = ROOT / "build" / "screenshots"
APP_PNG = ROOT / "build" / "icon" / "AppIcon.png"
APP_CLASSIC_PNG = ROOT / "build" / "icon" / "AppIcon-classic.png"
ASSETS = ROOT / "cmd" / "gauge" / "assets"

CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"


def now(offset: timedelta = timedelta()) -> str:
    return (datetime.now(timezone.utc) + offset).isoformat().replace("+00:00", "Z")


def fixture_layout() -> list:
    """The resolved /api/overview layout: subscriptions, then their cards."""
    def item(id_, label, hidden=False):
        provider, metric = id_.split("/")
        return {"id": id_, "label": label, "provider": provider, "metric": metric, "hidden": hidden}

    return [
        {"id": "google", "title": "Google AI Ultra", "detail": "Ultra 5x", "hidden": False,
         "slot": 1, "email": "songjian.li@gmail.com", "account_name": "Lisong Jian",
         "picture": "https://lh3.googleusercontent.com/a/fixture",
         "providers": ["antigravity", "google-flow", "colab"],
         "items": [
             item("antigravity/gemini_models", "Antigravity · Gemini"),
             item("antigravity/claude_models", "Antigravity · Claude & GPT"),
             item("antigravity/other_models", "Antigravity · other models", True),
             item("google-flow/flow_credits", "Flow credits"),
             item("colab/compute_units", "Colab compute units"),
         ]},
        {"id": "commandcode", "title": "Command Code", "hidden": False, "providers": ["commandcode"],
         "items": [item("commandcode/plan", "Plan"), item("commandcode/credits", "Credits", True)]},
        {"id": "opendesign", "title": "Open Design", "hidden": False, "providers": ["opendesign"],
         "items": [item("opendesign/plan", "Design Plan"), item("opendesign/balance", "Balance")]},
        {"id": "gcp", "title": "Google Cloud", "hidden": True, "providers": ["gcp-monitoring", "gcp-billing"],
         "items": [item("gcp-billing/month_to_date_cost", "Month to date")]},
    ]


def fixture_overview() -> dict:
    t = now()
    return {
        "generated_at": t,
        "started_at": now(-timedelta(hours=6)),
        "providers": [
            {"name": "antigravity", "title": "Antigravity", "interval_seconds": 300},
            {"name": "commandcode", "title": "Command Code", "interval_seconds": 300},
            {"name": "opendesign", "title": "Open Design", "interval_seconds": 300},
            {"name": "colab", "title": "Colab", "interval_seconds": 1800},
            {"name": "google-flow", "title": "Google Flow", "interval_seconds": 1800},
            {"name": "gcp-billing", "title": "GCP billing", "interval_seconds": 21600},
            {"name": "gcp-monitoring", "title": "GCP monitoring", "interval_seconds": 600},
        ],
        "layout": fixture_layout(),
        "runs": [
            {"provider": "antigravity", "status": "ok", "last_run": now(-timedelta(minutes=2)), "duration_ms": 420},
            {"provider": "commandcode", "status": "ok", "last_run": now(-timedelta(minutes=1)), "duration_ms": 210},
            {"provider": "opendesign", "status": "ok", "last_run": now(-timedelta(minutes=3)), "duration_ms": 380},
            {"provider": "colab", "status": "ok", "last_run": now(-timedelta(minutes=12)), "duration_ms": 610},
            {"provider": "google-flow", "status": "expired_auth", "last_run": now(-timedelta(minutes=20)), "error": "credential rejected: HTTP 401: session expired", "duration_ms": 300},
            {"provider": "gcp-billing", "status": "unconfigured", "last_run": now(-timedelta(hours=1)), "error": "not configured: gcp_billing_table is empty", "duration_ms": 2},
            {"provider": "gcp-monitoring", "status": "ok", "last_run": now(-timedelta(minutes=4)), "duration_ms": 880},
        ],
        "accounts": [
            {
                "slot": 1,
                "email": "songjian.li@gmail.com",
                "name": "Lisong Jian",
                "tier": "Google AI Pro",
                "primary": True,
            }
        ],
        "quotas": [
            {
                "provider": "antigravity",
                "metric": "gemini_models",
                "label": "Gemini models",
                "remaining": 68,
                "total": 100,
                "unit": "%",
                "status": "ok",
                "updated_at": now(-timedelta(seconds=40)),
                "series": [
                    {"label": "5-hour", "remaining": 68, "total": 100, "unit": "%", "reset_at": now(timedelta(hours=2, minutes=13))},
                    {"label": "weekly", "remaining": 82, "total": 100, "unit": "%", "reset_at": now(timedelta(days=3, hours=4))},
                ],
            },
            {
                "provider": "antigravity",
                "metric": "claude_models",
                "label": "Claude & GPT models",
                "remaining": 12,
                "total": 100,
                "unit": "%",
                "status": "warning",
                "updated_at": now(-timedelta(seconds=90)),
                "series": [
                    {"label": "5-hour", "remaining": 12, "total": 100, "unit": "%", "reset_at": now(timedelta(minutes=48))},
                ],
            },
            {"provider": "commandcode", "metric": "plan", "label": "Plan", "remaining": 61, "total": 100, "unit": "%", "status": "ok",
             "account": "abcdlsj", "updated_at": now(-timedelta(minutes=1)),
             "series": [
                 {"label": "5-hour", "remaining": 96.8, "total": 100, "unit": "%", "reset_at": now(timedelta(hours=1, minutes=40))},
                 {"label": "Weekly", "remaining": 61, "total": 100, "unit": "%", "reset_at": now(timedelta(hours=21))},
             ]},
            {"provider": "commandcode", "metric": "credits", "label": "Credits", "remaining": 120, "total": 0, "unit": "credits", "status": "ok", "updated_at": now(-timedelta(minutes=1))},
            {"provider": "opendesign", "metric": "plan", "label": "Design Plan", "remaining": 83.8, "total": 100, "unit": "%", "status": "ok",
             "account": "lisongjianshuai@gmail.com", "updated_at": now(-timedelta(minutes=3)),
             "series": [
                 {"label": "5-hour", "remaining": 100, "total": 100, "unit": "%", "reset_at": now(timedelta(hours=2))},
                 {"label": "Weekly", "remaining": 83.8, "total": 100, "unit": "%", "reset_at": now(timedelta(hours=23))},
             ]},
            {"provider": "opendesign", "metric": "balance", "label": "Balance", "remaining": 0.99, "total": 0, "unit": "USD", "status": "ok", "updated_at": now(-timedelta(minutes=3))},
            {"provider": "colab", "metric": "compute_units", "label": "Compute units", "remaining": 21.5, "total": 100, "unit": "units", "status": "ok", "reset_at": now(timedelta(days=9)), "updated_at": now(-timedelta(minutes=2))},
            {"provider": "google-flow", "metric": "flow_credits", "label": "Flow credits", "remaining": 750, "total": 1000, "unit": "credits", "status": "ok", "reset_at": now(timedelta(days=21)), "updated_at": now(-timedelta(minutes=3))},
            {"provider": "gcp-billing", "metric": "month_to_date_cost", "label": "Month to date", "remaining": 12.4, "total": 0, "unit": "USD", "status": "ok", "updated_at": now(-timedelta(minutes=5))},
        ],
    }


def fixture_overview_fresh() -> dict:
    """A first launch: every collector ran and found nothing to read."""
    ov = fixture_overview()
    ov["accounts"] = []
    ov["quotas"] = []
    ov["runs"] = [{"provider": r["provider"], "status": "unconfigured", "last_run": r["last_run"],
                   "error": "not configured", "duration_ms": 2} for r in ov["runs"]]
    ov["layout"] = [
        {"id": "google", "title": "Google AI", "hidden": False, "providers": ["antigravity", "google-flow", "colab"], "items": []},
        {"id": "commandcode", "title": "Command Code", "hidden": False, "providers": ["commandcode"], "slot": 1, "items": []},
        {"id": "opendesign", "title": "Open Design", "hidden": False, "providers": ["opendesign"], "slot": 1, "items": []},
        {"id": "gcp", "title": "Google Cloud", "hidden": False, "providers": ["gcp-monitoring", "gcp-billing"], "items": []},
    ]
    return ov


def fixture_overview_google_only() -> dict:
    """Only the Google account is signed in; the other sources are untouched."""
    ov = fixture_overview()
    google = {"antigravity", "google-flow", "colab"}
    ov["quotas"] = [q for q in ov["quotas"] if q["provider"] in google]
    ov["runs"] = [r if r["provider"] in google else
                  {"provider": r["provider"], "status": "unconfigured", "last_run": r["last_run"],
                   "error": "not configured", "duration_ms": 2} for r in ov["runs"]]
    fresh = {g["id"]: g for g in fixture_overview_fresh()["layout"]}
    layout = [g if g["id"] == "google" else fresh[g["id"]] for g in ov["layout"]]
    # A second Google identity, so the list shows two photos side by side.
    layout.insert(1, {"id": "google-2", "title": "Google AI Pro", "detail": "Pro", "hidden": False,
                      "slot": 2, "email": "gauge.test@gmail.com", "account_name": "Gauge Test",
                      "picture": "https://lh3.googleusercontent.com/a/fixture-2",
                      "providers": ["antigravity"], "items": []})
    ov["layout"] = layout
    ov["accounts"] = ov["accounts"] + [{"slot": 2, "email": "gauge.test@gmail.com", "name": "Gauge Test",
                                        "tier": "Google AI Pro", "primary": False}]
    return ov


def fixture_settings_fresh() -> dict:
    """Settings with nothing stored: every field back at its default."""
    out = fixture_settings()
    for sec in out["sections"]:
        for f in sec["fields"]:
            if f["kind"] != "number" and f["key"] != "browser_cookies":
                f["configured"] = False
                f["value"] = f.get("default", "")
    return out


def fixtures() -> dict:
    """Fixture variants, picked by ?fixture= on the page URL."""
    return {
        "": (fixture_overview, fixture_settings),
        "fresh": (fixture_overview_fresh, fixture_settings_fresh),
        "google-only": (fixture_overview_google_only, fixture_settings),
    }


def avatar_png(slot: int = 1, size: int = 96) -> bytes:
    """A stand-in profile photo: a head and shoulders on a backdrop that
    differs per slot, so two accounts read as two people."""
    base = [(214, 176, 140), (150, 182, 206), (176, 196, 160)][(slot - 1) % 3]
    rows = []
    for y in range(size):
        row = bytearray([0])
        for x in range(size):
            t = y / size
            r, g, b = int(base[0] - 40 * t), int(base[1] - 30 * t), int(base[2] - 20 * t)
            hx, hy = x - size * 0.5, y - size * 0.40
            if hx * hx + hy * hy < (size * 0.19) ** 2:
                r, g, b = 246, 214, 186
            sx, sy = x - size * 0.5, y - size * 1.02
            if sx * sx / (size * 0.42) ** 2 + sy * sy / (size * 0.40) ** 2 < 1:
                r, g, b = 58, 74, 104
            row += bytes((r, g, b))
        rows.append(bytes(row))

    def chunk(ctype: bytes, data: bytes) -> bytes:
        return struct.pack(">I", len(data)) + ctype + data + struct.pack(">I", zlib.crc32(ctype + data) & 0xFFFFFFFF)

    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", size, size, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(b"".join(rows), 6)) + chunk(b"IEND", b""))


def fixture_proxy() -> dict:
    """The /api/proxy shape: the macOS system proxy is in effect."""
    return {"config": {"setting": "", "url": "http://127.0.0.1:7890", "source": "system", "direct": False}}


def fixture_update() -> dict:
    """The /api/update shape: the running build, up to date."""
    return {"current": "0.4.0", "latest": "0.4.0", "available": False,
            "state": "idle", "progress": 0, "can_apply": False}


def fixture_settings() -> dict:
    """The /api/settings shape: one section per source, plus General."""
    def field(key, label, kind, value=None, **extra):
        out = {"key": key, "label": label, "kind": kind, "configured": value is not None,
               "value": value if value is not None else extra.get("default", ""),
               "sensitive": kind == "secret"}
        out.update(extra)
        return out

    def interval(provider, label, default, floor, value=None):
        return field(f"refresh_{provider}_minutes", label, "number", value, unit="min",
                     default=str(default), min=floor, help=f"Minimum {floor} min.")

    return {
        "sections": [
            {"id": "google", "title": "Google AI", "providers": ["antigravity", "google-flow", "colab"],
             "summary": "Antigravity model quota, Flow credits and Colab compute units of the Google account.",
             "fields": [
                 field("antigravity_refresh_token", "Antigravity refresh token", "secret", "••••x9Qa", optional=True, help="Read only while the IDE is closed. Leave empty to use the Gauge sign-in above."),
                 field("browser_cookies", "Read cookies from Chrome", "switch", "chrome", options=["chrome", "off"], default="chrome", help="Flow and Colab reuse the Chrome sign-in. One keychain prompt."),
                 field("flow_cookie", "Flow cookie", "secret", multiline=True, optional=True),
                 field("colab_cookie", "Colab cookie", "secret", multiline=True, optional=True),
                 interval("antigravity", "Antigravity", 5, 5),
                 interval("google-flow", "Flow", 30, 30),
                 interval("colab", "Colab", 30, 30, "45"),
             ]},
            {"id": "commandcode", "title": "Command Code", "providers": ["commandcode"], "summary": "5-hour and weekly plan windows.",
             "fields": [
                 field("commandcode_api_key", "API key", "secret", optional=True, help="Leave empty to use the Command Code CLI sign-in (~/.commandcode/auth.json)."),
                 interval("commandcode", "Refresh every", 5, 2),
             ]},
            {"id": "opendesign", "title": "Open Design", "providers": ["opendesign"], "summary": "Design Plan windows and USD balance.",
             "fields": [
                 field("opendesign_control_key", "Control key", "secret", optional=True, help="Leave empty to use the Open Design CLI sign-in (~/.amr/config.json)."),
                 interval("opendesign", "Refresh every", 5, 2),
             ]},
            {"id": "gcp", "title": "Google Cloud", "providers": ["gcp-billing", "gcp-monitoring"],
             "summary": "Billing-export credits and Gemini API call counts.",
             "fields": [
                 field("gcp_project_id", "Project ID", "text", "gauge-prod", placeholder="my-project"),
                 field("gcp_billing_table", "Billing export table", "text", placeholder="project.dataset.gcp_billing_export_v1_…", help="Standard usage cost export in BigQuery."),
                 field("gcp_initial_credits", "Initial credits", "number", "447.27", help="Starting balance used to derive the remaining amount."),
                 field("gcp_currency", "Currency", "text", default="HKD", placeholder="HKD"),
                 field("google_refresh_token", "Refresh token", "secret", optional=True, help="Fallback when no ADC is found."),
                 field("gcp_sa_json", "Service account JSON", "secret", multiline=True, optional=True, help="Takes precedence over the refresh token."),
                 interval("gcp-billing", "Billing refresh", 360, 60),
                 interval("gcp-monitoring", "Monitoring refresh", 10, 10),
             ]},
            {"id": "general", "title": "General", "providers": [],
             "fields": [
                 field("proxy_url", "Proxy", "proxy", optional=True, placeholder="http://127.0.0.1:7890"),
                 field("webhook_url", "Alert webhook", "secret", optional=True, placeholder="https://hooks.slack.com/services/…"),
                 field("app_icon", "Icon", "select", options=["gauge", "classic"], default="gauge", help="Menu bar and window mark."),
             ]},
        ]
    }


ICON_ROW = """  <div class="row {tone}">
    <div class="tag">{name} · {tone}</div>
    <figure><img class="shadow" src="/icon/{file}" width="256" height="256"><figcaption>1024 → 256</figcaption></figure>
    <figure><img class="shadow" src="/icon/{file}" width="128" height="128"><figcaption>128</figcaption></figure>
    <figure><img class="shadow" src="/icon/{file}" width="64" height="64"><figcaption>64</figcaption></figure>
    <figure><img class="shadow" src="/icon/{file}" width="32" height="32"><figcaption>32</figcaption></figure>
    <figure><img class="shadow" src="/icon/{file}" width="16" height="16"><figcaption>16</figcaption></figure>
  </div>
"""

ICON_PREVIEW = """<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><style>
  body { margin: 0; font: 12px "Avenir Next", -apple-system, sans-serif; }
  .row { display: flex; align-items: center; gap: 34px; padding: 20px 24px; }
  .row.light { background: #f7f7f4; color: #121417; }
  .row.dark { background: #17181b; color: #f2f3f5; }
  .tag { width: 130px; font-size: 11px; letter-spacing: 0.06em; text-transform: uppercase; opacity: 0.55; }
  figure { margin: 0; display: grid; gap: 6px; justify-items: center; }
  figcaption { font-size: 10px; letter-spacing: 0.06em; opacity: 0.45; }
  img { display: block; }
  .shadow { filter: drop-shadow(0 2px 6px rgba(0, 0, 0, 0.18)); }
</style></head><body>
""" + "".join(
    ICON_ROW.format(name=name, tone=tone, file=file)
    for name, file in (("gauge", "AppIcon.png"), ("classic", "AppIcon-classic.png"))
    for tone in ("light", "dark")
) + """</body></html>
"""


TRAY_PREVIEW = """<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><style>
  body { margin: 0; font: 13px "Avenir Next", -apple-system, sans-serif; }
  .row { position: relative; height: 268px; }
  .wall-light { background: linear-gradient(120deg, #e8e4dc 0%, #cfd8e6 55%, #b4c3d8 100%); }
  .wall-dark  { background: linear-gradient(120deg, #0e1116 0%, #1d2633 55%, #33404f 100%); }
  .bar { position: absolute; inset: 0 0 auto 0; height: 26px; display: flex; align-items: center;
         padding: 0 12px; gap: 18px; font-size: 12px; }
  .bar.light { background: rgba(255, 255, 255, 0.66); color: #1b1d21; backdrop-filter: blur(24px); }
  .bar.dark  { background: rgba(22, 24, 28, 0.66); color: #f2f3f5; backdrop-filter: blur(24px); }
  .bar .grow { flex: 1; }
  .bar img { width: 22px; height: 22px; display: block; }
  .bar.dark img { filter: invert(1); }
  .sample { position: absolute; left: 14px; top: 40px; display: flex; align-items: flex-end; gap: 26px; }
  .box { display: grid; gap: 6px; justify-items: center; }
  .box span { font-size: 10px; letter-spacing: 0.06em; text-transform: uppercase; opacity: 0.6; }
  .card { border-radius: 6px; padding: 5px; display: grid; place-items: center; }
  .card.light { background: #ffffff; box-shadow: 0 1px 3px rgba(0, 0, 0, 0.18); }
  .card.dark { background: #000000; box-shadow: 0 1px 3px rgba(0, 0, 0, 0.4); }
  .card img { display: block; }
  .card.dark img { filter: invert(1); }
  .caption { position: absolute; right: 16px; bottom: 16px; font-size: 11px; letter-spacing: 0.04em;
             text-transform: uppercase; opacity: 0.55; }
  .ink-light { color: #1b1d21; }
  .ink-dark { color: #f2f3f5; }
</style></head><body>
  <div class="row wall-light">
    <div class="bar light">
      <b>Finder</b><span>File</span><span>Edit</span><span>View</span><span class="grow"></span>
      <img src="/tray/tray-template@2x.png" alt="">
      <img src="/tray/tray-classic-template@2x.png" alt="">
      <span>Wed 21:09</span>
    </div>
    <div class="sample ink-light">
      <div class="box"><span>1x</span><div class="card light"><img src="/tray/tray-template.png" width="22" height="22" alt=""></div></div>
      <div class="box"><span>4x</span><div class="card light"><img src="/tray/tray-template@2x.png" width="88" height="88" alt=""></div></div>
      <div class="box"><span>8x</span><div class="card light"><img src="/tray/vector.svg" width="176" height="176" alt=""></div></div>
      <div class="box"><span>classic 1x</span><div class="card light"><img src="/tray/tray-classic-template.png" width="22" height="22" alt=""></div></div>
      <div class="box"><span>4x</span><div class="card light"><img src="/tray/tray-classic-template@2x.png" width="88" height="88" alt=""></div></div>
      <div class="box"><span>8x</span><div class="card light"><img src="/tray/vector-classic.svg" width="176" height="176" alt=""></div></div>
    </div>
    <div class="caption ink-light">light wallpaper, dark ink</div>
  </div>
  <div class="row wall-dark">
    <div class="bar dark">
      <b>Finder</b><span>File</span><span>Edit</span><span>View</span><span class="grow"></span>
      <img src="/tray/tray-template@2x.png" alt="">
      <img src="/tray/tray-classic-template@2x.png" alt="">
      <span>Wed 21:09</span>
    </div>
    <div class="sample ink-dark">
      <div class="box"><span>1x</span><div class="card dark"><img src="/tray/tray-template.png" width="22" height="22" alt=""></div></div>
      <div class="box"><span>4x</span><div class="card dark"><img src="/tray/tray-template@2x.png" width="88" height="88" alt=""></div></div>
      <div class="box"><span>8x</span><div class="card dark"><img src="/tray/vector.svg" width="176" height="176" alt=""></div></div>
      <div class="box"><span>classic 1x</span><div class="card dark"><img src="/tray/tray-classic-template.png" width="22" height="22" alt=""></div></div>
      <div class="box"><span>4x</span><div class="card dark"><img src="/tray/tray-classic-template@2x.png" width="88" height="88" alt=""></div></div>
      <div class="box"><span>8x</span><div class="card dark"><img src="/tray/vector-classic.svg" width="176" height="176" alt=""></div></div>
    </div>
    <div class="caption ink-dark">dark wallpaper, light ink</div>
  </div>
</body></html>
"""


# api_fixture is what the pages would GET from the server. The harness also
# answers those paths over HTTP, but a fetch that lands after Chrome's virtual
# time budget expires yields a screenshot of a half-rendered page, so the
# fixture is injected into the HTML as a fetch shim instead: every GET resolves
# in a microtask, before the first paint.
def api_fixture(variant: str = "") -> dict:
    table = fixtures()
    overview, settings = table.get(variant, table[""])
    return {
        "/api/overview": overview(),
        "/api/settings": settings(),
        "/api/update": fixture_update(),
    }


FETCH_SHIM = """<script>
(function () {
  var fixture = %s;
  var real = window.fetch.bind(window);
  window.fetch = function (input, init) {
    var method = ((init && init.method) || 'GET').toUpperCase();
    if (method !== 'GET') return real(input, init);
    var url = typeof input === 'string' ? input : (input && input.url) || '';
    var path = url;
    if (/^https?:/.test(url)) path = new URL(url).pathname;
    else path = path.split('?')[0];
    if (!Object.prototype.hasOwnProperty.call(fixture, path)) return real(input, init);
    return Promise.resolve(new Response(JSON.stringify(fixture[path]), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    }));
  };
})();
</script>"""


def inject_fixture(html: str, variant: str = "") -> str:
    shim = FETCH_SHIM % json.dumps(api_fixture(variant))
    return html.replace("<head>", "<head>\n" + shim, 1)


class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=str(WEB), **kwargs)

    def log_message(self, *_args):
        pass

    def _send(self, body: bytes, content_type: str) -> None:
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _send_json(self, payload: dict) -> None:
        self._send(json.dumps(payload).encode(), "application/json")

    def _send_file(self, path: Path, content_type: str) -> None:
        self._send(path.read_bytes(), content_type)

    def do_GET(self) -> None:  # noqa: N802
        path = self.path.split("?")[0]
        variant = self.path.partition("fixture=")[2].split("&")[0]
        match path:
            case "/popover.html":
                self._send(inject_fixture((WEB / path.lstrip("/")).read_text(), variant).encode(),
                           "text/html; charset=utf-8")
            case "/api/overview":
                self._send_json(fixture_overview())
            case "/api/settings":
                self._send_json(fixture_settings())
            case "/api/update":
                self._send_json(fixture_update())
            case "/api/proxy":
                self._send_json(fixture_proxy())
            case "/icon/preview.html":
                self._send(ICON_PREVIEW.encode(), "text/html; charset=utf-8")
            case "/icon/AppIcon.png":
                self._send_file(APP_PNG, "image/png")
            case "/icon/AppIcon-classic.png":
                self._send_file(APP_CLASSIC_PNG, "image/png")
            case "/tray/preview.html":
                self._send(TRAY_PREVIEW.encode(), "text/html; charset=utf-8")
            case "/tray/vector.svg":
                self._send(make_icons.tray_svg(176, "gauge").encode(), "image/svg+xml")
            case "/tray/vector-classic.svg":
                self._send(make_icons.tray_svg(176, "classic").encode(), "image/svg+xml")
            case p if p.startswith("/tray/") and p.endswith(".png"):
                self._send_file(ASSETS / p.removeprefix("/tray/"), "image/png")
            case "/mark.svg":
                # Mirrors internal/app/icon.go: ?icon= picks a mark, else the
                # fixture's setting (gauge).
                icon = self.path.partition("icon=")[2].split("&")[0] or "gauge"
                self._send(make_icons.web_mark_path(icon if icon in make_icons.MARKS else "gauge").read_bytes(), "image/svg+xml")
            case p if p.startswith("/api/avatar/"):
                # Home is shot without photos so its baseline stays put;
                # Settings turns them on to show the avatar rows.
                if getattr(self.server, "avatars", False):
                    slot = p.removeprefix("/api/avatar/")
                    self._send(avatar_png(int(slot) if slot.isdigit() else 1), "image/png")
                else:
                    self.send_error(404)
            case _:
                super().do_GET()

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length)
        if self.path.split("?")[0] == "/api/ui/height" and body:
            try:
                h = int(json.loads(body).get("height") or 0)
                if h > 0:
                    self.server.reported_height = max(140, min(780, h))
            except (ValueError, TypeError):
                pass
        self.send_response(204)
        self.end_headers()


def crop_png_height(path: Path, target_h: int) -> None:
    """Crop a non-interlaced PNG to its top target_h pixel rows in-place."""
    raw_file = path.read_bytes()
    sig = b"\x89PNG\r\n\x1a\n"
    if not raw_file.startswith(sig) or target_h <= 0:
        return
    pos = len(sig)
    ihdr = None
    pre_idat: list[tuple[bytes, bytes]] = []
    idat_parts: list[bytes] = []
    while pos + 8 <= len(raw_file):
        length = struct.unpack(">I", raw_file[pos:pos + 4])[0]
        ctype = raw_file[pos + 4:pos + 8]
        cdata = raw_file[pos + 8:pos + 8 + length]
        pos += 12 + length
        if ctype == b"IHDR":
            ihdr = cdata
        elif ctype == b"IDAT":
            idat_parts.append(cdata)
        elif ctype == b"IEND":
            break
        elif not idat_parts:
            pre_idat.append((ctype, cdata))
    if not ihdr or not idat_parts:
        return
    w, h, bit_depth, color_type, comp, flt, interlace = struct.unpack(">IIBBBBB", ihdr)
    channels = {0: 1, 2: 3, 3: 1, 4: 2, 6: 4}.get(color_type)
    if interlace != 0 or not channels or target_h >= h:
        return
    stride = (w * channels * bit_depth + 7) // 8
    pixels = zlib.decompress(b"".join(idat_parts))
    row_bytes = 1 + stride
    if len(pixels) < target_h * row_bytes:
        return
    new_ihdr = struct.pack(">IIBBBBB", w, target_h, bit_depth, color_type, comp, flt, interlace)
    new_idat = zlib.compress(pixels[:target_h * row_bytes], 6)

    def pack_chunk(ctype: bytes, data: bytes) -> bytes:
        crc = zlib.crc32(ctype + data) & 0xFFFFFFFF
        return struct.pack(">I", len(data)) + ctype + data + struct.pack(">I", crc)

    chunks = [sig, pack_chunk(b"IHDR", new_ihdr)]
    for ctype, cdata in pre_idat:
        chunks.append(pack_chunk(ctype, cdata))
    chunks.append(pack_chunk(b"IDAT", new_idat))
    chunks.append(pack_chunk(b"IEND", b""))
    path.write_bytes(b"".join(chunks))


def popover_shot(httpd: socketserver.TCPServer, url: str, out: Path, scale: int = 2) -> None:
    httpd.reported_height = None
    shot(url, out, 372, 780, scale)
    h = getattr(httpd, "reported_height", None)
    if isinstance(h, int) and 140 <= h < 780:
        crop_png_height(out, h * scale)


def shot(url: str, out: Path, width: int, height: int, scale: int = 2) -> None:
    """Screenshot a URL with headless Chrome.

    Chrome writes the PNG but does not always exit on this macOS build, so the
    file is the completion signal and the process is killed once it settles.
    """
    out.parent.mkdir(parents=True, exist_ok=True)
    out.unlink(missing_ok=True)
    proc = subprocess.Popen(
        [
            CHROME,
            "--headless=new",
            "--disable-gpu",
            "--hide-scrollbars",
            "--no-first-run",
            "--no-default-browser-check",
            "--user-data-dir=" + str(OUT / ".chrome"),
            f"--force-device-scale-factor={scale}",
            f"--window-size={width},{height}",
            "--virtual-time-budget=2500",
            f"--screenshot={out}",
            url,
        ],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    )
    try:
        deadline = time.monotonic() + 60
        last_size = -1
        while time.monotonic() < deadline:
            if out.exists():
                size = out.stat().st_size
                if size > 0 and size == last_size:
                    return
                last_size = size
            time.sleep(0.25)
        raise SystemExit(f"screenshot: timed out waiting for {out}")
    finally:
        # Chrome often outlives the screenshot; kill the whole group, but never
        # let cleanup failures hide a rendered file.
        with contextlib.suppress(OSError):
            os.killpg(proc.pid, signal.SIGKILL)
        with contextlib.suppress(OSError):
            proc.kill()
        proc.wait()


def main() -> None:
    if not Path(CHROME).exists():
        raise SystemExit(f"screenshot: Chrome not found at {CHROME}")
    if not APP_PNG.exists() or not APP_CLASSIC_PNG.exists():
        make_icons.build_app_previews()
    OUT.mkdir(parents=True, exist_ok=True)
    shutil.rmtree(OUT / ".chrome", ignore_errors=True)

    with socketserver.TCPServer(("127.0.0.1", 0), Handler) as httpd:
        port = httpd.server_address[1]
        thread = threading.Thread(target=httpd.serve_forever, daemon=True)
        thread.start()
        base = f"http://127.0.0.1:{port}"
        popover_shot(httpd, f"{base}/popover.html?theme=light", OUT / "popover-light.png")
        popover_shot(httpd, f"{base}/popover.html?theme=dark", OUT / "popover-dark.png")
        httpd.avatars = True
        popover_shot(httpd, f"{base}/popover.html?settings=1&theme=light", OUT / "popover-settings.png")
        popover_shot(httpd, f"{base}/popover.html?settings=1&theme=dark", OUT / "popover-settings-dark.png")
        popover_shot(httpd, f"{base}/popover.html?settings=google&theme=light", OUT / "popover-settings-source.png")
        popover_shot(httpd, f"{base}/popover.html?settings=gcp&theme=light", OUT / "popover-settings-gcp.png")
        popover_shot(httpd, f"{base}/popover.html?settings=add&theme=light", OUT / "popover-settings-add.png")
        popover_shot(httpd, f"{base}/popover.html?settings=general&theme=dark", OUT / "popover-settings-general.png")
        popover_shot(httpd, f"{base}/popover.html?settings=general&theme=light", OUT / "popover-settings-general-light.png")
        popover_shot(httpd, f"{base}/popover.html?settings=google/connection&theme=light", OUT / "popover-settings-connection.png")
        popover_shot(httpd, f"{base}/popover.html?settings=gcp/connection&theme=light", OUT / "popover-settings-gcp-connection.png")
        popover_shot(httpd, f"{base}/popover.html?settings=google/schedule&theme=light", OUT / "popover-settings-schedule.png")
        popover_shot(httpd, f"{base}/popover.html?settings=general/webhook&theme=light", OUT / "popover-settings-webhook.png")
        popover_shot(httpd, f"{base}/popover.html?settings=general/network&theme=light", OUT / "popover-settings-network.png")
        popover_shot(httpd, f"{base}/popover.html?settings=add/commandcode&theme=light", OUT / "popover-settings-add-instance.png")
        popover_shot(httpd, f"{base}/popover.html?settings=1&theme=light&fixture=fresh", OUT / "popover-settings-fresh.png")
        popover_shot(httpd, f"{base}/popover.html?settings=add&theme=light&fixture=fresh", OUT / "popover-settings-add-fresh.png")
        popover_shot(httpd, f"{base}/popover.html?settings=1&theme=light&fixture=google-only", OUT / "popover-settings-google-only.png")
        shot(f"{base}/icon/preview.html", OUT / "app-icon.png", 900, 1200)
        shot(f"{base}/tray/preview.html", OUT / "tray-icon-readability.png", 900, 536)
        httpd.shutdown()

    for name in (
        "popover-light.png",
        "popover-dark.png",
        "popover-settings.png",
        "popover-settings-dark.png",
        "popover-settings-source.png",
        "popover-settings-gcp.png",
        "popover-settings-add.png",
        "popover-settings-general.png",
        "popover-settings-general-light.png",
        "popover-settings-connection.png",
        "popover-settings-gcp-connection.png",
        "popover-settings-schedule.png",
        "popover-settings-webhook.png",
        "popover-settings-network.png",
        "popover-settings-add-instance.png",
        "popover-settings-fresh.png",
        "popover-settings-add-fresh.png",
        "popover-settings-google-only.png",
        "app-icon.png",
        "tray-icon-readability.png",
    ):
        print(f"screenshot: {(OUT / name).relative_to(ROOT)}")


if __name__ == "__main__":
    main()
