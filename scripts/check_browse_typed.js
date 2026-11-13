// Typed-path checker for the folder picker (and the two config-file dialogs).
//
// Where this came from - the user, in the folder picker:
//   「我在浏览路径的时候没办法输入地址，只能点，这很不方便」
//
// The server was never the limit: /api/browse accepts any path (relative becomes
// absolute, a file path lists its directory, "test:dir" goes through the backend).
// The whole restriction was `readonly` on one <input>.
//
// Making it editable creates three new failure modes, and they are what this file
// checks - not "the box accepts keystrokes":
//
//   1. Enter does nothing (no handler wired), so typing is still just decoration.
//   2. A path that fails to open becomes the "current" folder, so 选这个文件夹
//      silently saves a folder that does not exist. Measured shape of that bug: the
//      box showed the typo, the pair got the typo, and nothing said so.
//   3. A path typed but not Entered is ignored by 选这个文件夹, which then saves the
//      folder browsed *before* typing - the box and the saved value disagree.
//
// How to run it:
//   1. Start the daemon, open http://127.0.0.1:5730/ in the browser tools.
//   2. addScriptTag({ path: '...\scripts\check_browse_typed.js' })
//   3. await page.evaluate(() => window.__checkBrowseTyped())
//
// Every assertion is restored afterwards: the check edits a real pair's local_root
// and must not leave the session dirty with a test path in it.
(function () {
  "use strict";

  const sleep = ms => new Promise(r => setTimeout(r, ms));

  window.__checkBrowseTyped = async function () {
    const failures = [];
    const notes = {};
    const input = () => document.getElementById("browse-current");
    const enter = el => el.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));

    if (!sessionCfg || !(sessionCfg.pairs || []).length) {
      return { failures: ["没有可用的目录对，无法检查"], notes };
    }
    const idx = 0;
    const savedRoot = sessionCfg.pairs[idx].local_root;
    const savedDirty = dirty;

    try {
      // ---- 1. the box is editable and has a visible "go" affordance
      openBrowse(idx, "local", "");
      await sleep(700);
      notes.entryPath = input().value;
      if (input().readOnly) failures.push("路径框仍是 readonly，只能点不能输入");
      if (!document.getElementById("browse-go")) failures.push("没有「转到」按钮，回车是唯一的入口且不可见");

      // ---- 2. typing an existing folder + Enter navigates
      const good = "C:\\Windows\\System32";
      input().value = good;
      enter(input());
      await sleep(1000);
      notes.afterEnter = { path: input().value, browsePath, dirs: document.querySelectorAll("#browse-list button[data-dir]").length };
      if (browsePath !== good) failures.push(`回车后没有导航：browsePath=${browsePath}，期望 ${good}`);
      if (document.getElementById("browse-err").textContent) {
        failures.push(`有效路径却报了错：${document.getElementById("browse-err").textContent}`);
      }

      // ---- 3. a bad path: reports the reason, does not become "current",
      //         and is not described as an empty folder
      const lastGood = browsePath;
      const bad = "C:\\__weftsync_no_such_dir__";
      input().value = bad;
      enter(input());
      await sleep(1000);
      notes.afterBad = { input: input().value, browsePath, err: document.getElementById("browse-err").textContent };
      if (!document.getElementById("browse-err").textContent) {
        failures.push("不存在的路径没有报错");
      }
      if (document.getElementById("browse-list").innerText.includes("没有子目录")) {
        failures.push("不存在的路径被说成「没有子目录」—— 与真错误同时显示，读起来像空目录");
      }
      if (browsePath !== lastGood) {
        failures.push(`打不开的路径被当成了当前目录：browsePath=${browsePath}（应为 ${lastGood}）`);
      }

      // ---- 4. Escape abandons the edit without losing the position
      enter(input()) && 0;                       // (no-op, keeps linters quiet)
      input().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
      notes.afterEscape = input().value;
      if (input().value !== lastGood) {
        failures.push(`Esc 没有还原到最后一次成功打开的目录：${input().value}（应为 ${lastGood}）`);
      }

      // ---- 5. 选这个文件夹 must not save a path that does not open
      input().value = bad;
      document.getElementById("browse-pick").click();
      await sleep(1000);
      notes.pickBad = { open: !document.getElementById("browse-dlg").classList.contains("hidden"), root: sessionCfg.pairs[idx].local_root };
      if (document.getElementById("browse-dlg").classList.contains("hidden")) {
        failures.push("打不开的路径被「选这个文件夹」接受了（对话框关掉了）");
      }
      if (sessionCfg.pairs[idx].local_root !== savedRoot) {
        failures.push(`打不开的路径写进了目录对：${sessionCfg.pairs[idx].local_root}`);
      }

      // ---- 6. a good path typed but not Entered IS what gets saved
      const typed = "C:\\Windows";
      input().value = typed;
      document.getElementById("browse-pick").click();
      await sleep(1200);
      notes.pickTyped = {
        closed: document.getElementById("browse-dlg").classList.contains("hidden"),
        root: sessionCfg.pairs[idx].local_root,
      };
      if (!document.getElementById("browse-dlg").classList.contains("hidden")) {
        failures.push("输入有效路径后点「选这个文件夹」没有生效（对话框没关）");
      }
      if (sessionCfg.pairs[idx].local_root !== typed) {
        failures.push(`输入了 ${typed} 但保存的是 ${sessionCfg.pairs[idx].local_root} —— 框里写一个、存另一个`);
      }

      // ---- 7. a typed cloud path switches the picker to the cloud side
      openBrowse(idx, "remote", "");
      await sleep(700);
      input().value = "test:";
      enter(input());
      await sleep(1600);
      notes.cloud = { kind: document.getElementById("browse-kind").textContent, err: document.getElementById("browse-err").textContent.slice(0, 60) };
      if (document.getElementById("browse-kind").textContent !== "云端") {
        failures.push(`输入 test: 后仍是「${document.getElementById("browse-kind").textContent}」，没有切到云端`);
      }
      if (document.getElementById("browse-err").textContent) {
        failures.push(`输入 test: 报了错：${notes.cloud.err}`);
      }
      closeBrowse();

      // ---- 8. the two config-file dialogs share the same affordance
      openConfigDlg();
      await sleep(900);
      const od = document.getElementById("open-dir");
      if (!od) failures.push("打开配置对话框里没有 #open-dir");
      else {
        if (od.readOnly) failures.push("「打开配置」的目录框仍是 readonly");
        if (!document.getElementById("open-go")) failures.push("「打开配置」没有「转到」");
      }
      dlgCancel();

      dlgOpen("dlg-saveas");
      await sleep(300);
      loadConfigFiles("saveas");
      await sleep(900);
      const sd = document.getElementById("saveas-dir");
      if (!sd) failures.push("另存为对话框里没有 #saveas-dir");
      else {
        if (sd.readOnly) failures.push("「另存为」的目录框仍是 readonly");
        if (!document.getElementById("saveas-go")) failures.push("「另存为」没有「转到」");
      }
      dlgCancel();
    } finally {
      // Put the pair back. A check that leaves a test path in the editor is worse
      // than no check: the next 保存 would write C:\Windows into a real job file.
      closeBrowse();
      if (sessionCfg && sessionCfg.pairs && sessionCfg.pairs[idx]) {
        sessionCfg.pairs[idx].local_root = savedRoot;
      }
      dirty = savedDirty;
      renderPairs();
    }

    if (failures.length) {
      console.error("browse typed path: %d failure(s)", failures.length);
      failures.forEach(f => console.error("  " + f));
    } else {
      console.log("browse typed path: OK - enter, pick, cloud switch and both config dialogs");
    }
    return { failures, notes };
  };
})();
