#!/usr/bin/env python3
"""Sync only confirmed free gateway models to ZCode, pruning paid/unknown entries and backing up first."""

import argparse
from contextlib import closing
import datetime
import json
import os
from pathlib import Path
import shutil
import sqlite3
import tempfile
import time
import urllib.request


def confirmed_free(model):
    pricing = model.get("pricing") or {}
    if model.get("platform") in ("trae-cn", "kiro"):
        is_kiro = model.get("platform") == "kiro"
        if is_kiro and (not isinstance(model.get("context_tokens"), int) or isinstance(model.get("context_tokens"), bool)
                        or model["context_tokens"] <= 0 or not isinstance(model.get("max_output_tokens"), int)
                        or isinstance(model.get("max_output_tokens"), bool) or model["max_output_tokens"] < 0):
            return False
        access = model.get("access") or {}
        try:
            now = datetime.datetime.now(datetime.timezone.utc)
            times = [datetime.datetime.fromisoformat(str(value).replace("Z", "+00:00")) for value in (pricing["observed_at"], access["checked_at"])]
            rate, remaining = pricing.get("credit_multiplier"), access.get("remaining_credits")
            return (not model.get("pricing_sync_failed") and not model.get("pricing_from_snapshot")
                    and pricing.get("source") == ("kiro_cli_catalog" if is_kiro else "trae_account_catalog") and pricing.get("status") == "credits"
                    and isinstance(rate, (int, float)) and not isinstance(rate, bool) and 0 < rate < float("inf")
                    and access.get("source") == ("kiro_cli_free_plan" if is_kiro else "trae_free_plan") and access.get("basis") == "recurring_free_allowance"
                    and access.get("plan") == "free" and access.get("overage_enabled") is False
                    and isinstance(remaining, (int, float)) and not isinstance(remaining, bool) and 0 < remaining < float("inf")
                    and all(-60 <= (now - value).total_seconds() <= 300 for value in times))
        except (KeyError, TypeError, ValueError):
            return False
    if pricing.get("status") != "free" or model.get("pricing_sync_failed"):
        return False
    if pricing.get("source") not in ("upstream_config", "openrouter_models", "cursor_free_plan"):
        return False
    if pricing.get("source") == "upstream_config" and pricing.get("credit_multiplier") != 0:
        return False
    try:
        observed = datetime.datetime.fromisoformat(pricing["observed_at"].replace("Z", "+00:00"))
        age = (datetime.datetime.now(datetime.timezone.utc) - observed).total_seconds()
        return -60 <= age <= 300
    except (KeyError, TypeError, ValueError):
        return False


def output_limit(model):
    maximum = model.get("max_output_tokens", 0)
    if model.get("platform") == "kiro" and maximum == 0:
        return 8192  # Recommended client budget, not a published/enforced ACP limit.
    # ZCode uses this maximum as its requested generation budget. Some free
    # providers reject their own advertised catalogue ceiling (Dots: 460800).
    # Keep the full context window, but use a tested, practical output budget.
    return min(maximum, 8192) if model.get("platform") == "openrouter" else maximum


def read_source(path):
    try:
        return path.read_bytes()
    except FileNotFoundError:
        return None


def write_config(path, document, source, dry_run=False):
    """Do not overwrite an intervening edit or rewrite an unchanged document."""
    def check_source():
        if read_source(path) != source:
            raise ValueError("ZCode config changed during sync; no configuration was replaced. Run sync again with the latest file")

    check_source()
    if not dry_run and source is not None and json.loads(source.decode("utf-8-sig")) == document:
        return None, None
    path.parent.mkdir(parents=True, exist_ok=True)
    output = path.with_name(path.name + ".candidate") if dry_run else path
    temporary = None
    backup = None
    try:
        # Each writer owns its temporary file. A second sync must not truncate
        # another process's candidate before it is atomically installed.
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", newline="\n",
                                         dir=path.parent, prefix=output.name + ".", suffix=".tmp", delete=False) as handle:
            temporary = Path(handle.name)
            json.dump(document, handle, ensure_ascii=False, indent=2)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        check_source()
        if not dry_run and source is not None:
            stamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S-%f")
            backup = path.with_name(path.name + ".bak-" + stamp)
            shutil.copy2(path, backup)
        check_source()
        os.replace(temporary, output)
        return output, backup
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def usage_snapshot(path, cutoff_ms):
    """Read aggregate counters only, including committed WAL data; never open a new database."""
    if not path.is_file():
        return {"status": "missing"}
    try:
        with closing(sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True, timeout=1)) as connection:
            connection.execute("PRAGMA query_only=ON")
            columns = {row[1] for row in connection.execute("PRAGMA table_info(model_usage)")}
            if not {"started_at", "computed_total_tokens"} <= columns:
                return {"status": "unsupported_schema"}
            count, tokens, first, last = connection.execute(
                "SELECT count(*), coalesce(sum(computed_total_tokens), 0), min(started_at), max(started_at) "
                "FROM model_usage WHERE started_at <= ?", (cutoff_ms,)).fetchone()
            day = lambda value: datetime.datetime.fromtimestamp(value / 1000, datetime.timezone.utc).date().isoformat() if value is not None else None
            return {"status": "ready", "model_requests": count, "tokens": tokens,
                    "first_day_utc": day(first), "last_day_utc": day(last)}
    except (OSError, sqlite3.Error, OverflowError, ValueError, TypeError):
        # Database errors can include paths or SQL values. Keep those local.
        return {"status": "unavailable"}


