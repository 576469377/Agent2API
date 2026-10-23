import { escapeHTML as e } from "./core.js";

const text = (lang, zh, en) => lang === "en" ? en : zh;
const amount = (value, lang) => Number.isFinite(value) ? new Intl.NumberFormat(lang === "en" ? "en-US" : "zh-CN", { maximumFractionDigits: 2 }).format(value) : "—";
const stamp = (value, lang) => value && Number.isFinite(Date.parse(value)) ? new Date(value).toLocaleString(lang === "en" ? "en-US" : "zh-CN", { timeZone: "Asia/Shanghai", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }) : "—";

export function checkinLabel(state, lang = "zh") {
  if (state?.checked_in === true) return text(lang, "今日已签到", "Checked in today");
  if (state?.available === false) return text(lang, "签到未开放", "Check-in unavailable");
  if (state?.checked_in === false) return text(lang, "今日未签到", "Not checked in today");
  return text(lang, "签到状态待查询", "Check-in status unknown");
}

export function renderTraeCheckin(state, lang = "zh", account = {}) {
  if (!state) return "";
  const L = (zh, en) => text(lang, zh, en), balance = state.balance;
  const name = account.name || "TRAE", scope = `data-platform="${e(account.platform || "trae-cn")}" data-label="${e(account.label || "")}"`;
  const result = { pending: L("尚未执行", "Not run yet"), success: L("签到成功", "Check-in succeeded"), already_checked: L("已签到，已跳过领取", "Already checked in; claim skipped"), unavailable: L("活动未开放，已跳过", "Activity unavailable; skipped"), failed: L("执行失败", "Run failed") }[state.last_result] || L("等待检查", "Awaiting check");
  return `<section class="card trae-checkin" aria-label="${e(L(`${name} 每日签到与积分`, `${name} daily check-in and credits`))}">
    <div class="trae-checkin-heading"><div><h2>${e(L(`${name} 每日签到与积分`, `${name} daily check-in and credits`))}</h2><p>${e(state.enabled ? L(`每天 ${state.time} 自动签到 · 北京时间`, `Automatic check-in at ${state.time} daily · Beijing time`) : L("自动签到已暂停", "Automatic check-in paused"))}</p></div><span class="${state.checked_in === true ? "positive" : "muted"}">${e(checkinLabel(state, lang))}</span></div>
    ${balance ? `<dl class="trae-credit-metrics">${[[L("剩余积分", "Remaining credits"), balance.remaining], [L("本期已用", "Credits used"), balance.used], [L("本期总额", "Total credits") , balance.total]].map(([label, value]) => `<div><dt>${e(label)}</dt><dd>${e(amount(value, lang))}</dd></div>`).join("")}</dl>` : ""}
    <div class="trae-checkin-facts"><span>${e(L("今日奖励", "Today's reward"))}<strong>${e(amount(state.daily_credits, lang))} ${e(L("积分", "credits"))}</strong></span><span>${e(L("下次执行", "Next run"))}<strong>${e(stamp(state.next_run_at, lang))}</strong></span><span>${e(L("最近尝试", "Last attempt"))}<strong>${e(stamp(state.last_attempt_at, lang))}</strong></span><span>${e(L("最近结果", "Last result"))}<strong>${e(state.busy ? L("检查或执行中…", "Checking or running…") : result)}</strong></span></div>
    ${state.last_error ? `<p class="message-error" role="status">${e(state.last_error)}</p>` : ""}
    ${balance && !balance.verified_free ? `<p class="muted">${e(L("余额已读取，积分来源仍待确认；模型调用继续执行免费权益检查。", "Balance loaded; credit eligibility still needs verification. Model calls retain their free entitlement checks."))}</p>` : ""}
    <p class="trae-checkin-note">${e(L("关闭网页后仍会自动签到，网关需要保持运行。自动失败后可使用立即签到补救。", "Check-in runs with the page closed while the gateway stays online. Manual check-in is a fallback after an automatic failure."))}</p>
    <div class="trae-checkin-footer"><span>${e(balance ? L("余额更新", "Balance updated") : L("状态更新", "Status updated"))} ${e(stamp(balance?.checked_at || state.status_checked_at, lang))}${balance?.expires_at ? ` · ${e(L("最近到期", "Next expiry"))} ${e(stamp(balance.expires_at, lang))}` : ""}</span><div><button type="button" class="button small" data-action="trae-checkin-settings" ${scope}>${e(L("签到设置", "Schedule"))}</button><button type="button" class="button small" data-action="trae-checkin-refresh" ${scope} ${state.busy ? "disabled" : ""}>${e(L("刷新状态", "Refresh"))}</button><button type="button" class="button small ghost" data-action="trae-checkin-manual" ${scope} ${state.can_manual && !state.busy ? "" : "disabled"}>${e(L("立即签到（兜底）", "Manual fallback"))}</button></div></div>
  </section>`;
}
