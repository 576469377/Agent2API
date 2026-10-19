#!/usr/bin/env python3
"""Sync only confirmed free gateway models to ZCode, pruning paid/unknown entries and backing up first."""

import argparse
import datetime
import json
import os
from pathlib import Path
import shutil
import urllib.request


def confirmed_free(model):
    pricing = model.get("pricing") or {}
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
    # ZCode uses this maximum as its requested generation budget. Some free
    # providers reject their own advertised catalogue ceiling (Dots: 460800).
    # Keep the full context window, but use a tested, practical output budget.
    return min(maximum, 8192) if model.get("platform") == "openrouter" else maximum


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
    args = parser.parse_args()

    path = Path(args.zcode_config).expanduser()
    document = json.loads(path.read_text(encoding="utf-8-sig")) if path.exists() else {
        "schemaVersion": 1,
        "config": {
            "providerOrder": [],
            "providerConfigRules": {"providerRules": []},
            "modelConfigRules": {"providerModelRules": [], "manualProviderModelRules": []},
        },
    }
    if document.get("schemaVersion") != 1:
        raise ValueError("Unsupported ZCode config version; no changes made")
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
            if rule["providerId"] == args.provider_id and model.get("platform") == "openrouter":
                spec = rule.setdefault("config", {}).setdefault("optionSpecs", {}).setdefault("maxOutputTokens", {})
                limit = output_limit(model)
                previous_max = spec.get("max")
                if previous_max is None or previous_max > limit:
                    spec["max"] = limit
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
        if model.get("platform") == "cursor":
            # Cursor emits thinking but does not expose an effort/on-off knob.
            rules["manualProviderModelRules"][-1]["config"]["optionSpecs"]["reasoningLevel"] = {
                "values": ["enabled"], "map": "{}",
            }
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

    path.parent.mkdir(parents=True, exist_ok=True)
    output = path.with_name(path.name + ".candidate") if args.dry_run else path
    text = json.dumps(document, ensure_ascii=False, indent=2) + "\n"
    if not args.dry_run and path.exists():
        stamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S-%f")
        backup = path.with_name(path.name + ".bak-" + stamp)
        shutil.copy2(path, backup)
        print("Backup:", backup)
    temporary = output.with_name(output.name + ".tmp")
    temporary.write_text(text, encoding="utf-8")
    os.replace(temporary, output)
    print("Candidate:" if args.dry_run else "Updated:", output)
    print("Models:", ", ".join(selected))
    if removed:
        print("Removed paid/unknown/unavailable models:", ", ".join(removed))


if __name__ == "__main__":
    main()
