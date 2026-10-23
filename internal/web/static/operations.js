// Explain recorded facts only. Generic provider wording is never a diagnosis.
export function requestIssue(row) {
  if (!row || row.ok) return null;
  const code = String(row.error_code || row.error || "").split(":", 1)[0].trim();
  if (row.status === 499) return { kind: "canceled", hint: "canceled" };
  if (row.status === 504 || /timeout|deadline/.test(code)) return { kind: "timeout", hint: "timeout" };
  if (["queue_full", "retry_budget_exhausted"].includes(code)) return { kind: "capacity", hint: "capacity" };
  if (code === "image_count_exceeded") return { kind: "images", hint: "images" };
  if (code === "image_cleanup_unavailable") return { kind: "image_structure", hint: "image_structure" };
  if (row.status === 401 || row.status === 403) return { kind: "auth", hint: "auth" };
  if (row.status === 429) return { kind: "rate", hint: "rate" };
  return { kind: "unknown", hint: "unknown" };
}

// Keep the model's recent visual context separate from historical omissions.
// Missing fields on older gateways remain unknown, not a measured zero.
export function imageRecordSummary(row) {
  return {
    received: Number.isFinite(row?.image_count) ? row.image_count : null,
    forwarded: Number.isFinite(row?.forwarded_image_count) ? row.forwarded_image_count : null,
    removed: Number.isFinite(row?.removed_image_count) ? row.removed_image_count : 0,
    limit: Number.isFinite(row?.max_images) ? row.max_images : null,
  };
}

export function activeFilterKeys(filters, keys) {
  return keys.filter((key) => filters[key] != null && (key === "query" || filters[key] !== "all") && String(filters[key]).trim() !== "");
}

export function credentialState(account) {
  if (["auth", "unauthorized"].includes(account?.reason) || account?.identity?.refresh_expired) return "sign_in";
  if (account?.platform === "kiro" && account.healthy === true && account.state === "ready") return "signed_in";
  if (!account?.identity) return "unknown";
  return account.identity.needs_refresh ? "refresh" : "signed_in";
}

// Direct integrations expose a status record, but have no WorkBuddy pool or
// device-login controls. Do not infer a credential file from missing identity.
export function accountConnection(account) {
  if (account?.platform === "kilo") return { source: "anonymous", pooled: false };
  if (account?.platform === "openrouter") return { source: "api_key", pooled: false };
  if (account?.platform === "cursor") return { source: "desktop", pooled: false };
  if (account?.platform === "trae-cn") return { source: "web", pooled: false, login: true };
  if (account?.platform === "kiro") return { source: "cli", pooled: false, login: true };
  const source = account?.identity?.source === "desktop" ? "desktop"
    : account?.identity?.source === "file" || account?.identity?.credential_path ? "file" : "unknown";
  return { source, pooled: ["workbuddy", "workbuddy-global"].includes(account?.platform) };
}

export function sanitizePlatforms(platforms = []) {
  return platforms.filter(platform => typeof platform.supports_sanitize === "boolean" ? platform.supports_sanitize :
    ["workbuddy", "workbuddy-global"].includes(platform.id));
}

// 后端声明接入方式；兼容旧版网关时才使用内置映射。
export function platformSetup(platform) {
  const fallback = {
    workbuddy: { method: "device_code", account_mode: "pool" },
    "workbuddy-global": { method: "device_code", account_mode: "pool" },
    "trae-cn": { method: "web_oauth", account_mode: "single" },
    kiro: { method: "cli_oauth", account_mode: "single" },
    kilo: { method: "anonymous", account_mode: "single" },
    openrouter: { method: "api_key", account_mode: "single" },
    cursor: { method: "desktop", account_mode: "single" },
  };
  const setup = platform?.connection ?? fallback[platform?.id];
  return ["device_code", "web_oauth", "cli_oauth", "api_key", "desktop", "anonymous"].includes(setup?.method) ? setup : null;
}

export function connectionPlatforms(active = [], available = []) {
  const entries = new Map();
  for (const p of active) if (platformSetup(p)) entries.set(p.id, { ...p, enabled: true });
  for (const p of available) if (!entries.has(p.id) && platformSetup(p)) entries.set(p.id, { ...p, enabled: false });
  return [...entries.values()];
}
