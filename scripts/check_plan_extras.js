// Does the comparison show files that exist only in the cloud?
//
// Where this came from - the user, after deleting six files locally:
//   「当我在本地删除了几个文件之后对比没有对比出新的东西啊。这是预期的吗」
//
// Half of it was expected: `update` runs rclone's CopyDir, which never removes
// anything on the destination, so those six paths have no action. The other half
// was not: the table did not list them at all, so "nothing to do" and "six files
// still up there" rendered identically.
//
// What it asserts:
//   1. destination-only rows appear, one per path, with the name and size in the
//      right-hand pane and an empty left-hand pane;
//   2. the action symbol is neutral (·), not the delete ✗ - this variant deletes
//      nothing, and a cross would promise a deletion that will not happen;
//   3. they cannot be ticked, so they never become an include filter for a run;
//   4. they are not counted as work: the footer total and 概览 stay on the
//      actionable rows, while a separate · N counter shows the leftovers;
//   5. the status line says both facts at once - "N 项要处理" and "云端有 M 个本地
//      没有的文件（更新不删除）" - because either one alone is misleading.
//
// How to run it (needs a pair whose destination holds at least one file the source
// does not - the demo fixture in .scratch does):
//   1. daemon on a disposable port, open the console
//   2. addScriptTag({ path: '...\scripts\check_plan_extras.js' })
//   3. await page.evaluate(() => window.__checkPlanExtras())
//
// To prove it can fail: drop the `if variant != VariantMirror { rows =
// addDestinationExtras(...) }` block from job/run.go and the first assertion
// fails with 0 leftover rows.
(function () {
  "use strict";

  const sleep = ms => new Promise(r => setTimeout(r, ms));

  window.__checkPlanExtras = async function () {
    const failures = [];
    const notes = {};
    const st = () => document.getElementById("s-status").textContent || "";
    const rows = () => Array.from(document.querySelectorAll("#cmp-rows tr"));

    const actionOf = tr => {
      const cell = tr.querySelector(".ffs-a");
      return cell ? cell.className.replace(/\s+/g, " ") : "";
    };

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

    const extras = rows().filter(tr => /ffs-a extra/.test(actionOf(tr)));
    notes.totalRows = rows().length;
    notes.extraRows = extras.length;
    if (extras.length === 0) {
      failures.push(
        "对比表里没有任何「云端多余」的行 —— 本地删除过的文件在界面上仍然看不见" +
        "（先往云端放一个本地没有的文件再跑这条检查）");
      return { failures, notes };
    }

    // 1 + 2: one row per path, neutral symbol, name on the right only.
    notes.sample = extras.slice(0, 3).map(tr => tr.innerText.replace(/\s+/g, " ").trim());
    for (const tr of extras) {
      const cells = Array.from(tr.querySelectorAll("td"));
      const sym = tr.querySelector(".ffs-act");
      if (!sym) { failures.push("多余行没有动作符号"); break; }
      if (sym.textContent.trim() !== "·") {
        failures.push(`多余行用了「${sym.textContent.trim()}」符号，应该是中性的 ·`);
        break;
      }
      // cells: [box, leftName, leftSize, action, rightName, rightSize]
      if (cells[1] && cells[1].textContent.trim()) {
        failures.push("多余行在左栏写了名字，但本地并没有这个文件");
        break;
      }
      if (!cells[4] || !cells[4].textContent.trim()) {
        failures.push("多余行没有在右栏写出文件名 —— 那就等于没说清是哪个文件");
        break;
      }
      if (!cells[5] || !cells[5].textContent.trim()) {
        failures.push("多余行没有在右栏写出大小");
        break;
      }
    }

    // 3: not tickable.
    for (const tr of extras) {
      if (tr.querySelector('input[type="checkbox"]')) {
        failures.push("多余行可以勾选 —— 勾中会变成同步的 include 过滤条件");
        break;
      }
    }
    const master = document.getElementById("cmp-all-box");
    if (master && !master.disabled) {
      const before = extras[0].className;
      master.checked = true;
      master.dispatchEvent(new Event("change"));
      await sleep(120);
      if (extras[0].className !== before) {
        failures.push("全选把多余行也选进去了");
      }
      // Back to the state the check found: everything ticked. Leaving it unchecked
      // put 「已勾选 0 / 4 项，只处理勾选的」 on screen afterwards, which is a
      // truthful sentence about a state the check itself created.
      master.checked = true;
      master.dispatchEvent(new Event("change"));
      await sleep(120);
      if (/已勾选 0 \//.test(document.getElementById("cmp-more").innerText || "")) {
        failures.push("检查结束后页面停在「已勾选 0」上 —— 检查不应该改变用户的选择");
      }
    }

    // 4: the footer keeps work and leftovers apart.
    const bar = (document.getElementById("cmp-bar") || {}).innerText || "";
    notes.bar = bar.replace(/\s+/g, " ");
    if (!new RegExp("·\\s*" + extras.length).test(notes.bar)) {
      failures.push(`底部没有单独的「· ${extras.length}」计数：${notes.bar}`);
    }
    const totalText = (/共\s*(\d+)\s*个文件/.exec(notes.bar) || [])[1];
    const actionable = rows().filter(tr => !/ffs-a extra/.test(actionOf(tr))).length;
    notes.actionable = actionable;
    if (totalText !== undefined && Number(totalText) !== actionable) {
      failures.push(`「共 ${totalText} 个文件」把不处理的也算进去了（应为本 ${actionable} 个）`);
    }

    // 5: the status line carries both numbers.
    notes.status = st();
    if (!/云端有\s*\d+\s*个本地没有的文件/.test(notes.status)) {
      failures.push("底栏没有说云端还留着几个文件：" + notes.status);
    }
    if (!/不删除/.test(notes.status)) {
      failures.push("底栏没有说明这一轮不会删除它们，读者会以为点同步就清掉了");
    }

    return { failures, notes };
  };
})();
