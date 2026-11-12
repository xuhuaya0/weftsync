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

  // The drawer head used to carry a <strong> with the current page's name AND a
  // row of pills where the same name was highlighted - the same word twice,
  // 50px apart. The rule that catches it: the head holds navigation, so the
  // selected tab is the only place the current page's name may appear.
  function checkDrawerHead() {
    const out = [];
    const head = document.querySelector(".drawer-head");
    if (!head) return ["没有 .drawer-head"];
    const nav = head.querySelector(".drawer-nav");
    if (!nav) return ["抽屉头部没有 .drawer-nav"];

    const others = [...head.children].filter(el => el !== nav && el.id !== "drawer-close");
    for (const el of others) {
      const t = el.textContent.trim();
      if (t) out.push(`抽屉头部除了页签和关闭按钮还有「${t}」`);
    }

    const tabs = [...nav.querySelectorAll("button[data-drawer]")];
    if (tabs.length < 5) out.push(`抽屉只有 ${tabs.length} 个页签`);
    for (const b of tabs) {
      const id = b.dataset.drawer;
      if (!document.getElementById("tab-" + id)) out.push(`页签「${b.textContent}」没有对应页面 #tab-${id}`);
      if (!b.getAttribute("aria-selected")) out.push(`页签「${b.textContent}」没有 aria-selected`);
    }

    const active = tabs.filter(b => b.getAttribute("aria-selected") === "true");
    if (active.length !== 1) {
      out.push(`选中的页签有 ${active.length} 个，应该刚好 1 个`);
    } else {
      // Reading a computed colour while a transition is in flight returns the
      // interpolated value, not the target. This check first failed against
      // itself 5 times in 6 for exactly that reason: the global `button` rule
      // animates `background` over 120ms, so a read right after a tab switch
      // returned rgba(0,0,0,0) - the start of the fade - and looked like the tab
      // was not sharing the body colour.
      //
      // Watching getAnimations() to detect that does not work: switching away and
      // straight back makes Chrome run the transition *in reverse*, which completes
      // instantly, so the count is 0 while the fade is real. Measured.
      //
      // So ask the style system instead of watching it. Whether a property animates
      // is a static fact in transition-property / transition-duration, and reading it
      // is deterministic. Tab switches should not animate at all: the content is
      // already swapped, so a "you are here" cue that arrives 120ms later is wrong
      // on its own terms.
      const cs = getComputedStyle(active[0]);
      const props = cs.transitionProperty.split(",");
      const durs = cs.transitionDuration.split(",");
      const fading = props.filter((p, i) => {
        const name = p.trim();
        const secs = parseFloat(durs[i % durs.length]);
        return (name === "all" || name === "background" || name === "background-color") && secs > 0;
      });
      if (fading.length) {
        out.push(`页签的 background 带 ${durs[0].trim()} 过渡（transition-property: ${cs.transitionProperty}）—— ` +
                 `内容已经换了，选中底色却要等动画，而且让颜色无法在切换瞬间测量`);
      }

      const tabBg = cs.backgroundColor;
      const bodyBg = getComputedStyle(document.getElementById("drawer")).backgroundColor;
      if (tabBg !== bodyBg) {
        out.push(`选中的页签底色 ${tabBg} 与内容区 ${bodyBg} 不一致，接不成一块`);
      }
      // Same label elsewhere in the head is the duplicate-title defect.
      for (const el of others) {
        if (el.textContent.trim() === active[0].textContent.trim()) {
          out.push(`当前页名「${el.textContent.trim()}」在头部出现两次`);
        }
      }
    }

    // The same defect one level down: a single-card page whose card heading is
    // the tab label word for word. Checked for every page, not just the open one.
    for (const b of tabs) {
      const label = b.textContent.trim();
      const sec = document.getElementById("tab-" + b.dataset.drawer);
      if (!sec) continue;
      for (const h of sec.querySelectorAll(":scope > .card strong")) {
        if (h.textContent.trim() === label) {
          out.push(`页「${label}」的卡标题与页签名完全相同`);
          break;
        }
      }
    }
    return out;
  }

  // Every text-like control on the page must look like the same control.
  //
  // Where this came from: `input[type=password]` was left out of the CSS selector
  // list (text / number / no-type were in it), so every secret field rendered with
  // the browser default - 2px inset border, square corners, 1px 2px padding, 21px
  // tall next to 32px siblings. A one-line selector list, one omitted type, and the
  // only place it shows is the one place a user is most careful.
  //
  // The rule is not "password looks right": it is "single-line controls look like
  // one control, and multi-line controls look like one control". Two groups, not
  // one: a textarea is legitimately a different shape (monospace, taller padding),
  // and collapsing the two would just teach the check to be ignored.
  function checkInputConsistency() {
    const out = [];
    const all = [...document.querySelectorAll("input, select, textarea")]
      .filter(el => !["checkbox", "radio", "hidden", "submit", "button"].includes(el.type || ""))
      .filter(el => el.getClientRects().length > 0 || el.closest("#drawer, .dlg, .browse-dlg"));
    if (all.length < 6) return [`只找到 ${all.length} 个文本类控件，检查无法得出结论`];

    const shape = el => {
      const cs = getComputedStyle(el);
      return [cs.borderTopWidth, cs.borderTopStyle, cs.borderTopLeftRadius,
              cs.paddingTop, cs.paddingLeft, cs.fontSize].join("|");
    };
    const groups = { "单行输入框": [], "多行输入框": [] };
    for (const el of all) {
      (el.tagName === "TEXTAREA" ? groups["多行输入框"] : groups["单行输入框"]).push(el);
    }

    for (const [groupName, els] of Object.entries(groups)) {
      if (els.length < 2) continue;
      const counts = new Map();
      for (const el of els) counts.set(shape(el), (counts.get(shape(el)) || 0) + 1);
      const [major] = [...counts.entries()].sort((a, b) => b[1] - a[1]);
      if (!major) continue;
      const [majorShape, majorCount] = major;
      for (const el of els) {
        if (shape(el) === majorShape) continue;
        const label = (el.closest("label") || {}).textContent ||
                      (el.closest(".cred-field, .field") || {}).textContent ||
                      el.id || el.name || el.tagName;
        const cs = getComputedStyle(el);
        out.push(`${groupName}「${String(label).trim().slice(0, 18)}」（${el.tagName.toLowerCase()}` +
                 `${el.type ? " type=" + el.type : ""}）样式与同类不一致：` +
                 `${cs.borderTopWidth} ${cs.borderTopStyle} / radius ${cs.borderTopLeftRadius}` +
                 ` / padding ${cs.paddingTop} ${cs.paddingLeft} / ${cs.fontSize}` +
                 `（多数：${majorShape}，${majorCount}/${els.length} 个）`);
      }
    }
    return out;
  }

  // Names that were renamed must not come back in prose. D77 renamed the tabs
  // 所有配置 -> 作业列表 and 完整配置 -> 配置表单; the conflicts page was still telling
  // the reader to go to 到「作业」点一次预览 and to set keep-both in （配置页）.
  // A rename is only finished when nothing still points at the old name, and nothing
  // mechanical catches that - so this does.
  //
  // These are exact strings, not patterns: 「作业」 with the corner quotes is always a
  // reference to the old tab, while 作业 as a bare noun is everywhere and legitimate.
  const STALE_NAMES = ["「作业」", "「完整配置」", "「所有配置」", "配置页"];

  function checkStaleNames() {
    const out = [];
    // textContent, not innerText: innerText skips anything not displayed, so a
    // stale reference inside a collapsed hint or an inactive tab is invisible to it.
    // Measured: the first version passed with `到「作业」点一次预览` injected into the
    // conflict hint, precisely because that hint is hidden while the list is empty -
    // which is the state almost every run starts in.
    const text = document.body.textContent;
    for (const name of STALE_NAMES) {
      if (text.includes(name)) {
        // No extra 「」 around `name`: the quoted entries already carry their own,
        // and 「「作业」」 reads like a typo in the report.
        out.push(`界面文字里还有改名前的 ${name}`);
      }
    }
    return out;
  }

  // An explanation of buttons may not appear next to an empty table: there are no
  // rows, so the buttons it names are not on screen. This was the actual defect
  // on the conflicts page - a four-line note about 这里两个按钮 above 暂无冲突.
  function checkConflictPage() {
    const out = [];
    const sec = document.getElementById("tab-conflicts");
    if (!sec) return ["没有 #tab-conflicts"];
    const hint = document.getElementById("conflict-hint");
    const table = document.getElementById("conflict-table");
    const empty = document.getElementById("conflicts-empty");
    if (!hint || !table || !empty) return ["冲突页缺少 hint / table / empty 三者之一"];

    const rows = table.querySelectorAll("tbody tr");
    const shown = el => !!(el.getClientRects().length);
    if (rows.length === 0) {
      if (shown(hint)) out.push("冲突表为空，却还在说明两个按钮");
      if (!shown(empty)) out.push("冲突表为空，却没有空状态说明");
    } else {
      if (!shown(hint)) out.push("冲突表有行，却没有说明两个按钮各做什么");
      if (shown(empty)) out.push("冲突表有行，空状态却还在显示");
      // The backend reports machine tokens and English prose. Both used to reach
      // this table verbatim: a 类型 pill reading "remote-replaced" (wrapping onto
      // two lines) and a 说明 in English, on an otherwise Chinese page.
      for (const tr of rows) {
        const cells = tr.querySelectorAll("td");
        const kind = (cells[2] || {}).textContent || "";
        const detail = (cells[3] || {}).textContent || "";
        if (!/[\u4e00-\u9fff]/.test(kind)) out.push(`类型列没有中文：${kind.trim()}`);
        if (!/[\u4e00-\u9fff]/.test(detail)) out.push(`说明列没有中文：${detail.trim()}`);
      }
    }
    return out;
  }

  window.__checkSettingsCards = async function () {
    // Always switch to settings, not only when the drawer is closed. The drawer
    // being open is not the same as the settings page being loaded: if it is open
    // on another tab, the cards still read 载入中… and every "this card has no
    // controls" assertion fires against a placeholder. Measured: three false
    // failures the one time this ran with the drawer already open on 作业列表.
    openDrawer("settings");
    await waitFor(() => {
      const n = document.getElementById("set-notify");
      const c = document.getElementById("cred-body");
      return n && !n.classList.contains("empty") && c && !c.classList.contains("empty");
    }, 6000);
    await sleep(150);
    const loaded = !document.getElementById("set-notify").classList.contains("empty");
    if (!loaded) return { failures: ["设置页 6 秒内没有加载完，无法检查"], checked: 0 };

    const sec = document.getElementById("tab-settings");
    const cards = [...sec.querySelectorAll(":scope > .card")];
    let failures = [];
    for (const c of cards) failures = failures.concat(checkCard(c));
    if (cards.length < 3) failures.push(`设置页只渲染出 ${cards.length} 张卡`);
    failures = failures.concat(checkDrawerHead());

    // The About window has to be reachable and non-empty: it is where the
    // read-only rows went.
    openAbout();
    await waitFor(() => {
      const b = document.getElementById("about-body");
      return b && !b.classList.contains("empty");
    }, 4000);
    failures = failures.concat(checkAboutDialog());
    dlgCancel();
    failures = failures.concat(checkInputConsistency());
    failures = failures.concat(checkStaleNames());

    // The conflicts page is one tab away and carries its own empty state.
    openDrawer("conflicts");
    await sleep(250);
    failures = failures.concat(checkConflictPage());

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
