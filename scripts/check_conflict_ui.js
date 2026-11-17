// Conflict-in-the-console checker: does a conflict abort name the file, in Chinese,
// without printing the same English paragraph three times?
//
// Where this came from - the user, after deleting one file in Feishu and pressing
// 同步:
//   我同步了一次之后在云盘删除了一个文件，再次点击同步报错
// The screenshot showed the same 400-character English sentence in the failure
// block, the result card and the status bar, and NOTHING said which file.
//
// What it asserts:
//   1. the failure block's title is Chinese and carries the count;
//   2. the conflicting paths are listed, each with a Chinese kind label;
//   3. the result card says the comparison stopped for conflicts, in one short
//      Chinese sentence, and does not print the raw error;
//   4. the status bar is one Chinese line pointing at the explanation;
//   5. in the conflicts table, 以云端为准 is disabled for remote-deleted (there is
//      no cloud copy left to pull) with a reason in the title, while 以本地为准
//      stays available;
//   6. none of the three surfaces contains the backend's English phrasing.
//
// It drives the page's own renderers with fabricated data rather than arranging a
// real conflict: the local backend has no `conflicts` command, so a real one needs
// a Feishu tenant and a deliberately deleted file - not something a check should do
// to somebody's cloud folder.
//
// How to run it:
//   1. Start the daemon, open the console in the browser tools.
//   2. addScriptTag({ path: '...\scripts\check_conflict_ui.js' })
//   3. await page.evaluate(() => window.__checkConflictUI())
//
// To prove it can fail: delete the `conflict` entry from REMEDY_TEXT (the block
// falls back to "这一条还没有对应的处理说明") or drop the pullButton() special case.
(function () {
  "use strict";

  const ENGLISH = /pair "|conflict\(s\)|NOTHING was uploaded|review the report above/i;

  const conflict = {
    path: "R5_SS103_HV_135_20250331_135012.xlsx",
    kind: "remote-deleted",
    detail: "the file recorded in the snapshot is no longer in the folder",
  };

  // The shape the daemon sends: a job whose last preview aborted on conflicts.
  //
  // last_run MUST be older than plan.at. With both at the same instant
  // latestOutcome() falls through to the run, whose last_ok is true, and the
  // failure block then correctly stays hidden - which looked like "the block is
  // broken" while writing this script.
  function fakeJob() {
    return {
      name: "__conflict_probe__",
      state: "idle",
      last_run: "2026-11-17T17:00:00+08:00",
      last_ok: true,
      plan: {
        at: "2026-11-17T18:06:00+08:00",
        error: 'pair "pair-2" has 1 conflict(s) between the folder and the sync state. ' +
          "NOTHING was uploaded, overwritten or deleted. Review the report above, " +
          "then either fix the folder in the destination, run `weftsync backend dedupe` " +
          'for duplicate copies, or set conflict = "overwrite" / "keep-both" for this pair',
        remedy: {
          kind: "conflict",
          fields: { pair: "pair-2", count: "1", paths: conflict.path, kinds: conflict.path + "\tremote-deleted" },
        },
        results: [{
          pair: "pair-2", variant: "update", conflicts: [conflict], plan: [],
          plan_summary: {}, dry_run: {}, actual: {},
        }],
        guard_hit: false, conflicts: 1, transferred: 0, deleted: 0,
      },
    };
  }

  // Only remedySubject is overridable: it is a function declaration, so it lives on
  // window. `snapshot` and `CONFLICT_KIND` are let/const - script-scoped, invisible
  // from here - so the conflicts TABLE cannot be driven with fabricated data. Its
  // rendering is covered by the per-button markup assertions below plus a manual
  // check against a real conflict (a local backend has no `conflicts` command, so a
  // real one needs a cloud folder and a deliberately deleted file).
  function withJob(job, fn) {
    const prevRemedy = window.remedySubject;
    const prevPlan = window.planJob;
    window.remedySubject = () => job;
    window.planJob = job.name;
    try {
      return fn();
    } finally {
      window.remedySubject = prevRemedy;
      window.planJob = prevPlan;
    }
  }

  window.__checkConflictUI = function () {
    const failures = [];
    const measured = {};
    const job = fakeJob();

    const out = withJob(job, () => {
      window.renderRemedy();
      const remedy = document.getElementById("s-remedy");
      document.getElementById("plan-body").innerHTML = planHTML(job.plan);
      const status = (finishStatus(job, "plan"), document.getElementById("s-status").textContent || "");
      return {
        remedyText: remedy.innerText || "",
        remedyShown: !remedy.classList.contains("hidden"),
        planText: document.getElementById("plan-body").innerText || "",
        status: status,
      };
    });

    measured.remedyText = out.remedyText;
    measured.planText = out.planText;
    measured.status = out.status;

    // 1 + 2: the failure block
    if (!out.remedyShown) {
      failures.push("冲突失败时说明块没有显示");
    }
    if (!/同步停下了/.test(out.remedyText)) {
      failures.push("说明块标题不是中文的「同步停下了」：" + out.remedyText.slice(0, 60));
    }
    if (!/1 个文件/.test(out.remedyText)) {
      failures.push("说明块没有说清有几个文件");
    }
    if (out.remedyText.indexOf(conflict.path) < 0) {
      failures.push("说明块没有列出冲突的文件名（这正是用户最需要的一句）");
    }
    if (out.remedyText.indexOf("云端已删除") < 0) {
      failures.push("说明块没有给出中文类型标签「云端已删除」");
    }
    if (ENGLISH.test(out.remedyText)) {
      failures.push("说明块里还有英文原文");
    }

    // 3: the result card
    if (!/比较停下/.test(out.planText)) {
      failures.push("结果卡没有说「比较停下」：" + out.planText.slice(0, 60));
    }
    if (!/什么都没改/.test(out.planText)) {
      failures.push("结果卡没有说「什么都没改」");
    }
    if (ENGLISH.test(out.planText)) {
      failures.push("结果卡还在印英文原文");
    }

    // 4: the status bar
    if (!/比较停下/.test(out.status) || !/\d/.test(out.status)) {
      failures.push("底栏没有「比较停下：N 个…」：" + out.status);
    }
    if (ENGLISH.test(out.status)) {
      failures.push("底栏还在印英文原文");
    }

    // 5: the per-row actions, asserted through their own builder
    const deleted = window.pullButton({ kind: "remote-deleted", job: "j", pair: "p", path: "a.txt" });
    const replaced = window.pullButton({ kind: "remote-replaced", job: "j", pair: "p", path: "a.txt" });
    measured.deletedPull = deleted;
    measured.replacedPull = replaced;
    if (!/disabled/.test(deleted)) {
      failures.push("云端已删除时「以云端为准」还能点 —— 点了只会报 object not found");
    }
    if (!/已经没了/.test(deleted)) {
      failures.push("禁用的「以云端为准」没有在 title 里说明原因");
    }
    if (/disabled/.test(replaced)) {
      failures.push("云端已替换时「以云端为准」被禁用了 —— 那是有东西可下载的正常情况");
    }
    const detail = window.conflictDetail(conflict);
    measured.detail = detail;
    if (!/不在云端/.test(detail)) {
      failures.push("冲突说明不是中文：" + detail);
    }

    // Restore whatever the page had before this check.
    window.renderRemedy();
    return { failures: failures, measured: measured };
  };
})();
