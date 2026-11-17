// Pair-delete checker: does deleting a folder pair actually stick?
//
// Where this came from - the user, after deleting a pair and refreshing:
//   「这个页面没有 pair，按减号删除后刷新又回来了 为啥」
//
// It was a real defect and it was mine: the minus button only spliced the
// in-memory copy and called markDirty(), so the row vanished from the screen while
// the job file still had the pair. The screen claimed a change the disk did not
// have, and a refresh rebuilt the page from the file - so the pair came back.
//
// What it asserts:
//   1. clicking minus writes to the file immediately (pairs on disk drop by one);
//   2. after a full page reload the pair is still gone (the bug above);
//   3. the status line says it was written and names the file;
//   4. the unsaved star is cleared, because nothing is pending any more;
//   5. on the LAST remaining pair the minus clears that pair's paths instead of
//      removing the row, leaves the file untouched (an empty pair cannot be saved),
//      marks the page unsaved, and saving afterwards explains in Chinese what is
//      missing rather than surfacing the server's English "local.root is required".
//
//      Point 5 came from a correction by the user:
//        「我不是要删除作业，当改 pair 是最后一个的时候，我是要在 ui 上清掉最后
//          一个 pair 中的内容」
//
// How to run it (on a THROWAWAY job - it really deletes pairs):
//   1. Start a daemon serving a scratch job with 2+ pairs, open its page.
//   2. addScriptTag({ path: '...\scripts\check_pair_delete.js' })
//   3. await page.evaluate(() => window.__checkPairDelete())
//
// It returns { failures: [...], measured: {...} }.
//
// To prove it can fail, put back the old handler (splice + markDirty, no save) and
// re-run: assertion 1 must report that the file kept its pairs.
(async function () {
  "use strict";

  async function diskPairs() {
    const r = await fetch("/api/config?job=" + encodeURIComponent(sessionCfg.name));
    const c = await r.json();
    return c.pairs.map(function (p) { return p.name; });
  }

  function rows() {
    return document.querySelectorAll("#pair-rows .pair-row").length;
  }

  function statusText() {
    return (document.getElementById("s-status") || {}).textContent || "";
  }

  function starShown() {
    return !!document.querySelector("#cfg-list .dirty:not([style*='none'])");
  }

  window.__checkPairDelete = async function () {
    var failures = [];
    var measured = {};

    if (typeof sessionCfg === "undefined" || !sessionCfg) {
      failures.push("页面还没有载入配置（sessionCfg 为空），无法测试");
      return { failures: failures, measured: measured };
    }
    var start = await diskPairs();
    measured.start = start;
    if (start.length < 2) {
      // A data precondition, not a failure: on a one-pair job there is nothing to
      // delete by design. Say so instead of reporting a vague error.
      failures.push("这个作业只有 " + start.length + " 个目录对，至少需要 2 个才能测试删除");
      return { failures: failures, measured: measured };
    }

    // 1. delete the last row
    window.confirm = function () { return true; };
    var before = rows();
    document.querySelectorAll("#pair-rows .p-remove")[before - 1].click();
    // Saving is a round trip through the daemon; give it room.
    for (var i = 0; i < 40 && rows() === before; i++) {
      await new Promise(function (r) { setTimeout(r, 100); });
    }
    await new Promise(function (r) { setTimeout(r, 700); });

    var afterDisk = await diskPairs();
    measured.afterDelete = { rows: rows(), onDisk: afterDisk, status: statusText(), star: starShown() };

    if (afterDisk.length !== start.length - 1) {
      failures.push("点了减号但配置文件里还是 " + afterDisk.length + " 个目录对（" +
                    afterDisk.join(", ") + "）—— 删除没有写入磁盘，刷新就会回来");
    }
    if (rows() !== start.length - 1) {
      failures.push("行数 " + rows() + "，应为 " + (start.length - 1));
    }
    if (!/已写入/.test(measured.afterDelete.status)) {
      failures.push("底栏没有说「已写入」：" + measured.afterDelete.status);
    }
    if (measured.afterDelete.star) {
      failures.push("保存之后还挂着未保存星号");
    }

    // 2. the actual regression: reload and see whether it survives
    await loadSession();
    await new Promise(function (r) { setTimeout(r, 400); });
    var afterReload = await diskPairs();
    measured.afterReload = { rows: rows(), onDisk: afterReload };
    if (afterReload.length !== afterDisk.length) {
      failures.push("重新载入后目录对又变成 " + afterReload.length + " 个 —— 这就是用户报的那个 bug");
    }

    // 3. keep deleting until one is left, then check the refusal
    var guard = 0;
    while ((await diskPairs()).length > 1 && guard++ < 10) {
      var n = rows();
      document.querySelectorAll("#pair-rows .p-remove")[n - 1].click();
      for (var j = 0; j < 40 && rows() === n; j++) {
        await new Promise(function (r) { setTimeout(r, 100); });
      }
      await new Promise(function (r) { setTimeout(r, 700); });
    }

    var oneBefore = {
      rows: rows(),
      onDisk: await diskPairs(),
      status: statusText(),
      localInput: (document.querySelector("#pair-rows .p-local") || {}).value,
    };
    document.querySelectorAll("#pair-rows .p-remove")[rows() - 1].click();
    await new Promise(function (r) { setTimeout(r, 700); });
    var oneAfter = {
      rows: rows(),
      onDisk: await diskPairs(),
      status: statusText(),
      localInput: (document.querySelector("#pair-rows .p-local") || {}).value,
      remoteInput: (document.querySelector("#pair-rows .p-remote") || {}).value,
      star: starShown(),
    };
    measured.lastPair = { before: oneBefore, after: oneAfter };

    // 唯一的一对不能整行删掉 —— 零个目录对的作业会被服务端拒绝
    // （no [[pair]] defined），而且 daemon 会把空提交当作「什么也没发」把旧的对塞回去。
    if (oneAfter.onDisk.length !== 1 || oneAfter.rows !== 1) {
      failures.push("最后一个目录对整行被删掉了（磁盘 " + oneAfter.onDisk.length +
                    " 个）—— 一个作业至少需要一个目录对");
    }
    // 但这个按钮不能是死的：它要清空那一对的内容（2026-11-17 用户澄清：
    // 「我是要在 ui 上清掉最后一个 pair 中的内容」）。
    if (oneBefore.localInput && oneAfter.localInput !== "") {
      failures.push("点了减号，本机目录还是「" + oneAfter.localInput +
                    "」—— 最后一个目录对应该被清空内容");
    }
    if (oneBefore.localInput && oneAfter.remoteInput !== "") {
      failures.push("点了减号，云端目录还是「" + oneAfter.remoteInput + "」");
    }
    if (!/已清空/.test(oneAfter.status)) {
      failures.push("清空之后底栏没有说明：" + oneAfter.status);
    }
    if (!oneAfter.star) {
      failures.push("清空之后没有标成未保存（星号没亮），用户会以为已经写进文件了");
    }

    // 4. 清空之后保存必须给出能看懂的原因。
    //    服务端的原文是 pair "x": local.root is required —— 英文、而且要到点了
    //    保存才出现。清空是本次操作故意造成的「填了一半」，所以要在这里就用中文
    //    说清缺什么。（这也证明了清空确实不会被偷偷写进文件。）
    var saveMsg = "";
    try {
      await saveSession();
      saveMsg = "(保存竟然成功了)";
    } catch (e) {
      saveMsg = e.message;
    }
    measured.saveAfterClear = saveMsg;
    if (/保存竟然成功了/.test(saveMsg)) {
      failures.push("清空之后竟然保存成功 —— 空路径的配置不该落盘");
    } else if (!/还没有填/.test(saveMsg)) {
      failures.push("清空后保存的报错不是中文提示：" + saveMsg);
    }
    var stillOne = await diskPairs();
    if (stillOne.length !== 1) {
      failures.push("清空后磁盘上的目录对数变成了 " + stillOne.length);
    }

    return { failures: failures, measured: measured };
  };
})();