def usage_report(path, before, cutoff_ms):
    after = usage_snapshot(path, cutoff_ms)
    comparison = "unavailable"
    if before["status"] == after["status"] == "ready":
        comparison = "no_record_count_decrease" if after["model_requests"] >= before["model_requests"] else "record_count_decreased"
    return {"schema": "agent2api-zcode-usage-check-v1", "scope": "local-zcode-agent-db", "through_ms": cutoff_ms, "before": before, "after": after,
            "comparison": comparison, "client_ui_verified": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", default="config.json", help="Gateway configuration (API key stays local)")
    parser.add_argument("--base-url", default="http://127.0.0.1:8787")
    parser.add_argument("--zcode-config", default=str(Path.home() / ".zcode/v2/provider_config.json"))
    parser.add_argument("--provider-id", default="agent2api")
    parser.add_argument("--provider-name", default="Agent2API")
    parser.add_argument("--models", nargs="+", help="Only add these confirmed free model IDs, in this order")
    parser.add_argument("--default-model", help="Also select this provider/model as the default")
    parser.add_argument("--dry-run", action="store_true", help="Write a candidate beside the config without applying it")
    parser.add_argument("--verify-usage", action="store_true", help="Read local ZCode usage counters before/after sync; never writes session or statistics storage")
    parser.add_argument("--usage-db", help="Explicit local ZCode agent database for --verify-usage; otherwise derived from the selected v2 profile")
    args = parser.parse_args()
    if args.usage_db and not args.verify_usage:
        parser.error("--usage-db requires --verify-usage")

    path = Path(args.zcode_config).expanduser()
    source = read_source(path)
    document = json.loads(source.decode("utf-8-sig")) if source is not None else {
        "schemaVersion": 1,
        "config": {
            "providerOrder": [],
            "providerConfigRules": {"providerRules": []},
            "modelConfigRules": {"providerModelRules": [], "manualProviderModelRules": []},
        },
    }
    if document.get("schemaVersion") != 1:
        raise ValueError("Unsupported ZCode config version; no changes made")
    usage_path = None
    usage_before = None
    cutoff_ms = int(time.time() * 1000)
    if args.verify_usage:
        if args.usage_db:
            usage_path = Path(args.usage_db).expanduser()
        elif path.parent.name == "v2":
            usage_path = path.parent.parent / "cli/db/db.sqlite"
        else:
            parser.error("--verify-usage with a non-v2 config requires --usage-db; no changes made")
        if usage_path.resolve() in {path.resolve(), path.with_name(path.name + ".candidate").resolve()}:
            parser.error("Usage database must be separate from the provider config and candidate; no changes made")
        usage_before = usage_snapshot(usage_path, cutoff_ms)
    key = os.environ.get("AGENT2API_API_KEY") or json.loads(Path(args.config).read_text(encoding="utf-8-sig")).get("auth", {}).get("api_key", "")
    headers = {"Authorization": "Bearer " + key} if key else {}

    # Revalidate prices even on --dry-run. The operation only invalidates model
    # caches; it never sends a generation request or changes gateway settings.
    refresh = urllib.request.Request(args.base_url.rstrip("/") + "/api/config",
                                     data=b'{"refresh_models":true}',
                                     headers={**headers, "Content-Type": "application/json"})
    with urllib.request.urlopen(refresh, timeout=30) as response:
        json.load(response)
    request = urllib.request.Request(args.base_url.rstrip("/") + "/api/models?refresh=1", headers=headers)
    with urllib.request.urlopen(request, timeout=30) as response:
        catalogue = json.load(response)
    if catalogue.get("refreshing") or any(row.get("status") == "error" for row in catalogue.get("catalog_status", [])):
        raise ValueError("Gateway catalogue refresh incomplete or failed; no changes made")
    models = catalogue["models"]
    by_id = {}
    for model in models:
        qualified = model.get("routing_id")
        if qualified:
            by_id[qualified] = model
        if model.get("default_route", not qualified):
            by_id[model["id"]] = model
    def qualified_id(model_id):
        return by_id.get(model_id, {}).get("routing_id") or model_id

    # A missing price, a dynamic router or a historical entry is not evidence of
    # free access. Use only explicit current catalogue metadata, never the name.
    allowed = {
        model.get("routing_id") or model["id"] for model in models
        if confirmed_free(model)
        and model.get("context_tokens", 0) > 0 and model.get("max_output_tokens", 0) > 0
    }
    if not allowed:
        raise ValueError("No confirmed free models with published limits; refresh the gateway catalogue before syncing; no changes made")
    selected = [qualified_id(m) for m in args.models] if args.models else list(dict.fromkeys(
        model.get("routing_id") or model["id"]
        for model in models if (model.get("routing_id") or model["id"]) in allowed
    ))
    if args.default_model:
        args.default_model = qualified_id(args.default_model)
    missing = set(selected) - by_id.keys()
    if missing:
        raise ValueError("Models absent from gateway: " + ", ".join(sorted(missing)))
    rejected = set(selected) - allowed
    if rejected:
        raise ValueError("Models are not confirmed free with published limits: " + ", ".join(sorted(rejected)))
    if len(set(selected)) != len(selected):
        raise ValueError("Duplicate model IDs")
    if args.default_model and args.default_model not in selected:
        raise ValueError("Default model must be one of the selected models")

    config = document["config"]
    providers = config["providerConfigRules"]["providerRules"]
    provider = next((p for p in providers if p["providerId"] == args.provider_id), None)
    if provider is None:
        provider = {"providerId": args.provider_id}
        providers.append(provider)
    previous = provider.get("config", {})
    old_ids = [qualified_id(m) for m in previous.get("personalModelIds", [])]
    removed = [m for m in old_ids if m not in allowed]
    merged_ids = list(dict.fromkeys([m for m in old_ids if m in allowed] + selected))
    merged_order = list(dict.fromkeys([qualified_id(m) for m in previous.get("modelOrder", [])
                                      if qualified_id(m) in merged_ids] + merged_ids))
    provider.update({
        "providerName": args.provider_name,
        "enabled": provider.get("enabled", True),
        "config": {
            **previous,
            "group": "standard-personal",
            "access": {"type": "api-key", "apiKey": key or "agent2api-local"},
            "api": {"type": "anthropic-messages", "baseUrl": args.base_url.rstrip("/") + "/v1"},
            "personalModelIds": merged_ids,
            "modelOrder": merged_order,
        },
    })
    order = config.setdefault("providerOrder", [])
    if args.provider_id not in order:
        order.append(args.provider_id)
    rules = config.setdefault("modelConfigRules", {"providerModelRules": [], "manualProviderModelRules": []})
    # Rename references together, retaining capability overrides and disabled states.
    for kind in ("providerModelRules", "manualProviderModelRules"):
        rules[kind] = [rule for rule in rules.get(kind, [])
                       if rule["providerId"] != args.provider_id or qualified_id(rule["modelId"]) in allowed]
        for rule in rules.get(kind, []):
            if rule["providerId"] == args.provider_id:
                rule["modelId"] = qualified_id(rule["modelId"])
    identities = [(r["providerId"], r["modelId"]) for kind in ("providerModelRules", "manualProviderModelRules")
                  for r in rules.get(kind, [])]
    if len(set(identities)) != len(identities):
        raise ValueError("Prefix migration would merge conflicting model settings; no changes made")
    for kind in ("providerModelRules", "manualProviderModelRules"):
        for rule in rules[kind]:
            model = by_id.get(rule["modelId"], {})
            if rule["providerId"] == args.provider_id and model.get("platform") in ("openrouter", "kiro"):
                spec = rule.setdefault("config", {}).setdefault("optionSpecs", {}).setdefault("maxOutputTokens", {})
                limit = output_limit(model)
                previous_max = spec.get("max")
                if previous_max is None or previous_max > limit:
                    spec["max"] = limit
                if model.get("platform") == "kiro":
                    rule["config"]["optionSpecs"]["reasoningLevel"] = {"values": ["disabled"], "map": "{}"}
            if rule["providerId"] == args.provider_id and model.get("platform") == "trae-cn":
                rule.setdefault("config", {}).setdefault("optionSpecs", {})["reasoningLevel"] = {
                    "values": ["enabled"], "map": "{}",
                }
                if (model.get("id") in ("deepseek-v4.1-flash", "glm-5.3-flash")
                        and model.get("context_tokens") == 1000000 and model.get("max_output_tokens") == 64000):
                    properties = rule["config"].setdefault("properties", {})
                    if properties.get("contextWindow") == 200000:
                        properties["contextWindow"] = 1000000
                        maximum = rule["config"]["optionSpecs"].setdefault("maxOutputTokens", {})
                        if maximum.get("max") == 32000:
                            maximum["max"] = 64000
    default = config.get("defaultModelSelection") or {}
    if default.get("providerId") == args.provider_id:
        default["modelId"] = qualified_id(default["modelId"])
    existing = {(r["providerId"], r["modelId"]) for kind in ("providerModelRules", "manualProviderModelRules")
                for r in rules.get(kind, [])}
    for model_id in selected:
        if (args.provider_id, model_id) in existing:
            continue
        model = by_id[model_id]
        context = model.get("context_tokens", 0)
        max_output = output_limit(model)
        if not context or not max_output:
            raise ValueError("Upstream has no context/output limits for " + model_id + "; choose models with published metadata")
        thinking = bool(model.get("supports_thinking"))
        rules["manualProviderModelRules"].append({
            "providerId": args.provider_id,
            "modelId": model_id,
            "config": {
                "enabled": True,
                "properties": {
                    "contextWindow": context,
                    "supportsJsonSchemaOutput": False,
                    "supportsNativeWebSearch": False,
                    "supportsMidConversationSystem": False,
                    "inputFormat": {"supportsImage": bool(model.get("supports_images")), "supportsVideo": False, "supportsPdf": False},
                },
                "optionSpecs": {
                    "reasoningLevel": {
                        "values": ["disabled", "enabled"] if thinking else ["disabled"],
                        "map": 'reasoningLevel == "disabled" ? {"thinking":{"type":"disabled"}} : {"thinking":{"type":"adaptive"},"output_config":{"effort": reasoningLevel == "enabled" ? "high" : reasoningLevel}}',
                    },
                    "maxOutputTokens": {"max": max_output},
                },
            },
        })
        if model.get("platform") in ("cursor", "trae-cn"):
            # These providers emit thinking without an effort/on-off knob.
            rules["manualProviderModelRules"][-1]["config"]["optionSpecs"]["reasoningLevel"] = {
                "values": ["enabled"], "map": "{}",
            }
        if model.get("platform") == "kiro":
            rules["manualProviderModelRules"][-1]["config"]["optionSpecs"]["reasoningLevel"] = {"values": ["disabled"], "map": "{}"}
    if args.default_model:
        config["defaultModelSelection"] = {"providerId": args.provider_id, "modelId": args.default_model}
    elif default.get("providerId") == args.provider_id and default.get("modelId") not in merged_ids:
        # Never leave a removed paid model selected, and never select a model the
        # user explicitly disabled. A retained free default keeps all options.
        disabled = {r["modelId"] for kind in ("providerModelRules", "manualProviderModelRules")
                    for r in rules[kind] if r["providerId"] == args.provider_id and r.get("config", {}).get("enabled") is False}
        replacement = next((m for m in merged_order if m not in disabled), None)
        if replacement and provider["enabled"]:
            config["defaultModelSelection"] = {"providerId": args.provider_id, "modelId": replacement}
        else:
            config.pop("defaultModelSelection", None)

    output, backup = write_config(path, document, source, args.dry_run)
    if backup is not None:
        print("Backup:", backup)
    if output is None:
        print("Unchanged: ZCode config already matches; no write or backup needed")
    else:
        print("Candidate:" if args.dry_run else "Updated:", output)
    print("Models:", ", ".join(selected))
    if removed:
        print("Removed paid/unknown/unavailable models:", ", ".join(removed))
    if usage_path is not None:
        print("ZCode usage check:", json.dumps(usage_report(usage_path, usage_before, cutoff_ms), ensure_ascii=False))


if __name__ == "__main__":
    main()
