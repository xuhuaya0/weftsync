// Does the result card describe the present after a sync?
//
// Where this came from - the user, right after pressing 同步:
//   「目前点完同步之后不应该自动显示新的比较结果吗？或者直接显示没有差异，
//     怎么还是显示的跟之前一样？」
//
// The daemon never touched the previous preview when a real run finished, and the
// page restored that preview afterwards (restorePlanCard). So the table under the
// words 同步完成 was measured BEFORE the run, looked exactly like a fresh one, and
// stayed on screen through page reloads. "I just synced" and "I compared fifty
// files ago" were indistinguishable.
//
// Two things have to hold now:
//   1. a run marks its preview stale, and a stale preview says so, in Chinese, and
//      cannot be executed (按此执行 disabled with a reason);
//   2. after a successful 同步 the page re-measures by itself, so the card ends up
//      on the current state - "两侧一致" if that is the truth.
//
// How to run it (needs a job that is cheap and harmless to execute - a local pair,
// and a fixture that currently has a difference):
//   1. daemon on a disposable port, e.g.
//      weftsync-ui.exe daemon .scratch\small.toml --config .scratch\rclone-test.conf --addr 127.0.0.1:5742
//   2. open that address, then addScriptTag({ path: '...\scripts\check_plan_freshness.js' })
//   3. await page.evaluate(() => window.__checkPlanFreshness())
//
// To prove it can fail: drop `if (e.lastPlan != nil) { e.lastPlan.Stale = true }`
// from daemon.execute (part 1 breaks: the stale assertion still passes because it
// drives planHTML directly, but the live part then finds plan.stale false) or
// delete the autoCompareAfterRun() call from updateBusy (part 2 breaks: the card
// keeps the pre-run table and never reaches 两侧一致).
(function () {
  "use strict";

  const sleep = ms => new Promise(r => setTimeout(r, ms));

  window.__checkPlanFreshness = async function () {
    const failures = [];
    const notes = {};
    const st = () => document.getElementById("s-status").textContent || "";
    const body = () => document.getElementById("plan-body").innerText.replace(/\s+/g, " ").trim();
    const runBtn = () => document.getElementById("plan-run");

    // ---- 1. a stale preview must not read like a current one ----------------
    //
    // Driven through the page's own formatters rather than by arranging a real
    // stop: "the table is old" is a property of the data, and fabricating the data
    // is the exact test.
    const stalePlan = {
      at: "2026-11-17T10:00:00+08:00", results: [], guard_hit: false,
      conflicts: 0, transferred: 0, deleted: 0, stale: true,
    };
    const staleHTML = planHTML(stalePlan);
    notes.staleHTML = staleHTML;
    if (!/上一次同步之前的比较结果/.test(staleHTML)) {
      failures.push("过期的比较结果没有标注，看起来和刚量的一样");
    }
    if (/没有需要变更的内容/.test(staleHTML)) {
      failures.push("过期的比较结果还说「没有需要变更的内容」：那句话是关于现在的");
    }
    if (!planRunDisabled(stalePlan)) {
      failures.push("过期计划的「按此执行」还能点：执行的是一个已经不存在的状态");
    }
    if (!/重新比较/.test(planRunTitle(stalePlan))) {
      failures.push("禁用的「按此执行」没有在 title 里说明为什么要先重新比较");
    }

    const fresh = { at: "2026-11-17T10:00:00+08:00", results: [], guard_hit: false,
      conflicts: 0, transferred: 0, deleted: 0 };
    const freshHTML = planHTML(fresh);
    notes.freshHTML = freshHTML;
    if (/上一次同步之前/.test(freshHTML)) {
      failures.push("刚量过的计划被标成了过期");
    }
    if (!/没有需要变更的内容/.test(freshHTML)) {
      failures.push("刚量过、两侧一致的计划没有说「没有需要变更的内容」");
    }
    if (planRunDisabled(fresh)) {
      failures.push("刚量过的空计划不该禁用「按此执行」");
    }

    if (!sessionCfg) {
      failures.push("没有可用的配置");
      return { failures, notes };
    }

    // ---- 2. after a real sync the card must re-measure itself ---------------
    const closeBtn = document.getElementById("plan-close");
    if (closeBtn && !closeBtn.disabled) { closeBtn.click(); await sleep(150); }

    document.getElementById("s-compare-btn").click();
    for (let i = 0; i < 120; i++) {
      await sleep(250);
      const job = (snapshot.jobs || [])[0] || {};
      const scanning = /正在扫描|正在比较/.test(st());
      if (job.plan && job.state !== "running" && !scanning) break;
    }
    const job0 = (snapshot.jobs || [])[0] || {};
    notes.beforeSync = { planAt: job0.plan && job0.plan.at, status: st(), body: body() };
    if (!job0.plan) {
      failures.push("比较没有产出计划，「同步之后会不会重算」无法测试");
      return { failures, notes };
    }
    if (/没有需要变更/.test(body())) {
      failures.push("测试数据两侧已一致 —— 先往本地目录放一个文件，再跑这条检查");
      return { failures, notes };
    }
    const planAtBefore = job0.plan.at;
    const bodyBefore = body();

    // Auto-accept the confirmation dialog; its wording is covered elsewhere.
    const realConfirm = window.confirm;
    window.confirm = () => true;
    try {
      document.getElementById("s-sync-btn").click();
    } finally {
      window.confirm = realConfirm;
    }

    // Wait for the run, then for the comparison the page starts on its own.
    let done = null;
    for (let i = 0; i < 240; i++) {
      await sleep(250);
      const job = (snapshot.jobs || [])[0] || {};
      if (job.state === "running") continue;
      if (!job.plan || job.plan.at === planAtBefore) continue;
      if (/正在扫描|正在比较|正在同步/.test(st())) continue;
      done = job;
      break;
    }
    notes.afterSync = {
      status: st(), body: body(),
      planAt: done && done.plan && done.plan.at,
      stale: done && done.plan && !!done.plan.stale,
      runDisabled: runBtn().disabled, runTitle: runBtn().title,
    };

    if (!done) {
      failures.push("同步结束后结果卡没有换成新的比较结果（等了两分钟）");
      return { failures, notes };
    }
    if (body() === bodyBefore) {
      failures.push("结果卡的内容和同步之前一字不差 —— 这正是用户报的那个现象");
    }
    if (/正在/.test(st())) {
      failures.push("同步和重算都结束了，底栏还写着「正在…」：" + st());
    }
    if (!/同步完成/.test(st())) {
      failures.push("底栏没有说同步的结果：" + st());
    }
    if (!/比较完成|比较停下|比较失败/.test(st())) {
      failures.push("底栏没有说重算的结果：一次点按产生两件事，两件都要说");
    }
    if (done.plan && done.plan.stale) {
      failures.push("刚量出来的计划被标成了过期");
    }
    if (runBtn().disabled) {
      failures.push("新计划到手了，「按此执行」却还是禁用的");
    }
    if (runBtn().title) {
      failures.push("新计划的「按此执行」还带着过期的解释：" + runBtn().title);
    }

    return { failures, notes };
  };
})();
