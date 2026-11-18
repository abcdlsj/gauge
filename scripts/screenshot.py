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
import subprocess
import threading
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path

import make_icons

ROOT = Path(__file__).resolve().parent.parent
WEB = ROOT / "web"
OUT = ROOT / "build" / "screenshots"
APP_PNG = ROOT / "build" / "icon" / "AppIcon.png"
TRAY_PNG = ROOT / "cmd" / "gauge" / "assets" / "tray-template.png"
TRAY_PNG_2X = ROOT / "cmd" / "gauge" / "assets" / "tray-template@2x.png"

CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"


def now(offset: timedelta = timedelta()) -> str:
    return (datetime.now(timezone.utc) + offset).isoformat().replace("+00:00", "Z")


def fixture_overview() -> dict:
    t = now()
    return {
        "generated_at": t,
        "started_at": now(-timedelta(hours=6)),
        "providers": [
            {"name": "gemini-cli", "title": "Gemini CLI", "interval_seconds": 300},
            {"name": "claude", "title": "Claude", "interval_seconds": 300},
            {"name": "colab", "title": "Colab", "interval_seconds": 900},
            {"name": "google-flow", "title": "Google Flow", "interval_seconds": 900},
            {"name": "gcp-billing", "title": "GCP Billing", "interval_seconds": 3600},
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
                "provider": "gemini-cli",
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
                "provider": "claude",
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
            {"provider": "colab", "metric": "compute_units", "label": "Compute units", "remaining": 21.5, "total": 100, "unit": "units", "status": "ok", "reset_at": now(timedelta(days=9)), "updated_at": now(-timedelta(minutes=2))},
            {"provider": "google-flow", "metric": "flow_credits", "label": "Flow credits", "remaining": 750, "total": 1000, "unit": "credits", "status": "ok", "reset_at": now(timedelta(days=21)), "updated_at": now(-timedelta(minutes=3))},
            {"provider": "gcp-billing", "metric": "month_to_date_cost", "label": "Month to date", "remaining": 12.4, "total": 0, "unit": "USD", "status": "ok", "updated_at": now(-timedelta(minutes=5))},
        ],
    }


def fixture_settings() -> dict:
    """Enough rows that the form has to scroll inside the 780pt cap."""
    def text(key, label, group, value=None, placeholder=None):
        field = {"key": key, "label": label, "group": group, "kind": "text", "configured": value is not None}
        if value is not None:
            field["value"] = value
        if placeholder is not None:
            field["placeholder"] = placeholder
        return field

    def secret(key, label, group, configured):
        return {"key": key, "label": label, "group": group, "kind": "password", "sensitive": True, "configured": configured}

    def select(key, label, group, options, value):
        return {"key": key, "label": label, "group": group, "kind": "select", "options": options, "configured": True, "value": value, "sensitive": False}

    return {
        "fields": [
            secret("gemini_cookie", "Cookie", "Gemini CLI", True),
            text("gemini_project", "Project", "Gemini CLI", value="gauge-prod"),
            text("gemini_model", "Model", "Gemini CLI", value="gemini-2.5-pro"),
            select("gemini_interval", "Cadence", "Gemini CLI", ["5m", "15m", "1h"], "5m"),
            secret("claude_cookie", "Cookie", "Claude", False),
            text("claude_org", "Organisation", "Claude", placeholder="org-…"),
            secret("antigravity_cookie", "Session", "Antigravity", True),
            text("antigravity_dir", "Gemini dir", "Antigravity", value="~/.gemini"),
            secret("flow_cookie", "Cookie", "Google Flow", True),
            text("flow_project", "Project", "Google Flow", value="flow-lab"),
            text("flow_quota", "Quota key", "Google Flow", placeholder="flow.credits"),
            select("colab_region", "Region", "Colab", ["us-central1", "europe-west4"], "us-central1"),
            text("colab_project", "Project", "Colab", value="colab-runtime"),
            secret("gcp_json", "Service account", "GCP Billing", False),
            text("gcp_project", "Billing project", "GCP Billing", value="gauge-prod"),
            text("gcp_dataset", "BigQuery dataset", "GCP Billing", placeholder="billing_export"),
            text("webhook_url", "Webhook", "Notifications", placeholder="https://…"),
        ]
    }


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
  <div class="row light">
    <div class="tag">app icon · light</div>
    <figure><img class="shadow" src="/icon/AppIcon.png" width="256" height="256"><figcaption>1024 → 256</figcaption></figure>
    <figure><img class="shadow" src="/icon/AppIcon.png" width="128" height="128"><figcaption>128</figcaption></figure>
    <figure><img class="shadow" src="/icon/AppIcon.png" width="64" height="64"><figcaption>64</figcaption></figure>
    <figure><img class="shadow" src="/icon/AppIcon.png" width="32" height="32"><figcaption>32</figcaption></figure>
    <figure><img class="shadow" src="/icon/AppIcon.png" width="16" height="16"><figcaption>16</figcaption></figure>
  </div>
  <div class="row dark">
    <div class="tag">app icon · dark</div>
    <figure><img class="shadow" src="/icon/AppIcon.png" width="256" height="256"><figcaption>1024 → 256</figcaption></figure>
    <figure><img class="shadow" src="/icon/AppIcon.png" width="128" height="128"><figcaption>128</figcaption></figure>
    <figure><img class="shadow" src="/icon/AppIcon.png" width="64" height="64"><figcaption>64</figcaption></figure>
    <figure><img class="shadow" src="/icon/AppIcon.png" width="32" height="32"><figcaption>32</figcaption></figure>
    <figure><img class="shadow" src="/icon/AppIcon.png" width="16" height="16"><figcaption>16</figcaption></figure>
  </div>
