import { escapeHTML as e } from "./core.js";
import { confirmedFreeModels } from "./pricing.js";

export const configClients = ["cc-switch", "pi"];
const names = { workbuddy: "WorkBuddy 国内", "workbuddy-global": "WorkBuddy 国际", openrouter: "OpenRouter", cursor: "Cursor", "trae-cn": "TRAE 国内", kiro:"Kiro Free" };

export function clientModelHasBudget(model) {
  return model.context_tokens > 0 && (model.max_output_tokens > 0 || (model.platform === "kiro" && model.max_output_tokens === 0));
}

export function clientPlatformOptions(models) {
  const free = confirmedFreeModels(models).filter(m => m.supports_tools === true && clientModelHasBudget(m));
  return Object.entries(names).filter(([id]) => models.some(m => m.platform === id)).map(([id, name]) => ({ id, name, count: free.filter(m => m.platform === id).length }));
}

export function selectedClientPlatforms(state, models) {
  const options = clientPlatformOptions(models).map(p => p.id);
  return state.platforms === null ? options : options.filter(id => state.platforms.includes(id));
}

export function clientRequest(client, state, models) {
  if (!configClients.includes(client)) throw new Error("Unknown configuration client");
  const body = { platforms: selectedClientPlatforms(state, models) };
  if (client === "cc-switch") body.apps = [...state.apps];
  return { path: `/api/clients/${client}`, body, headers: { "X-Agent2API-Local-Action": `${client}-sync` } };
}

export function clientReady(client, state, info, models) {
  return !!info?.available && !state.busy && selectedClientPlatforms(state, models).length > 0 && (client !== "cc-switch" || state.apps.length > 0);
}

export async function runClientBatch(clients, run) {
  // 分别保留结果；一个客户端失败后仍允许另一个完成配置。
  const outcomes = [];
  for (const client of clients) {
    try { outcomes.push({ client, result: await run(client) }); }
    catch (error) { outcomes.push({ client, error }); }
  }
  return outcomes;
}

export function renderClientSetup(states, info, models, L, batchBusy = false) {
  const options = clientPlatformOptions(models);
  const anyBusy = configClients.some(id => states[id].busy) || batchBusy;
  const ready = configClients.filter(id => clientReady(id, states[id], info[id], models));
  const choice = (client, action, id, label, selected, disabled) => `<button type="button" class="client-choice ${selected ? "active" : ""}" data-action="${action}" data-kind="${client}" data-value="${e(id)}" aria-pressed="${selected}" ${disabled ? "disabled" : ""}>${e(label)}</button>`;
  const cards = configClients.map(client => {
    const state = states[client], local = info[client], platforms = selectedClientPlatforms(state, models);
    const result = state.result;
    const reason = local?.reason || (!local ? L("正在检测本机配置…", "Checking local configuration…") : "");
    const summary = result ? (result.status === "unchanged" ? L(`配置已是最新：${result.model_count} 个免费模型，未重复写入。`, `Up to date: ${result.model_count} free models; no rewrite.`) : L(`已配置 ${result.model_count} 个免费模型、${result.provider_count} 个供应商；新增 ${result.added.length} 项，保留 ${result.retained} 项，移除 ${result.removed.length} 项。`, `Configured ${result.model_count} free models in ${result.provider_count} providers: ${result.added.length} added, ${result.retained} retained, ${result.removed.length} removed.`)) : "";
    return `<section class="card client-config-card" aria-label="${client}"><div class="card-head"><div><h3>${client === "pi" ? "pi Coding Agent" : "cc-switch"}</h3><p>${e(client === "pi" ? L("按平台组织供应商与完整免费模型列表", "Providers and free model lists grouped by platform") : L("每个模型生成独立供应商，方便在应用内切换", "One switchable provider per free model"))}</p></div><span class="badge">${e(local?.available ? L("可配置", "Ready") : L("待就绪", "Unavailable"))}</span></div><div class="card-body"><fieldset class="client-options" ${anyBusy ? "disabled" : ""}><legend>${e(L("选择上游平台", "Upstream platforms"))}</legend><div class="client-choices">${options.map(p => choice(client, "client-platform", p.id, `${p.name} · ${p.count}`, platforms.includes(p.id), anyBusy)).join("") || `<p class="muted">${e(L("请先接入账号并刷新模型目录。", "Connect an account and refresh the model catalogue first."))}</p>`}</div>${client === "cc-switch" ? `<label>${e(L("写入应用", "Target applications"))}</label><div class="client-choices">${[["claude", "Claude Code"], ["codex", "Codex"]].map(([id, label]) => choice(client, "client-app", id, label, state.apps.includes(id), anyBusy)).join("")}</div>` : ""}</fieldset><p class="scope-note">${e(local?.available ? client === "pi" && !local.exists ? L("首次配置会创建 models.json；点击时重新核验免费权益。", "The first setup creates models.json; free access is checked on click.") : L(`本机已有 ${local.configured_entries} 项 Agent2API 配置；点击时重新核验免费权益。`, `${local.configured_entries} Agent2API entries exist; free access is checked on click.`) : reason)}</p><button type="button" class="button primary" data-action="sync-client" data-kind="${client}" ${anyBusy || !clientReady(client, state, local, models) ? "disabled" : ""}>${e(state.busy ? L("正在核验并写入…", "Verifying and writing…") : L(`配置到 ${client}`, `Configure ${client}`))}</button>${local?.profile ? `<details class="client-location"><summary>${e(L("配置位置", "Configuration location"))}</summary><code>${e(local.profile)}</code></details>` : ""}${state.error ? `<p class="message-error" role="alert">${e(state.error)}</p>` : ""}${result ? `<div class="zcode-sync-result" role="status"><strong>${e(summary)}</strong>${result.backup_path ? `<p>${e(L("原配置备份：", "Backup: "))}<code>${e(result.backup_path)}</code></p>` : ""}${client === "pi" && result.removed.length ? `<p>${e(L("已移除无法确认免费的模型；如果其中包含原默认模型，请在 pi 中重新选择。", "Models without confirmed free access were removed. Reselect the default in pi if it was among them."))}</p>` : ""}<p>${e(client === "pi" ? L("在 pi 中使用 /model 选择模型；若列表未刷新，执行 /reload 或重启 pi。", "Use /model in pi; run /reload or restart pi if needed.") : L("重新打开 cc-switch，选择 Claude Code / Codex 中的 Agent2API 供应商，再点击启用。当前供应商保持原样。", "Reopen cc-switch, select an Agent2API provider under Claude Code / Codex, then enable it. The current provider is preserved."))}</p><details><summary>${e(L("查看本次模型", "View configured models"))}</summary><ul>${result.model_ids.map(id => `<li><code>${e(id)}</code></li>`).join("")}</ul></details></div>` : ""}</div></section>`;
  }).join("");
  return `<section class="client-config-section"><div class="section-heading"><div><h2>${e(L("更多客户端快捷配置", "More client configurations"))}</h2><p class="muted">${e(L("仅配置支持工具调用的免费模型。备份后写入，保留其他供应商、默认选择与历史记录。", "Configure free models with tool support. Backups, other providers, defaults and history are preserved."))}</p></div><button type="button" class="button" data-action="sync-clients" ${anyBusy || ready.length !== configClients.length ? "disabled" : ""}>${e(batchBusy ? L("正在批量配置…", "Configuring clients…") : L("同时配置 cc-switch 和 pi", "Configure cc-switch and pi"))}</button></div><div class="client-config-grid">${cards}</div></section>`;
}
