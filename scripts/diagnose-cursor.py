#!/usr/bin/env python3
"""Read Cursor account/catalogue compatibility without generating or printing credentials."""

import argparse
from contextlib import closing
import json
import os
from pathlib import Path
import re
import sqlite3
import sys
import urllib.error
import urllib.request


def user_data_dir():
    if sys.platform == "win32":
        return Path(os.environ.get("APPDATA", Path.home() / "AppData/Roaming")) / "Cursor"
    if sys.platform == "darwin":
        return Path.home() / "Library/Application Support/Cursor"
    return Path(os.environ.get("XDG_CONFIG_HOME", Path.home() / ".config")) / "Cursor"


def desktop_token(path):
    # Read-only SQLite access preserves the running editor's database/WAL.
    with closing(sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True)) as connection:
        row = connection.execute("SELECT value FROM ItemTable WHERE key=?", ("cursorAuth/accessToken",)).fetchone()
    return row[0] if row and isinstance(row[0], str) else ""


def desktop_version():
    if sys.platform == "win32":
        root = Path(os.environ.get("LOCALAPPDATA", Path.home() / "AppData/Local")) / "Programs/cursor/resources/app"
    elif sys.platform == "darwin":
        root = Path("/Applications/Cursor.app/Contents/Resources/app")
    else:
        return ""
    try:
        value = json.loads((root / "package.json").read_text(encoding="utf-8")).get("version", "")
        return value if isinstance(value, str) else ""
    except (OSError, ValueError):
        return ""


class ProbeError(Exception):
    def __init__(self, kind, status=None):
        self.kind = kind
        self.status = status


def fetch(base_url, path, token, version, body=None):
    headers = {
        "Authorization": "Bearer " + token,
        "Content-Type": "application/json",
        "Connect-Protocol-Version": "1",
        "x-ghost-mode": "true",
    }
    if version:
        headers["x-cursor-client-version"] = version
    data = None if body is None else json.dumps(body).encode("utf-8")
    request = urllib.request.Request(base_url.rstrip("/") + path, data=data, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            raw = response.read(8 * 1024 * 1024 + 1)
            if len(raw) > 8 * 1024 * 1024:
                raise ProbeError("response_too_large")
            return json.loads(raw)
    except urllib.error.HTTPError as error:
        # Do not echo arbitrary error text: upstream failures may reflect keys.
        raise ProbeError("http_error", error.code) from None
    except (urllib.error.URLError, TimeoutError):
        raise ProbeError("connection_failed") from None
    except (ValueError, UnicodeError):
        raise ProbeError("invalid_json") from None


def version_tuple(value):
    match = re.fullmatch(r"(\d+)\.(\d+)\.(\d+)(?:[-+].*)?", value or "")
    return tuple(map(int, match.groups())) if match else None


def summarize(profile, limits, server, catalogue, usable, version):
    status = server.get("clientVersionStatus") or {}
    current = version_tuple(version)
    supported = version_tuple(status.get("minSupportedClientVersion"))
    old = bool(current and supported and current < supported)
    # Account catalogue membership is not zero-price model metadata. Never
    # export this list to ZCode or claim that listed models can generate free.
    models = catalogue.get("models") or []
    priced = [model for model in models if isinstance(model.get("price"), (int, float))
              and not isinstance(model.get("price"), bool)]
    free_account = profile.get("membershipType") == "free" and profile.get("isTeamMember") is False
    billing_disabled = limits.get("noUsageBasedAllowed") is True
    return {
        "mode": "read_only_no_generation",
        "account": {
            "membership": profile.get("membershipType"),
            "team_member": profile.get("isTeamMember"),
            "on_demand_disabled": billing_disabled,
            "individual_free_account_confirmed": free_account,
        },
        "client": {
            "installed_version": version or None,
            "minimum_supported_version": status.get("minSupportedClientVersion"),
            "minimum_allowed_version": status.get("minAllowedClientVersion"),
            "below_supported_version": old,
        },
        "catalogue": {
            "listed_models": len(models),
            "usable_model_entries": len(usable.get("models") or []),
            "models_with_explicit_prices": len(priced),
            "confirmed_free_generation_models": [],
        },
        "next_step": "update_cursor" if old else "verify_generation_with_current_client_or_sdk",
        "free_account_billing_guard_passed": free_account and billing_disabled,
        "generation_verified": False,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state-db", type=Path, default=user_data_dir() / "User/globalStorage/state.vscdb")
    parser.add_argument("--base-url", default="https://api2.cursor.sh")
    parser.add_argument("--client-version", default=desktop_version())
    parser.add_argument("--token-env", default="CURSOR_ACCESS_TOKEN", help="Optional desktop access token environment variable; never printed")
    args = parser.parse_args()
    try:
        api_key = os.environ.get("CURSOR_API_KEY", "").strip()
        token = os.environ.get(args.token_env, "").strip()
        if api_key:
            token = fetch(args.base_url, "/auth/exchange_user_api_key", api_key, args.client_version, {}).get("accessToken", "")
        elif not token and args.state_db.is_file():
            token = desktop_token(args.state_db)
        if not token:
            raise ProbeError("no_credential")
        profile = fetch(args.base_url, "/auth/full_stripe_profile", token, args.client_version)
        limits = fetch(args.base_url, "/aiserver.v1.DashboardService/GetHardLimit", token, args.client_version, {})
        server = fetch(args.base_url, "/aiserver.v1.ServerConfigService/GetServerConfig", token, args.client_version, {})
        catalogue = fetch(args.base_url, "/aiserver.v1.AiService/AvailableModels", token, args.client_version,
                          {"scope": 1, "excludeMaxNamedModels": True})
        usable = fetch(args.base_url, "/aiserver.v1.AiService/GetUsableModels", token, args.client_version, {})
        report = summarize(profile, limits, server, catalogue, usable, args.client_version)
        report["credential_source"] = "user_api_key" if api_key else "desktop_access_token"
        print(json.dumps(report, ensure_ascii=False, indent=2))
    except ProbeError as error:
        print(json.dumps({"mode": "read_only_no_generation", "error": error.kind, "http_status": error.status}))
        return 1
    except (OSError, sqlite3.Error, TypeError, AttributeError):
        print(json.dumps({"mode": "read_only_no_generation", "error": "invalid_local_state_or_upstream_schema"}))
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
