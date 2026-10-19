// Explain recorded facts only. Generic provider wording is never a diagnosis.
export function requestIssue(row) {
  if (!row || row.ok || !row.error) return null;
  const code = String(row.error).split(":", 1)[0].trim();
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
  if (!account?.identity) return "unknown";
  return account.identity.needs_refresh ? "refresh" : "signed_in";
}

// Direct integrations expose a status record, but have no WorkBuddy pool or
// device-login controls. Do not infer a credential file from missing identity.
export function accountConnection(account) {
  if (account?.platform === "openrouter") return { source: "api_key", pooled: false };
  if (account?.platform === "cursor") return { source: "desktop", pooled: false };
  const source = account?.identity?.source === "desktop" ? "desktop"
    : account?.identity?.source === "file" || account?.identity?.credential_path ? "file" : "unknown";
  return { source, pooled: ["workbuddy", "workbuddy-global"].includes(account?.platform) };
}
