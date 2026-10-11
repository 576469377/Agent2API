// Preserve the console's optional math rendering. Only formula-containing
// responses load the pinned KaTeX assets; offline responses keep readable text.
let loading = null,
  retryAt = 0;
const pending = new Set();
const base = "https://cdn.jsdelivr.net/npm/katex@0.16.11/dist/";
function load() {
  if (window.katex) return Promise.resolve();
  if (loading) return loading;
  if (Date.now() < retryAt)
    return Promise.reject(new Error("Math renderer unavailable"));
  if (!document.getElementById("katex-css")) {
    const css = document.createElement("link");
    css.id = "katex-css";
    css.rel = "stylesheet";
    css.href = base + "katex.min.css";
    document.head.append(css);
  }
  loading = new Promise((resolve, reject) => {
    const script = document.createElement("script");
    script.src = base + "katex.min.js";
    script.referrerPolicy = "no-referrer";
    const timer = setTimeout(() => {
      script.remove();
      reject(new Error("Math renderer timed out"));
    }, 10000);
    script.onload = () => {
      clearTimeout(timer);
      resolve();
    };
    script.onerror = () => {
      clearTimeout(timer);
      script.remove();
      reject(new Error("Math renderer unavailable"));
    };
    document.head.append(script);
  }).catch((error) => {
    loading = null;
    retryAt = Date.now() + 30000;
    throw error;
  });
  return loading;
}
export function renderMath(root) {
  if (!root.querySelector(".math[data-tex]")) return;
  pending.add(root);
  load()
    .then(() => {
      for (const element of pending) {
        if (element.isConnected)
          element.querySelectorAll(".math[data-tex]").forEach((formula) => {
            try {
              window.katex.render(formula.dataset.tex, formula, {
                displayMode: formula.dataset.display === "true",
                throwOnError: false,
                trust: false,
                strict: "ignore",
              });
            } catch {}
          });
        pending.delete(element);
      }
    })
    .catch(() => pending.clear());
}
