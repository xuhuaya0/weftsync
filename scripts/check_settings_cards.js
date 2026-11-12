// Settings-page checker: is every card on this page something you can change?
//
// Where this came from - the user, looking at 全局设置与凭据:
//   「运行信息 / 通知 / 过滤规则 / 开机自启 这几项应该在这里么？是否有没用的东西」
//
// Two of the cards were decoration:
//   - 运行信息: 530px of read-only rows plus a 刷新 button. A refresh button
//     re-reads; it does not change anything, so the card held no setting at all.
//     帮助 → 关于 weftsync, meanwhile, opened this page and showed nothing.
//   - 通知: the whole card was prose saying "this cannot be switched on here".
//
// The rule that catches both is about state-changing controls, not about looks:
//
//   a card on this page must contain at least one input / textarea / select /
//   details. A button does not count.
//
// And: if every control a card has is disabled, the card has to say why.
//
// How to run it (from the VS Code browser tools):
//
//   1. Start the daemon and open http://127.0.0.1:5730/
//   2. addScriptTag({ path: '...\scripts\check_settings_cards.js' }) on the page
//   3. await page.evaluate(() => window.__checkSettingsCards())
//
// It returns { failures: [...], checked: N }.
//
// To prove the check can fail, put a card back that only has a button in it
// (that was 运行信息) and re-run: it must report one failure naming that card.
(function () {
  "use strict";

  function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

  async function waitFor(fn, ms) {
    const end = Date.now() + (ms || 3000);
    while (Date.now() < end) {
      try { if (fn()) return true; } catch (e) { /* not ready yet */ }
      await sleep(50);
    }
    return false;
  }

  function cardTitle(card) {
    const el = card.querySelector(":scope > .row strong") || card.querySelector("strong");
    return el ? el.textContent.trim() : (card.id || "(无标题)");
  }

  function checkCard(card) {
    const out = [];
    const name = cardTitle(card);
    const controls = [...card.querySelectorAll("input, textarea, select, details")];
    if (controls.length === 0) {
      out.push(`${name}: 这张卡里没有任何可改的东西（只有按钮和文字）`);
      return out;
    }
    const live = controls.filter(el => el.tagName === "DETAILS" || !el.disabled);
    if (live.length === 0) {
      const why = card.querySelector(".hint, .note");
      if (!why) out.push(`${name}: 所有控件都不可用，却没有一句话说明为什么`);
      else if (why.textContent.trim().length < 10) out.push(`${name}: 不可用的理由不到 10 个字，看不出来源`);
    }
    return out;
  }

  function checkAboutDialog() {
    const out = [];
    const body = document.getElementById("about-body");
    if (!body) return ["没有 #about-body：关于窗口不存在"];
    const rows = body.querySelectorAll(".about-grid > span");
    if (rows.length < 8) out.push(`关于窗口只有 ${rows.length} 个单元格，至少该有 4 条信息`);
    const text = body.textContent;
    for (const must of ["版本", "监听地址", "已运行", "rclone 配置"]) {
      if (!text.includes(must)) out.push(`关于窗口缺少「${must}」`);
    }
    if (!/加载的作业文件（\d+）/.test(text)) out.push("关于窗口没有列出加载的作业文件");
    return out;
  }

  window.__checkSettingsCards = async function () {
    const drawer = document.getElementById("drawer");
    if (!drawer.classList.contains("open")) openDrawer("settings");
    await waitFor(() => {
      const n = document.getElementById("set-notify");
      const c = document.getElementById("cred-body");
      return n && !n.classList.contains("empty") && c && !c.classList.contains("empty");
    }, 4000);
    await sleep(150);

    const sec = document.getElementById("tab-settings");
    const cards = [...sec.querySelectorAll(":scope > .card")];
    let failures = [];
    for (const c of cards) failures = failures.concat(checkCard(c));
    if (cards.length < 3) failures.push(`设置页只渲染出 ${cards.length} 张卡`);

    // The About window has to be reachable and non-empty: it is where the
    // read-only rows went.
    openAbout();
    await waitFor(() => {
      const b = document.getElementById("about-body");
      return b && !b.classList.contains("empty");
    }, 4000);
    failures = failures.concat(checkAboutDialog());
    dlgCancel();

    const checked = cards.length;
    if (failures.length) {
      console.error("settings cards: %d failure(s) across %d card(s)", failures.length, checked);
      failures.forEach(f => console.error("  " + f));
    } else {
      console.log("settings cards: OK - %d card(s), about window filled", checked);
    }
    return { failures, checked };
  };
})();
