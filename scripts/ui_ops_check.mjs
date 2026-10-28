// Lightweight check of buildOps / statusText semantics (mirrors web/index.html).
import fs from "fs";
import vm from "vm";
function needsPick(t) { return t.status === "paused" && t.bittorrent && t.bittorrent.info && +t.completedLength === 0; }
const inflight = new Set();
const btn = (a, label, cls) => `<button data-a="${a}"${cls ? ` class="${cls}"` : ""}>${label}</button>`;
function buildOps(t) {
  let ops = "";
  const busy = inflight.has(t.gid);
  if (t.status === "paused") {
    ops += needsPick(t)
      ? btn("pick", "选择文件") + btn("resume", busy ? "处理中…" : "开始")
      : btn("resume", busy ? "处理中…" : "继续");
  } else if (t.status === "active" || t.status === "waiting") {
    ops += btn("pause", busy ? "处理中…" : "暂停");
  }
  if (t.status === "error" || t.status === "removed") ops += btn("retry", "重新开始");
  if (t.status === "complete" || t.status === "error") ops += btn("open", "打开");
  if (t.bittorrent && t.bittorrent.info && !needsPick(t)) ops += btn("pick", "选文件");
  ops += btn("more", "⋯", "more");
  return ops;
}
function labels(ops) {
  return [...ops.matchAll(/>([^<]+)<\/button>/g)].map(m => m[1]);
}
function countPause(ops) { return labels(ops).filter(x => x === "暂停").length; }

const cases = [
  { name: "active-bt", t: { gid: "1", status: "active", bittorrent: { info: { name: "x" } } }, expect: ["暂停", "选文件", "⋯"], pause: 1 },
  { name: "waiting", t: { gid: "2", status: "waiting" }, expect: ["暂停", "⋯"], pause: 1 },
  { name: "paused", t: { gid: "3", status: "paused" }, expect: ["继续", "⋯"], pause: 0 },
  { name: "paused-pick", t: { gid: "4", status: "paused", completedLength: 0, bittorrent: { info: { name: "x" } } }, expect: ["选择文件", "开始", "⋯"], pause: 0 },
  { name: "complete", t: { gid: "5", status: "complete", bittorrent: { info: { name: "x" } } }, expect: ["打开", "选文件", "⋯"], pause: 0 },
  { name: "error-http", t: { gid: "6", status: "error" }, expect: ["重新开始", "打开", "⋯"], pause: 0 },
  { name: "active-http", t: { gid: "7", status: "active" }, expect: ["暂停", "⋯"], pause: 1 },
];

let failed = 0;
for (const c of cases) {
  const ops = buildOps(c.t);
  const got = labels(ops);
  const p = countPause(ops);
  const ok = p === c.pause && c.expect.every((e, i) => got[i] === e) && got.length === c.expect.length;
  if (!ok) {
    console.error("FAIL", c.name, { got, expect: c.expect, pause: p });
    failed++;
  } else {
    console.log("ok", c.name, got.join("|"));
  }
}
if (failed) process.exit(1);
console.log("all ops cases passed");

const html = fs.readFileSync(new URL("../web/index.html", import.meta.url), "utf8");
function extract(name) {
  const start = html.indexOf("function " + name + "(");
  if (start < 0) throw new Error("missing " + name);
  let i = html.indexOf("{", start);
  let depth = 0;
  for (let j = i; j < html.length; j++) {
    if (html[j] === "{") depth++;
    else if (html[j] === "}") {
      depth--;
      if (depth === 0) return html.slice(start, j + 1);
    }
  }
  throw new Error("unclosed " + name);
}
const ctx = {
  fmtSize: (n) => String(n),
  isMeta: () => false,
};
vm.createContext(ctx);
vm.runInContext(
  extract("isResumeCheck") + "\n" + extract("progressView") + "\n" + extract("statusText") + "\n" +
  "this.isResumeCheck=isResumeCheck; this.progressView=progressView; this.statusText=statusText;",
  ctx
);
const checking = {
  status: "active", engine: "bt", btChecking: "true", btCheckPercent: "40",
  totalLength: "1000", completedLength: "0", btResumeBytes: "400", downloadSpeed: "0",
  bittorrent: { info: { name: "x" } }, errorMessage: "正在校验已下载的数据（断点续传，不是重新下载）"
};
const st = ctx.statusText(checking);
if (st[0] !== "校验续传" || !String(st[2]).includes("不是重新下载") || st[0] === "下载中") {
  console.error("FAIL statusText resume", st);
  process.exit(1);
}
const pv = ctx.progressView(checking);
if (!(pv.pct > 30) || !String(pv.hint).includes("上次") || String(pv.hint).startsWith("0.0%")) {
  console.error("FAIL progressView resume", pv);
  process.exit(1);
}
const normal = ctx.progressView({ status: "active", totalLength: "1000", completedLength: "250" });
if (Math.abs(normal.pct - 25) > 0.01 || !String(normal.hint).startsWith("25.0%")) {
  console.error("FAIL progressView normal", normal);
  process.exit(1);
}
console.log("ok resume-check", st[0], pv.hint);
