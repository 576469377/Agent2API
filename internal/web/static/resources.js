// View semantics shared by the console. All deadlines are anchored to receipt
// of a backend snapshot, never to a redraw of cached data.
export const pageResources = {
  overview: ["status", "metrics", "accounts", "models"], requests: ["metrics"],
  analytics: ["metrics"], connection: ["config", "models", "zcode", "cc-switch", "pi"],
  accounts: ["accounts", "metrics", "platforms"], platforms: ["platforms", "accounts", "models"],
  models: ["models", "accounts"], chat: ["models"], settings: ["config"],
};
export function refreshResources(page, matrix = false, background = false) {
  if (page === "models" && matrix) return background ? ["accounts", "matrix"] : ["models", "accounts", "matrix"];
  if (!background) return pageResources[page] || ["status"];
  if (page === "platforms") return ["platforms", "accounts", "models"];
  return page === "overview" ? ["status", "metrics", "accounts"] : ["requests", "analytics"].includes(page) ? ["metrics"] : page === "accounts" ? ["accounts", "metrics"] : page === "models" ? ["accounts"] : ["status"];
}

// Publish each resource when it settles; a slow upstream must not hold local data.
export async function loadResources(keys, { request, success, failure, settled }) {
  await Promise.allSettled(keys.map(async key => {
    try { await success(key, await request(key)); }
    catch (error) { failure(key, error); }
    finally { settled(key); }
  }));
}

// One active read per resource. Explicit refreshes supersede older reads;
// identity checks also protect against transports which settle after abort.
export function createResourceLoader(handlers) {
  const pending = new Map();
  const run = (key, options) => {
    const previous = pending.get(key);
    if (previous && !options.replace) return previous.promise;
    previous?.controller.abort();
    const entry = { controller: new AbortController() };
    pending.set(key, entry);
    handlers.started?.(key);
    const current = () => pending.get(key) === entry;
    entry.promise = (async () => {
      try {
        const data = await (options.request || handlers.request)(key, entry.controller.signal);
        if (current()) await (options.success || handlers.success)(key, data);
      } catch (error) {
        if (current()) (options.failure || handlers.failure)(key, error);
      } finally {
        if (current()) {
          pending.delete(key);
          handlers.settled(key);
        }
      }
    })();
    return entry.promise;
  };
  return {
    load(keys, options = {}) {
      return Promise.allSettled([...new Set(keys)].map(key => run(key, options)));
    },
    cancel() {
      for (const [key, entry] of pending) {
        pending.delete(key);
        entry.controller.abort();
        handlers.settled(key);
      }
    },
  };
}

// Model cooldowns belong to a platform/account pair, independent of credit rate.
export function modelAvailability(model, accounts, receivedAt, now = Date.now()) {
  const matching = (accounts || []).filter(a => a.platform === model.platform && a.state !== "disabled");
  const limited = [], ready = [], waiting = [];
  for (const account of matching) {
    if (account.state === "blocked" || ["unauthorized", "auth"].includes(account.reason)) continue;
    const seconds = Math.max(account.cooldown_secs || 0, account.model_cooldowns?.[model.id] || 0);
    if (seconds > 0) {
      if (!Number.isFinite(receivedAt)) continue;
      const remaining = remainingCooldown(seconds, receivedAt, now);
      (remaining ? limited : waiting).push({ account, remaining, until: receivedAt + seconds * 1000 });
    } else if (account.healthy) ready.push(account);
  }
  const kind = !Number.isFinite(receivedAt) ? "unknown" : limited.length ? ready.length ? "partial" : "limited" : ready.length ? "ready" : waiting.length ? "awaiting_update" : matching.length ? "unavailable" : "unknown";
  return { kind, ready: ready.length, total: matching.length, limited, waiting, nextReset: limited.length ? Math.min(...limited.map(row => row.until)) : null };
}
export function filterModelCatalog(models, filters = {}) {
  const q = (filters.query || "").trim().toLowerCase();
  return models.filter((m) => (!filters.platform || filters.platform === "all" || m.platform === filters.platform) &&
    (!filters.capability || filters.capability === "all" || m[filters.capability]) &&
    `${m.id} ${m.display_name || ""} ${m.routing_id || `${m.platform}/${m.id}`}`.toLowerCase().includes(q));
}
export function compareModelIdentity(a, b, platformOrder = []) {
  const rank = (id) => { const index = platformOrder.indexOf(id); return index < 0 ? platformOrder.length : index; };
  return String(a.id).localeCompare(String(b.id)) || rank(a.platform) - rank(b.platform) || String(a.platform).localeCompare(String(b.platform));
}
export function remainingCooldown(seconds, receivedAt, now = Date.now()) {
  if (!(seconds > 0) || !Number.isFinite(receivedAt)) return 0;
  return Math.max(0, Math.ceil(seconds - Math.max(0, now - receivedAt) / 1000));
}
export function availabilityState(account, supported, modelSeconds, modelReceivedAt, now = Date.now()) {
  if (!account) return { kind: "unknown" };
  if (account.state === "disabled") return { kind: "disabled" };
  if (account.state === "blocked" || ["unauthorized", "auth"].includes(account.reason)) return { kind: "blocked" };
  if (!account.healthy) return { kind: "account_unavailable" };
  if (!supported) return { kind: "unsupported" };
  if (modelSeconds > 0) {
    const remaining = remainingCooldown(modelSeconds, modelReceivedAt, now);
    return { kind: remaining ? "cooling" : "awaiting_update", remaining };
  }
  return { kind: "supported" };
}
export function pageSnapshotTime(page, receivedAt, matrix = false) {
  const keys = page === "overview" ? ["metrics", "accounts"] : page === "models" && matrix ? ["accounts", "matrix"] : refreshResources(page, matrix).filter((key) => key !== "status");
  const times = keys.map((key) => receivedAt[key]);
  return times.length && times.every(Number.isFinite) ? Math.min(...times) : null;
}
