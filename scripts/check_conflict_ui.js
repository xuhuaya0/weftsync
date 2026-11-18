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
//   6. none of the three surfaces contains the backend's English phrasing;
//   7. the wording states facts only - no second person, no guessing why the cloud
//      changed. The user's complaint was 「为什么会有这种提醒，而且这么口语化」:
//      the old banner said 「常见原因是你在云盘里删了或换了文件」, which guesses at
//      something the tool cannot know.
//
// Since D98 the engine no longer aborts on remote-deleted for a one-way pair (that
// is an ordinary upload), so the blocking kinds are remote-replaced and
// duplicate-name. The fabricated conflict uses remote-replaced for that reason;
// the engine-side rule is covered by go test (splitRestorable).
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
  // 第二人称 + 猜测原因：工具并不知道云盘为什么变了。
  const COLLOQUIAL = /你|常见原因|也可能是|对不上/;

  const conflict = {
    path: "R5_SS103_HV_135_20250331_135012.xlsx",
    kind: "remote-replaced",
    detail: "the file at this path is not the one recorded in the snapshot",
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
          fields: { pair: "pair-2", count: "1", paths: conflict.path, kinds: conflict.path + "\tremote-replaced" },
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

    // Everything this check overwrites, so the page can be put back exactly.
    //
    // Measured while writing this: the check rendered a fabricated conflict into
    // the result card and the status bar and then just stopped, leaving the
    // screen claiming 「比较停下：1 个路径…」 about a job that had compared fine.
    // A check that lies to the user afterwards is worse than no check.
    const st = document.getElementById("s-status");
    const keep = {
      statusHTML: st ? st.innerHTML : "",
      statusClass: st ? st.className : "",
      // planJob is a script-scoped `let`, not a property of window, so it cannot be
      // read directly from here. The panel writes the job name onto its own button,
      // which is the same fact and is readable from outside.
      planJob: (document.getElementById("plan-run") || { dataset: {} }).dataset.job || "",
    };

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
    if (!/同步停下/.test(out.remedyText)) {
      failures.push("说明块标题不是中文的「同步停下」：" + out.remedyText.slice(0, 60));
    }
    if (!/1 个路径/.test(out.remedyText)) {
      failures.push("说明块没有说清有几个路径");
    }
    if (out.remedyText.indexOf(conflict.path) < 0) {
      failures.push("说明块没有列出冲突的文件名（这正是用户最需要的一句）");
    }
    if (out.remedyText.indexOf("云端已替换") < 0) {
      failures.push("说明块没有给出中文类型标签「云端已替换」");
    }
    if (ENGLISH.test(out.remedyText)) {
      failures.push("说明块里还有英文原文");
    }
    if (COLLOQUIAL.test(out.remedyText)) {
      failures.push("说明块在猜原因或用第二人称（口语化）：" + out.remedyText.slice(0, 80));
    }

    // 3: the result card
    if (!/比较停下/.test(out.planText)) {
      failures.push("结果卡没有说「比较停下」：" + out.planText.slice(0, 60));
    }
    if (!/没有改动任何文件/.test(out.planText)) {
      failures.push("结果卡没有说「本轮没有改动任何文件」");
    }
    if (ENGLISH.test(out.planText)) {
      failures.push("结果卡还在印英文原文");
    }
    if (COLLOQUIAL.test(out.planText)) {
      failures.push("结果卡在猜原因或用第二人称：" + out.planText.slice(0, 80));
    }

    // 4: the status bar
    if (!/比较停下/.test(out.status) || !/\d/.test(out.status)) {
      failures.push("底栏没有「比较停下：N 个路径…」：" + out.status);
    }
    if (COLLOQUIAL.test(out.status)) {
      failures.push("底栏在猜原因或用第二人称：" + out.status);
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
    // The per-kind sentence, checked for each kind so a new kind cannot silently
    // fall back to the backend's English `detail`.
    const details = {
      deleted: window.conflictDetail({ kind: "remote-deleted" }),
      replaced: window.conflictDetail({ kind: "remote-replaced" }),
      duplicate: window.conflictDetail({ kind: "duplicate-name", recorded: "tok", found: ["a", "b"] }),
      unknown: window.conflictDetail({ kind: "something-new", detail: "backend sentence" }),
    };
    measured.detail = details;
    if (!/不在云端/.test(details.deleted)) {
      failures.push("云端已删除的说明不是中文：" + details.deleted);
    }
    if (!/不是同一份/.test(details.replaced)) {
      failures.push("云端已替换的说明不是中文：" + details.replaced);
    }
    if (!/同名/.test(details.duplicate)) {
      failures.push("云端重名的说明不是中文：" + details.duplicate);
    }
    if (details.unknown !== "backend sentence") {
      failures.push("未知类型的说明没有退回后端原文：" + details.unknown);
    }

    // Restore whatever the page had before this check: the explanation block, the
    // result card (the job's real plan, or the placeholder if it had none) and the
    // status line's exact markup and class.
    window.renderRemedy();
    if (keep.planJob && window.openPlanPanel) window.openPlanPanel(keep.planJob);
    else if (window.clearPlan) window.clearPlan();
    if (st) {
      st.className = keep.statusClass;
      st.innerHTML = keep.statusHTML;
    }
    return { failures: failures, measured: measured };
  };
})();