</body></html>
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
      <span>Wed 21:09</span>
    </div>
    <div class="sample ink-light">
      <div class="box"><span>1x</span><div class="card light"><img src="/tray/tray-template.png" width="22" height="22" alt=""></div></div>
      <div class="box"><span>4x</span><div class="card light"><img src="/tray/tray-template@2x.png" width="88" height="88" alt=""></div></div>
      <div class="box"><span>8x</span><div class="card light"><img src="/tray/vector.svg" width="176" height="176" alt=""></div></div>
    </div>
    <div class="caption ink-light">light wallpaper, dark ink</div>
  </div>
  <div class="row wall-dark">
    <div class="bar dark">
      <b>Finder</b><span>File</span><span>Edit</span><span>View</span><span class="grow"></span>
      <img src="/tray/tray-template@2x.png" alt="">
      <span>Wed 21:09</span>
    </div>
    <div class="sample ink-dark">
      <div class="box"><span>1x</span><div class="card dark"><img src="/tray/tray-template.png" width="22" height="22" alt=""></div></div>
      <div class="box"><span>4x</span><div class="card dark"><img src="/tray/tray-template@2x.png" width="88" height="88" alt=""></div></div>
      <div class="box"><span>8x</span><div class="card dark"><img src="/tray/vector.svg" width="176" height="176" alt=""></div></div>
    </div>
    <div class="caption ink-dark">dark wallpaper, light ink</div>
  </div>
</body></html>
"""


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
        match path:
            case "/api/overview":
                self._send_json(fixture_overview())
            case "/api/settings":
                self._send_json(fixture_settings())
            case "/icon/preview.html":
                self._send(ICON_PREVIEW.encode(), "text/html; charset=utf-8")
            case "/icon/AppIcon.png":
                self._send_file(APP_PNG, "image/png")
            case "/tray/preview.html":
                self._send(TRAY_PREVIEW.encode(), "text/html; charset=utf-8")
            case "/tray/vector.svg":
                self._send(make_icons.tray_svg(176).encode(), "image/svg+xml")
            case "/tray/tray-template.png":
                self._send_file(TRAY_PNG, "image/png")
            case "/tray/tray-template@2x.png":
                self._send_file(TRAY_PNG_2X, "image/png")
            case p if p.startswith("/api/avatar/"):
                self.send_error(404)
            case _:
                super().do_GET()

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length") or 0)
        self.rfile.read(length)
        self.send_response(204)
        self.end_headers()


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
    if not APP_PNG.exists():
        make_icons.build_app_icon()
    OUT.mkdir(parents=True, exist_ok=True)
    shutil.rmtree(OUT / ".chrome", ignore_errors=True)

    with socketserver.TCPServer(("127.0.0.1", 0), Handler) as httpd:
        port = httpd.server_address[1]
        thread = threading.Thread(target=httpd.serve_forever, daemon=True)
        thread.start()
        base = f"http://127.0.0.1:{port}"
        shot(f"{base}/popover.html?theme=light", OUT / "popover-light.png", 372, 720)
        shot(f"{base}/popover.html?theme=dark", OUT / "popover-dark.png", 372, 720)
        shot(f"{base}/popover.html?settings=1&theme=light", OUT / "popover-settings.png", 372, 780)
        shot(f"{base}/icon/preview.html", OUT / "app-icon.png", 900, 720)
        shot(f"{base}/tray/preview.html", OUT / "tray-icon-readability.png", 760, 536)
        httpd.shutdown()

    for name in (
        "popover-light.png",
        "popover-dark.png",
        "popover-settings.png",
        "app-icon.png",
        "tray-icon-readability.png",
    ):
        print(f"screenshot: {(OUT / name).relative_to(ROOT)}")


if __name__ == "__main__":
    main()
