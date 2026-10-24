// Lightweight check of buildOps / statusText semantics (mirrors web/index.html).
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
