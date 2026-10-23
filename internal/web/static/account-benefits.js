import { escapeHTML as e } from "./core.js";
import { checkinLabel, renderTraeCheckin } from "./trae-checkin.js";

const finite = value => typeof value === "number" && Number.isFinite(value) && value >= 0;
const words = (lang, zh, en) => lang === "en" ? en : zh;
export function accountBenefitEndpoint(kind, account, refresh = false) {
  if (!["benefits", "checkin"].includes(kind) || !account?.platform || !account?.label) throw new Error("Account selection required");
  const query = new URLSearchParams({ platform: account.platform, label: account.label });
  if (refresh) query.set("refresh", "1");
  return `/api/accounts/${kind}?${query}`;
}
export function renderCheckinOverview(accounts, lang = "zh", open = false) {
  const rows = accounts.filter(account => account.checkin);
  const names = {workbuddy:"WorkBuddy 国内版", "workbuddy-global":"WorkBuddy 国际版", "trae-cn":"TRAE 国内版"};
  if (!rows.length) return "";
  return `<details class="card account-checkin-overview" ${open ? "open" : ""}><summary>${e(words(lang, "每日自动签到", "Daily automatic check-in"))} · ${rows.length} ${e(words(lang, "个账号 · 查看状态与设置", "accounts · status and settings"))}</summary><div>${rows.map(account => renderTraeCheckin(account.checkin, lang, { name: `${names[account.platform] || account.platform} · ${account.identity?.nickname || account.label}`, platform: account.platform, label: account.label })).join("")}</div></details>`;
}
export function benefitAmount(value, unit, lang = "zh") {
  if (!finite(value)) return "—";
  const number = new Intl.NumberFormat(lang === "en" ? "en-US" : "zh-CN", { maximumFractionDigits: unit === "USD" ? 4 : 2 }).format(value);
  return unit === "USD" ? `$${number}` : `${number} ${unit === "credits" ? words(lang, "积分", "credits") : unit || ""}`.trim();
}

export function renderAccountBenefits(account, lang = "zh", compact = false) {
  const state = account?.benefits, checkin = account?.checkin, L = (zh, en) => words(lang, zh, en);
  if (!state && !checkin) return "";
  const balances = state?.balances || [], status = state?.status || "pending";
  const statusText = ({ ready: L("已读取", "Updated"), pending: L("等待查询", "Awaiting query"), stale: L("上次读数", "Last reading"), error: L("读取失败", "Read failed") })[status] || L("未知", "Unknown");
  if (compact) {
    const rows = balances.map(balance => {
      const field = finite(balance.remaining) ? "remaining" : finite(balance.used) ? "used" : "total";
      const label = ({ remaining: L("余", "Left"), used: L("已用", "Used"), total: L("总额", "Total") })[field];
      return `<span>${e(balance.name)} · ${e(label)} <strong>${e(benefitAmount(balance[field], balance.unit, lang))}</strong></span>`;
    }).join("");
    return `<div class="account-benefits-compact ${status === "stale" || status === "error" ? "amber" : ""}">${rows || `<span class="muted">${e(statusText)}</span>`}${checkin ? `<small>${e(checkinLabel(checkin, lang))}${checkin.enabled ? ` · ${e(L("自动", "Auto"))} ${e(checkin.time)}` : ""}</small>` : ""}${status === "stale" ? `<small>${e(statusText)}</small>` : ""}</div>`;
  }
  const attributes = `data-platform="${e(account.platform)}" data-label="${e(account.label)}"`;
  return `<section class="card account-benefits-detail"><div class="card-head"><h3>${e(L("积分与额度", "Credits and allowances"))}</h3><span class="scope-pill">${e(statusText)}</span></div><div class="card-body">${balances.length ? balances.map(balance => `<div class="account-balance"><h4>${e(balance.name)}</h4><dl class="trae-credit-metrics">${[[L("剩余", "Remaining"), balance.remaining], [L("已用", "Used"), balance.used], [L("总额", "Total"), balance.total]].map(([label, value]) => `<div><dt>${e(label)}</dt><dd>${e(benefitAmount(value, balance.unit, lang))}</dd></div>`).join("")}</dl>${balance.resets_at ? `<p class="scope-note">${e(L("重置 / 到期", "Reset / expiry"))} ${e(new Date(balance.resets_at).toLocaleString())}</p>` : ""}${balance.note ? `<p class="scope-note">${e(balance.note)}</p>` : ""}</div>`).join("") : `<p class="muted">${e(L("当前没有可读取的数值；未知不表示余额为零。", "No measured amount is available; unknown does not mean zero."))}</p>`}${state?.error ? `<p class="message-error" role="status">${e(state.error)}</p>` : ""}${state?.note ? `<p class="scope-note">${e(state.note)}</p>` : ""}<div class="account-benefits-footer"><small>${e(L("最近读取", "Last updated"))} ${e(state?.checked_at ? new Date(state.checked_at).toLocaleString() : "—")}</small><button class="button small" data-action="account-benefits-refresh" ${attributes}>${e(L("刷新积分与额度", "Refresh credits and allowances"))}</button></div></div></section>`;
}
