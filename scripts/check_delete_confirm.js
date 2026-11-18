// Is the pre-run confirmation good enough to BE the protection?
//
// D103. The user's words:
//   「删除前确认 + 这个方案不好吗？你还可以保留这个最大删除，但是可以加个开关默认关闭吗？
//     我觉得不需要啊」
//
// They are right about the shape: a number chosen in advance cannot know what this
// run is about to do, and `max_delete` answered "68 deletions" and "11 deletions"
// the same way - stop, go read the config. So the delete ceilings became opt-in
// (delete_guard, default false) and the confirmation took over. Which means the
// confirmation has to carry everything the guard used to imply:
//
//   1. how many items will be deleted;
//   2. WHICH items - paths, not just a count;
//   3. whether they can be recovered (deletion = permanent / versioning);
//   4. whether any ceiling is in force, so nobody assumes a safety net that is off;
//   5. it must appear even when every row is ticked. It used to be skipped then,
//      and 按此执行 never asked at all - so "select everything, press execute" was
//      the one path that deleted a whole mirror with no question;
//   6. unticked rows must not be counted: a number the run will not act on is as
//      wrong as an under-reported one.
//
// How to run it (needs a pair whose plan deletes at least one file):
//   1. daemon on a disposable port, open the console
//   2. addScriptTag({ path: '...\scripts\check_delete_confirm.js' })
//   3. await page.evaluate(() => window.__checkDeleteConfirm())
// (the check compares first if no plan is present)
//
// It replaces window.confirm with a recorder that always answers "cancel", so
// running it never deletes anything.
//
// To prove it can fail: replace beforeRunConfirm() with the old
// `"开始同步「"+job+"」？" + planSummaryText(plan)` and assertions 1-4 fail.
(function () {
  "use strict";

  const sleep = ms => new Promise(r => setTimeout(r, ms));

  // captureConfirm clicks something and returns the confirm text it produced, or
  // null when nothing was asked. window.confirm is replaced so the answer is
  // always "cancel" and no run starts.
  async function captureConfirm(click) {
    const prev = window.confirm;
    let text = null;
    window.confirm = m => { text = String(m == null ? "" : m); return false; };
    try {
      click();
    } finally {
      window.confirm = prev;
    }
    await sleep(250);
    return text;
  }

  window.__checkDeleteConfirm = async function () {
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

    notes.guardEnabled = (job.plan.results || []).some(r => r.guard_enabled);
    const del = deletionDetail(job.plan);
    notes.deletes = del.count;
    if (!del.count) {
      failures.push("这次计划没有删除项 —— 换一个会删除的目录对再跑（镜像 + 本地文件更少）");
      return { failures, notes };
    }

    // 1-4: the message itself.
    const msg = await captureConfirm(() => document.getElementById("s-sync-btn").click());
    notes.message = msg;
    if (msg === null) {
      failures.push("点「同步」没有弹出任何确认 —— 删除会毫无提示地发生");
      return { failures, notes };
    }
    if (msg.indexOf(String(del.count)) < 0) {
      failures.push(`确认里没有说会删除 ${del.count} 项`);
    }
    const shown = del.paths.slice(0, 5).filter(p => msg.indexOf(p) >= 0);
    notes.shownPaths = shown.length;
    if (shown.length < Math.min(5, del.paths.length)) {
      failures.push(`确认里没有列出要删的文件名（只找到 ${shown.length} 个）`);
    }
    if (!/直接删除|版本|回收站|不可撤销/.test(msg)) {
      failures.push("确认里没有说被删的东西能不能找回");
    }
    if (!/删除保护(开着|关着)/.test(msg)) {
      failures.push("确认里没有说删除保护是开还是关 —— 关着时读者会以为有安全网");
    }
    if (notes.guardEnabled === false && !/删除保护关着/.test(msg)) {
      failures.push("删除保护是关的，确认里却说不出这一点");
    }

    // 5: 按此执行 has to ask too.
    const runBtn = document.getElementById("plan-run");
    if (runBtn.disabled) {
      notes.planRunSkipped = "按钮被禁用（计划过期或为空），这一条不适用";
    } else {
      const runMsg = await captureConfirm(() => runBtn.click());
      notes.planRunMessage = runMsg;
      if (runMsg === null) {
        failures.push("「按此执行」没有确认就启动了删除 —— 全选时它会一次删掉整份计划");
      } else if (runMsg.indexOf(String(del.count)) < 0) {
        failures.push("「按此执行」的确认没有说要删除多少项");
      }
    }

    // 6: an unticked delete row drops out of the count.
    const firstDel = Array.from(document.querySelectorAll("#cmp-rows tr")).find(tr =>
      /ffs-a deletes/.test((tr.querySelector(".ffs-a") || {}).className || ""));
    if (firstDel) {
      const box = firstDel.querySelector('input[type="checkbox"]');
      const after = () => deletionDetail(((snapshot.jobs || [])[0] || {}).plan);
      if (box) {
        box.checked = false;
        box.dispatchEvent(new Event("change"));
        await sleep(150);
        notes.afterUntick = after().count;
        if (notes.afterUntick !== del.count - 1) {
          failures.push(`取消勾选一行后应删 ${del.count - 1} 项，实际算出 ${notes.afterUntick}`);
        }
        box.checked = true;
        box.dispatchEvent(new Event("change"));
        await sleep(150);
        if (after().count !== del.count) {
          failures.push("重新勾选后删除数没有回到原来的值");
        }
      }
    }

    return { failures, notes };
  };
})();
