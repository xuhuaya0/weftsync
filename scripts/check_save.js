// Does one press of 保存 send exactly one request, and does the value reach the file?
//
// Two bugs lived behind this question (2026-11-18):
//
//   1. duplicate id: the toolbar 保存 and the 配置表单 保存 shared id="cfg-save".
//      getElementById returns the first, so renderConfig() enabled/disabled the
//      TOOLBAR button while the form's own button stayed `disabled` from markup, and
//      both click handlers were attached to the same element. One press therefore
//      fired two handlers - saveNow() (sessionCfg) and the form handler
//      (collectConfig()) - and the two POSTs raced. That is how 「已保存」 came to
//      sit next to a file holding the previous value.
//   2. writeAtomic gave up on the first ENOENT from os.Rename, which is routine on a
//      OneDrive-synced directory, so a save could fail where the user least expects
//      it.
//
// What it asserts:
//   1. exactly ONE POST /api/config per press of the form's 保存;
//   2. the value typed into 删除上限 is what the file holds afterwards (read back
//      through the API, not from the form);
//   3. pressing the toolbar 保存 does not also run the form handler;
//   4. the form's 保存 is clickable once a job is open (it used to be permanently
//      disabled);
//   5. after a save the form shows the saved value, so a second press re-sends the
//      same thing rather than a stale copy.
//
// How to run it (needs a throwaway job file - this check writes to it):
//   1. daemon on a disposable port with a scratch job
//   2. addScriptTag({ path: '...\scripts\check_save.js' })
//   3. await page.evaluate(() => window.__checkSave())
//
// To prove it can fail: give any second element id="cfg-save"; assertions 1 and 3
// break (two POSTs) and 4 breaks (the form button stays disabled).
(function () {
  "use strict";

  const sleep = ms => new Promise(r => setTimeout(r, ms));

  window.__checkSave = async function () {
    const failures = [];
    const notes = {};

    // 0. No duplicate ids anywhere: the root cause above was one, and a duplicate
    //    id is invisible until it corrupts something.
    const counts = new Map();
    for (const el of document.querySelectorAll("[id]")) {
      if (el.id) counts.set(el.id, (counts.get(el.id) || 0) + 1);
    }
    const dupes = [...counts.entries()].filter(([, n]) => n > 1).map(([id, n]) => id + "×" + n);
    notes.duplicateIds = dupes;
    if (dupes.length) failures.push("重复 id：" + dupes.join("，"));

    // Open a job in 配置表单.
    const tab = Array.from(document.querySelectorAll('button[role="tab"]'))
      .find(b => b.dataset.drawer === "config");
    if (!tab) { failures.push("找不到「配置表单」标签页"); return { failures, notes }; }
    tab.click();
    await sleep(400);
    const picker = document.getElementById("cfg-job");
    const options = Array.from(picker.options).filter(o => o.value);
    if (!options.length) { failures.push("没有可编辑的作业"); return { failures, notes }; }
    const job = options[0].value;
    picker.value = job;
    picker.dispatchEvent(new Event("change"));
    await sleep(900);

    const form = document.getElementById("cfg-form-save");
    if (!form) { failures.push("配置表单的保存按钮（#cfg-form-save）不存在"); return { failures, notes }; }
    notes.formButtonDisabled = form.disabled;
    if (form.disabled) {
      failures.push("打开作业后，配置表单的保存按钮仍然是禁用的 —— 点不动就存不了");
      return { failures, notes };
    }

    // Where the field lives: per-pair when the form renders pairs, else job level.
    const hasPairs = !!document.querySelector("#cfg-pairs .pair-card");
    const prefix = hasPairs ? "p0-sync" : "cfg-sync";
    const maxdel = document.getElementById(prefix + "-maxdel");
    if (!maxdel) { failures.push("找不到删除上限输入框（" + prefix + "-maxdel）"); return { failures, notes }; }
    maxdel.value = "17";
    maxdel.dispatchEvent(new Event("input", { bubbles: true }));

    // Wrap fetch for the duration of one press.
    const realFetch = window.fetch;
    const posted = [];
    const wrap = () => {
      window.fetch = function (url, init) {
        const method = String((init && init.method) || "GET").toUpperCase();
        const u = String(url);
        if (method === "POST" && u.indexOf("/api/config") === 0) posted.push(u);
        return realFetch.apply(this, arguments);
      };
    };
    wrap();
    try {
      form.click();
      for (let i = 0; i < 80; i++) {
        await sleep(100);
        if (!/保存中/.test(form.textContent || "")) break;
      }
      await sleep(300);
    } finally {
      window.fetch = realFetch;
    }

    // 1: exactly one request.
    notes.posts = posted.length;
    if (posted.length === 0) {
      failures.push("点表单的保存没有任何请求 —— 那个按钮没有接上处理");
      return { failures, notes };
    }
    if (posted.length > 1) {
      failures.push("一次点击发出了 " + posted.length + " 个保存请求：" +
        "两个处理挂在同一个按钮上，它们会互相覆盖（屏幕上「已保存」，文件里可能是另一个值）");
    }

    const noteText = (document.getElementById("cfg-save-note").textContent || "").trim();
    notes.note = noteText;
    if (!/已保存|保存失败/.test(noteText)) failures.push("保存之后没有任何结果提示");
    if (/保存失败/.test(noteText)) failures.push("保存失败了：" + noteText);

    // 2: read the value back from the server, not from the form.
    const view = await fetch("/api/config?job=" + encodeURIComponent(job)).then(r => r.json());
    const got = hasPairs ? view.pairs[0].sync : view.sync;
    notes.saved = { max_delete: got.max_delete, set: got.max_delete_set };
    if (!got.max_delete_set || got.max_delete !== 17) {
      failures.push("文件里是 max_delete=" + got.max_delete + " set=" + got.max_delete_set +
        "，期望 17/true");
    }

    // 5: the form shows what was saved, so a second press cannot re-send an old copy.
    await sleep(300);
    const again = document.getElementById(prefix + "-maxdel");
    notes.formAfterSave = again ? again.value : null;
    if (again && again.value !== "17") {
      failures.push("保存后表单显示 " + JSON.stringify(again.value) + "，不是刚存的 17");
    }

    // 3: the form handler must not be attached to the toolbar button as well.
    const toolbar = document.getElementById("cfg-save");
    if (toolbar) {
      const before = posted.length;
      wrap();
      try {
        toolbar.click();
        await sleep(1200);
      } finally {
        window.fetch = realFetch;
      }
      notes.toolbarPosts = posted.length - before;
      if (notes.toolbarPosts > 1) {
        failures.push("工具栏的保存发出了 " + notes.toolbarPosts +
          " 个请求 —— 表单的处理也挂在它上面");
      }
    }

    return { failures, notes };
  };
})();
