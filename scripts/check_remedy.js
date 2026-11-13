// Failure-explanation checker: when a run fails, does the console say what
// happened and what to do about it?
//
// Where this came from - the user, after a real failed run:
//   「是啥问题啊，你得告诉我啊，而且我在UI界面也不能得知这个信息和如何解决的操作」
//
// At the time the page showed one line: 同步失败：feishu: replace "…": new version
// uploaded as token … . That sentence contradicts itself (the prefix says failed,
// the body says uploaded) and never says what to do.
//
// What this checks, in order:
//   1. the block exists, above the result card, and is empty+hidden while there is
//      nothing to report;
//   2. a feishu-duplicate-left failure renders the three things the user asked for:
//      what happened, that the files are still fine, and a runnable command;
//   3. the copy button copies the command it sits next to;
//   4. an unrecognised failure still gives the raw text and a way to look;
//   5. a later success retires the block.
//
// How to run it:
//   1. Start the daemon, open the console in the browser tools.
//   2. addScriptTag({ path: '...\scripts\check_remedy.js' })
//   3. await page.evaluate(() => window.__checkRemedy())
//
// It returns { failures: [...], measured: {...} }.
//
// To prove it can fail: make renderRemedy return early when last_remedy is null
// (the raw fallback disappears), or drop the second command from
// DuplicateLeftError.Remedy(), and re-run.
(function () {
  "use strict";

  var feishuFields = {
    path: "ADC_CapCal_Temp_Cycling/R5_FF104_20250408_113725.xlsx",
    folder: "ADC_CapCal_Temp_Cycling",
    remote: "test",
    new_token: "NeXsb9SJ6oyofyxO1azc89hNnNd",
    old_token: "VOhAbgpvwo0xEDxf76TcwvrGnj9",
  };

  function probeJob(over) {
    return Object.assign({
      name: "__remedy_probe__",
      state: "idle",
      last_run: "2026-11-13T14:27:41+08:00",
      last_ok: false,
      last_error: 'feishu: replace "ADC_CapCal_Temp_Cycling/R5_FF104_20250408_113725.xlsx": ' +
        "new version uploaded as token NeXsb9SJ6oyofyxO1azc89hNnNd, but deleting the " +
        "previous version VOhAbgpvwo0xEDxf76TcwvrGnj9 failed: code=1061001",
      last_remedy: {
        kind: "feishu-duplicate-left",
        fields: feishuFields,
        commands: [
          { label: "inspect", run: 'weftsync backend dedupe "test:ADC_CapCal_Temp_Cycling"' },
          { label: "apply", run: 'weftsync backend dedupe "test:ADC_CapCal_Temp_Cycling" -o confirm=true' },
        ],
      },
    }, over || {});
  }

  // renderFor drives the page's own renderer with a fabricated job, then reads the
  // DOM back. Nothing here re-implements the rendering: if the page did not wire
  // the block up, rendering for a real job would fail the same way.
  //
  // It swaps the global remedySubject, which is safe because both are top-level
  // function declarations in a classic script - i.e. properties of window - so the
  // call inside renderRemedy goes through the swapped binding.
  function renderFor(j) {
    var prev = window.remedySubject;
    window.remedySubject = function () { return j; };
    try {
      window.renderRemedy();
    } finally {
      window.remedySubject = prev;
    }
    var host = document.getElementById("s-remedy");
    return {
      hidden: host.classList.contains("hidden"),
      text: host.innerText || "",
      html: host.innerHTML,
      cmds: Array.prototype.map.call(
        host.querySelectorAll(".rm-step code"), function (el) { return el.textContent; }),
    };
  }

  window.__checkRemedy = function () {
    var failures = [];
    var measured = {};

    var needed = ["renderRemedy", "remedySubject", "latestOutcome", "finishStatus"];
    for (var i = 0; i < needed.length; i++) {
      if (typeof window[needed[i]] !== "function") {
        failures.push("页面里没有 " + needed[i] + "() —— 说明块没有被接进轮询，用户看到的仍是一行英文");
        return { failures: failures, measured: measured };
      }
    }

    var host = document.getElementById("s-remedy");
    if (!host) {
      failures.push("页面里没有 #s-remedy，失败说明无处安放");
      return { failures: failures, measured: measured };
    }
    // The block has to sit above the result card: a failure explained below the
    // table is a failure explained off-screen on a short window.
    var card = document.getElementById("plan-card");
    if (card && !(host.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING)) {
      failures.push("#s-remedy 不在「对比结果」之前，窗口矮时会被挤到看不见");
    }

    // 1. nothing to report -> nothing on screen
    var out = renderFor(null);
    measured.idleHidden = out.hidden;
    measured.idleTextLen = (out.text || "").trim().length;
    if (!out.hidden || measured.idleTextLen) {
      failures.push("没有失败时说明块仍然显示内容");
    }

    // 2. the real case
    out = renderFor(probeJob());
    measured.remedyText = out.text;
    if (out.hidden) failures.push("失败时说明块没有显示出来");
    var wanted = [
      ["发生了什么（旧版本没删掉）", "旧版本"],
      ["文件本身是对的", "内容是对的"],
      ["上游是新增而不是覆盖", "新增条目"],
      ["操作入口", "怎么办"],
      ["新 token", "NeXsb9SJ6oyofyxO1azc89hNnNd"],
      ["旧 token", "VOhAbgpvwo0xEDxf76TcwvrGnj9"],
      ["目录", "ADC_CapCal_Temp_Cycling"],
    ];
    for (var w = 0; w < wanted.length; w++) {
      if (out.text.indexOf(wanted[w][1]) < 0) {
        failures.push("说明块没有讲「" + wanted[w][0] + "」（缺 " + wanted[w][1] + "）");
      }
    }

    // 3. both commands, and the destructive one is second
    measured.commands = out.cmds;
    if (out.cmds.length !== 2) {
      failures.push("应该给两条命令（先看再执行），实际 " + out.cmds.length + " 条");
    } else {
      if (out.cmds[0].indexOf("confirm") >= 0) {
        failures.push("第一条命令就带 -o confirm=true —— 不可撤销的动作不该是第一个选择");
      }
      if (out.cmds[1].indexOf("confirm=true") < 0) {
        failures.push("第二条命令不删除任何东西，用户照抄也没用");
      }
      for (var c = 0; c < out.cmds.length; c++) {
        if (out.cmds[c].indexOf('"test:ADC_CapCal_Temp_Cycling"') < 0) {
          failures.push("命令没有写全 remote:目录，复制出去跑不了：" + out.cmds[c]);
        }
      }
    }

    // 4. the copy button carries the command it sits next to
    var copyBtn = host.querySelector(".rm-copy");
    if (!copyBtn) {
      failures.push("命令旁边没有复制按钮");
    } else if (copyBtn.dataset.cmd !== out.cmds[0]) {
      failures.push("复制按钮复制的不是它旁边那条命令");
    }

    // 5. the original text is still reachable, but folded away
    if (!/错误原文/.test(out.text)) {
      failures.push("原始报错没有保留（它是对账和报 bug 用的证据）");
    }

    // 6. an unrecognised failure must not turn into a dead end
    out = renderFor(probeJob({
      last_remedy: null,
      last_error: "some brand new backend failure",
    }));
    measured.rawFallback = out.text;
    if (out.hidden) {
      failures.push("认不出来的失败被直接隐藏了，用户手里只剩作业表里一个红字");
    } else {
      if (out.text.indexOf("some brand new backend failure") < 0) {
        failures.push("认不出来的失败没有显示原文");
      }
      if (!/日志/.test(out.text)) {
        failures.push("认不出来的失败没有指出下一步去哪儿看");
      }
      if (out.cmds.length) {
        failures.push("认不出来的失败却给出了命令");
      }
    }

    // 7. a later success retires it
    out = renderFor(probeJob({ last_ok: true, last_error: "", last_remedy: null }));
    measured.afterSuccessHidden = out.hidden;
    if (!out.hidden) {
      failures.push("后来同步成功了，说明块还挂着 —— 这会让用户学会无视它");
    }

    // 8. the status bar must not repeat the 300-character English sentence that
    //    the block above has just translated. Two copies of one error, one of them
    //    unreadable, is how a screen ends up saying the same thing twice and
    //    nothing at all.
    window.finishStatus(probeJob(), "run");
    const statusLine = document.getElementById("s-status").innerText || "";
    measured.statusLine = statusLine;
    if (statusLine.indexOf("feishu: replace") >= 0) {
      failures.push("底栏又把整段英文报错抄了一遍（说明块已经解释过了）");
    }
    if (statusLine.indexOf("见上面的说明") < 0) {
      failures.push("底栏没有把人引到说明块");
    }
    // ...but an unexplained failure must still say its own error, or the status
    // bar would go silent about a real failure.
    window.finishStatus({ last_ok: false, last_error: "some brand new backend failure" }, "run");
    const plainLine = document.getElementById("s-status").innerText || "";
    measured.plainStatusLine = plainLine;
    if (plainLine.indexOf("some brand new backend failure") < 0) {
      failures.push("没有说明块的失败，底栏把报错也吞了");
    }

    // Leave the page as we found it.
    window.renderRemedy();
    return { failures: failures, measured: measured };
  };
})();
