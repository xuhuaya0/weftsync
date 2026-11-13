// Does 按此执行 produce any visible feedback?
//
// Where this came from - the user, after clicking it:
//   「点击按此执行后也没什么反应呢」
//
// The handler posted the run and then called closePlanPanel(). That wiped the
// comparison table, printed 「已清除比较结果（没有改动任何文件）」 while a run was in
// fact starting, and left nothing on screen connected to the work. Independent of
// whether the run started, the page had told the user that nothing happened.
//
// Three things have to hold, and none of them is "the request returned 200":
//
//   1. immediately after the click the card says something is in progress -
//      otherwise a 5-second run looks like a dead button;
//   2. while it runs, the card is not empty (the live headline must survive the
//      first poll, which has no progress object yet);
//   3. when it finishes, the card comes back as the plan it executed, with the
//      button reset - a button still reading 启动中… next to a finished run reads
//      as stuck.
//
// How to run it (needs a job that is cheap and harmless to execute - a local pair):
//   1. daemon on a disposable port, e.g.
//      weftsync-ui.exe daemon .scratch\small.toml --config .scratch\rclone-test.conf --addr 127.0.0.1:5741
//   2. open that address, then addScriptTag({ path: '...\scripts\check_plan_run.js' })
//   3. await page.evaluate(() => window.__checkPlanRun())
(function () {
  "use strict";

  const sleep = ms => new Promise(r => setTimeout(r, ms));

  window.__checkPlanRun = async function () {
    const failures = [];
    const notes = {};
    const btn = () => document.getElementById("plan-run");
    const body = () => document.getElementById("plan-body").innerText.replace(/\s+/g, " ").trim();

    if (!sessionCfg) return { failures: ["没有可用的配置"], notes };

    // A comparison is the precondition: the button is generated from it.
    //
    // The old plan is cleared first. Otherwise "the button has a job" is satisfied by
    // the *previous* comparison, which made this check click during a scan that was
    // still running - measured as a false "the button did not change" failure.
    const closeBtn = document.getElementById("plan-close");
    if (closeBtn && !closeBtn.disabled) { closeBtn.click(); await sleep(150); }

    document.getElementById("s-compare-btn").click();
    let ready = false;
    for (let i = 0; i < 120; i++) {
      await sleep(250);
      const scanning = /正在扫描|正在比较/.test(document.getElementById("s-status").textContent);
      if (btn().dataset.job && !scanning) { ready = true; break; }
      const job = (snapshot.jobs || [])[0] || {};
      if (job.state !== "running" && btn().disabled && i > 8) break;
    }
    notes.afterCompare = { ready, text: btn().textContent.trim(), status: document.getElementById("s-status").textContent };
    if (!ready) return { failures: ["比较没有产出可执行的计划，「按此执行」无法测试"], notes };
    // An empty plan (both sides already equal) also disables the button, and for a
    // good reason. Saying so is more useful than "the click did nothing": the
    // fixture has to contain a difference for this check to mean anything.
    if (btn().disabled && /没有需要变更/.test(document.getElementById("s-status").textContent)) {
      return { failures: ["测试数据里两侧已一致，计划为空 —— 先制造一个差异再跑这条检查"], notes };
    }

    // 1 + 2: click, then look at the card right away and again a moment later.
    btn().click();
    await sleep(120);
    const immediate = { text: btn().textContent.trim(), body: body() };
    notes.immediate = immediate;
    if (!/启动中|同步中|正在/.test(immediate.text)) {
      failures.push(`点击后按钮没有进入进行中状态（仍为「${immediate.text}」）`);
    }
    if (!immediate.body) {
      failures.push("点击后结果区立刻变成空白 —— 用户看不到任何正在发生的事");
    }
    if (/已清除比较结果/.test(document.getElementById("s-status").textContent)) {
      failures.push("启动同步时底栏写「已清除比较结果」：句子与刚刚发生的动作相反");
    }

    await sleep(900);
    const during = body();
    notes.during = during.slice(0, 90);
    if (!during) {
      failures.push("同步开始约 1 秒后结果区仍是空白（首轮轮询把标题清掉了）");
    }

    // 3: wait for the run, then check the card came back.
    for (let i = 0; i < 80; i++) {
      await sleep(250);
      const job = (snapshot.jobs || [])[0] || {};
      if (job.state !== "running" && i > 3) break;
    }
    await sleep(900);
    const after = {
      text: btn().textContent.trim(),
      disabled: btn().disabled,
      job: btn().dataset.job,
      body: body().slice(0, 90),
      status: document.getElementById("s-status").textContent.trim(),
    };
    notes.after = after;

    if (!after.job) failures.push("同步结束后比较结果没有回到卡片上（计划丢失）");
    if (after.text !== "按此执行") {
      failures.push(`同步结束后按钮文字仍为「${after.text}」，应为「按此执行」`);
    }
    if (/还没有比较结果/.test(after.body)) {
      failures.push("同步结束后卡片退回「还没有比较结果」，把刚执行的计划删掉了");
    }

    if (failures.length) {
      console.error("plan-run: %d failure(s)", failures.length);
      failures.forEach(f => console.error("  " + f));
    } else {
      console.log("plan-run: OK - click shows progress, card restores after the run");
    }
    return { failures, notes };
  };
})();
