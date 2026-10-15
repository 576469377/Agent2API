#!/usr/bin/env python3
"""Read-only console/storage diagnostics. Never emit credentials or raw API data."""
import argparse
from collections import Counter
from datetime import datetime, timezone
import json
from pathlib import Path
import re
import time
import urllib.error
import urllib.request
from urllib.parse import urlsplit

LIMIT = 64 * 1024 * 1024


def number(value):
    return value if type(value) in (int, float) and 0 <= value < 1e18 else 0


def identifier(value):
    return value if isinstance(value, str) and re.fullmatch(r"[a-zA-Z0-9_./:-]{1,128}", value) else "other"


def stamp(value):
    try:
        date = datetime.fromisoformat(value.replace("Z", "+00:00"))
        return date.astimezone(timezone.utc).isoformat() if date.tzinfo else None
    except (AttributeError, ValueError, TypeError):
        return None


def usage_summary(data):
    history = data.get("usage_history") or {}
    days = sorted({row["date"] for row in history.get("days", [])
                   if isinstance(row, dict) and re.fullmatch(r"\d{4}-\d{2}-\d{2}", str(row.get("date", "")))})
    recent_dates = sorted({date[:10] for row in data.get("recent", [])
                           if isinstance(row, dict) and (date := stamp(row.get("time")))})
    return {"requests": number(data.get("total")),
            "tokens": number(data.get("input_tokens")) + number(data.get("output_tokens")),
            "daily_history": "usage_history" in data, "dates": days,
            "recent_records": len(data.get("recent") or []), "recent_dates": recent_dates,
            "saved_at": number(data.get("saved_at"))}


def model_summary(data, now=None):
    now = now or datetime.now(timezone.utc)
    groups = {}
    for row in data.get("catalog_status", []):
        groups[identifier(row.get("platform"))] = {"catalog_status": identifier(row.get("status")), "catalog_error": bool(row.get("error"))}
    for model in data.get("models", []):
        group = groups.setdefault(identifier(model.get("platform")), {})
        group["models"] = group.get("models", 0) + 1
        pricing = model.get("pricing") or {}
        observed = stamp(pricing.get("observed_at"))
        valid = observed and -300 <= (now - datetime.fromisoformat(observed)).total_seconds() <= 300
        valid = valid and pricing.get("source") == "upstream_config" and not group.get("catalog_error")
        rate = pricing.get("credit_multiplier")
        kind = ("zero_credits" if rate == 0 else "paid") if valid and type(rate) in (int, float) and rate >= 0 else "unknown_or_variable"
        group[kind] = group.get(kind, 0) + 1
    return groups


def account_summary(data):
    groups = {}
    for row in data.get("accounts", []):
        group = groups.setdefault(identifier(row.get("platform")), {"accounts": 0, "healthy": 0, "states": Counter(), "limited_models": Counter()})
        group["accounts"] += 1
        group["healthy"] += row.get("healthy") is True
        group["states"][identifier(row.get("state"))] += 1
        for model, seconds in (row.get("model_cooldowns") or {}).items():
            if number(seconds) > 0:
                group["limited_models"][identifier(model)] += 1
    return groups


def file_summary(path):
    out = {"file": str(path.resolve()), "exists": path.is_file()}
    if not out["exists"]:
        return out
    try:
        stat = path.stat()
        out.update(bytes=stat.st_size, modified_at=datetime.fromtimestamp(stat.st_mtime, timezone.utc).isoformat())
        if stat.st_size > LIMIT:
            out["read_status"] = "too_large"
        else:
            data = json.loads(path.read_text(encoding="utf-8-sig"))
            if not isinstance(data, dict):
                raise ValueError()
            out.update(read_status="ok", **usage_summary(data))
    except (OSError, ValueError, TypeError, AttributeError):
        out["read_status"] = "unreadable_or_invalid"
    return out


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def get_json(base, endpoint, key, timeout):
    started = time.monotonic()
    request = urllib.request.Request(base + endpoint, headers={"Authorization": "Bearer " + key} if key else {})
    # No redirects or environment proxies: never forward a gateway key elsewhere.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    meta = {}
    data = {}
    try:
        with opener.open(request, timeout=timeout) as response:
            meta["http_status"] = response.status
            raw = response.read(LIMIT + 1)
            if len(raw) > LIMIT:
                raise ValueError()
            data = json.loads(raw)
            if not isinstance(data, dict):
                raise ValueError()
    except urllib.error.HTTPError as error:
        meta["http_status"] = error.code
    except Exception:
        meta["read_status"] = "unreachable_or_invalid"
    meta["elapsed_ms"] = round((time.monotonic() - started) * 1000)
    return data, meta


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", default="config.json", help="Local gateway config; key is read internally and never printed")
    parser.add_argument("--url", help="Gateway URL; default uses local config")
    parser.add_argument("--metrics-file", help="Actual -metrics-file override, if used")
    parser.add_argument("--timeout", type=float, default=25)
    args = parser.parse_args()
    config_path = Path(args.config)
    try:
        cfg = json.loads(config_path.read_text(encoding="utf-8-sig")) if config_path.is_file() else {}
        server = cfg.get("server") or {}
        base = (args.url or "http://127.0.0.1:" + str(server.get("port") or 8787)).rstrip("/")
        url = urlsplit(base)
        if url.scheme not in ("http", "https") or not url.hostname or url.username or url.password or url.query or url.fragment:
            raise ValueError()
    except (OSError, ValueError, TypeError, AttributeError):
        parser.error("Invalid configuration or URL (details omitted)")
    key = (cfg.get("auth") or {}).get("api_key", "")
    report = {"format": 1, "working_directory": str(Path.cwd()), "config_exists": config_path.is_file(), "endpoints": {}}
    metrics = {}
    for endpoint in ("/api/status", "/api/metrics", "/api/models", "/api/accounts/manage"):
        data, meta = get_json(base, endpoint, key, args.timeout)
        report["endpoints"][endpoint] = meta
        if endpoint == "/api/metrics":
            metrics = data
            report["usage"] = usage_summary(data)
            storage = data.get("storage") or {}
            report["storage"] = {field: storage.get(field) for field in ("enabled", "status", "file", "loaded_from", "last_saved_at") if field in storage}
            report["storage"].update(load_error=bool(storage.get("load_error")), save_error=bool(storage.get("save_error")))
        elif endpoint == "/api/models":
            report["catalogs"] = model_summary(data)
        elif endpoint == "/api/accounts/manage":
            report["accounts"] = account_summary(data)
    primary = Path(args.metrics_file or cfg.get("metrics_file") or (config_path.parent / "metrics.json" if config_path.is_file() else "agent2api-metrics.json"))
    paths = {primary, primary.with_name("metrics.json"), primary.with_name("agent2api-metrics.json"), Path(str(primary) + ".recovery")}
    if url.hostname in ("127.0.0.1", "localhost", "::1"):
        for name in ((metrics.get("storage") or {}).get("file"), (metrics.get("storage") or {}).get("loaded_from")):
            if name:
                paths.add(Path(name))
    paths.update(Path(name) for name in cfg.get("metrics_history_imports", []) if str(name).lower().endswith(".json"))
    report["local_statistics"] = [file_summary(path) for path in sorted(paths)]
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
