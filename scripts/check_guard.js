// Does a preview the delete guard would refuse say why, with numbers, and stop
// offering a button that cannot work?
//
// Where this came from - the user, after switching a pair to "mirror" while its
// local root pointed at a folder with five files and the cloud folder had
// sixty-eight:
//   「这是为什么」
//
// The answer was "a mirror makes the destination an exact copy, so it would delete
// 68 files, and max_delete is 10". The card said none of that: no counts, no limit,
// no statement that 按此执行 would do nothing, and the button was clickable.
//
// What it asserts:
//   1. the card names BOTH numbers - how many would be deleted and the limit;
//   2. it says the run will be stopped before any deletion, so nobody fears a
//      half-finished sync;
//   3. it tells the reader where to change the limit;
//   4. 按此执行 is disabled, and its title explains why;
//   5. the status line says the run would be blocked, not just "N 项要处理";
//   6. delete rows do not claim a size of 0 B - rclone's dry-run delete line
//      carries no size, and 68 rows of "0 B" next to "删除 68 项" reads as "will
//      delete nothing".
//
// How to run it (needs a pair whose planned deletions exceed its max_delete - a
// mirror pair pointed at a small local folder does it):
//   1. daemon on a disposable port, open the console
//   2. addScriptTag({ path: '...\scripts\check_guard.js' })
//   3. await page.evaluate(() => window.__checkGuard())
//
// To prove it can fail: drop `|| !!p.guard_hit` from planRunDisabled (assertion 4
// breaks) or replace guardNoteHTML(p) with the old fixed sentence (1, 2 and 3 break).
(function () {
  "use strict";

  const sleep = ms => new Promise(r => setTimeout(r, ms));

  window.__checkGuard = async function () {
    const failures = [];
    const notes = {};
    const st = () => document.getElementById("s-status").textContent || "";

    if (!sessionCfg) return { failures: ["没有可用的配置"], notes };

    const closeBtn = document.getElementById("plan-close");
    if (closeBtn && !closeBtn.disabled) { closeBtn.click(); await sleep(150); }

    document.getElementById("s-compare-btn").click();
    for (let i = 0; i < 160; i++) {
      await sleep(250);
      const job = (snapshot.jobs || [])[0] || {};
      if (job.plan && job.state !== "running" && !/正在扫描|正在比较/.test(st())) break;
    }

    const job = (snapshot.jobs || [])[0] || {};
    if (!job.plan) {
      failures.push("比较没有产出计划");
      return { failures, notes };
    }
    if (!job.plan.guard_hit) {
      notes.summary = JSON.stringify((job.plan.results || []).map(r => r.plan_summary));
      failures.push("这次预览没有触发删除保护 —— 换一个删除数超过 max_delete 的目录对再跑");
      return { failures, notes };
    }

    const deleted = (job.plan.results || []).reduce(
      (n, r) => n + ((r.plan_summary && r.plan_summary.delete_remote) || 0), 0);
    const limits = (job.plan.results || [])
      .filter(r => r.guard_hit && r.max_delete)
      .map(r => r.max_delete);
    notes.deleted = deleted;
    notes.limits = limits;
    if (!deleted) {
      failures.push("保护触发了却没有删除行，无法验证文案");
      return { failures, notes };
    }
    if (!limits.length) {
      failures.push("预览没有带出 max_delete（界面无法说出具体数字）");
    }

    // 1 + 2 + 3: the card's explanation.
    const note = (document.querySelector("#plan-body .note") || {}).innerText || "";
    notes.note = note;
    if (note.indexOf(String(deleted)) < 0) {
      failures.push(`说明块没有说要删除 ${deleted} 项：` + note.slice(0, 80));
    }
    for (const l of limits) {
      if (note.indexOf(String(l)) < 0) {
        failures.push(`说明块没有给出 max_delete = ${l}`);
      }
    }
    if (!/保护机制在任何删除发生之前中止整轮/.test(note)) {
      failures.push("说明块没有说「会在任何删除发生之前中止」，读者会担心删了一半");
    }
    if (!/配置表单/.test(note)) {
      failures.push("说明块没有告诉人去哪里改 max_delete");
    }
    if (!/本地目录填对/.test(note)) {
      failures.push("说明块没有提醒先确认本地目录 —— 镜像删光云端最常见的原因就是目录填错");
    }

    // 4: the button.
    const runBtn = document.getElementById("plan-run");
    notes.runDisabled = runBtn.disabled;
    notes.runTitle = runBtn.title;
    if (!runBtn.disabled) {
      failures.push("「按此执行」还能点，但点了只会得到一次拒绝");
    }
    if (!/max_delete/.test(runBtn.title || "")) {
      failures.push("禁用的「按此执行」没有在 title 里说明原因：" + runBtn.title);
    }

    // 5: the status line.
    notes.status = st();
    if (!/会被拦下|被拦下/.test(notes.status)) {
      failures.push("底栏只说「N 项要处理」，没有说这一轮会被拦下：" + notes.status);
    }
    if (!/max_delete/.test(notes.status)) {
      failures.push("底栏没有给出 max_delete 的值：" + notes.status);
    }

    // 6: delete rows must not invent a size.
    const delRows = Array.from(document.querySelectorAll("#cmp-rows tr"))
      .filter(tr => /ffs-a deletes/.test((tr.querySelector(".ffs-a") || {}).className || ""));
    notes.deleteRows = delRows.length;
    const zero = delRows.filter(tr => {
      const cells = Array.from(tr.querySelectorAll("td"));
      return cells.some(td => (td.textContent || "").trim() === "0 B");
    });
    if (delRows.length && zero.length) {
      failures.push(`有 ${zero.length} 行删除显示「0 B」—— dry-run 的删除日志根本没有大小`);
    }

    return { failures, notes };
  };
})();
