// Frame-layout checker: is there consistent space around the window chrome, and
// does the top bar line up with the two columns below it?
//
// Where this came from - the user, looking at the top bar:
//   「为了美观，这部分的上下左右是不是都应该留一些空间」
// and later, from an annotated screenshot (2026-11-16):
//   「左边我画的两个框应该居中对齐（动上面的字），中间我画的两条线要左对齐
//     （动上面的按钮），右侧我画的两条线要右对齐（动上面的文字）。
//     上面一排要协调着动，配合着动」
//
// Measured before the first fix: left/right 8px, top/bottom 5px, against a 13px frame
// radius and 16px card padding. Two more defects fell out of the same measurement:
//   - .main padding was also 8, and the alignment between 比较 and the work column
//     depends on those two numbers being equal. Changing one alone breaks it.
//   - .dock was defined twice; the second block carried gap: 8px, which split the
//     "one continuous sidebar" into two panels with a gap between them.
//
// What it asserts now:
//   1. menubar left padding == .main left padding == --inset;
//   2. the top bar's three boundaries coincide with the two columns below it
//      (brand box == dock box, toolbar left == work left, 已连接 right == work right,
//      including when .main has grown a scrollbar);
//   3. one row at 1000px and wider;
//   4. left/right insets symmetric, measured against the work column's right edge;
//   5. vertical padding above == below, and not cramped;
//   6. the dock is one continuous surface (1px separators, no gap);
//   7. no horizontal overflow.
//
// How to run it:
//   1. Start the daemon, open http://127.0.0.1:5730/ in the browser tools.
//   2. addScriptTag({ path: '...\scripts\check_frame.js' })
//   3. await page.evaluate(() => window.__checkFrame())
//
// It returns { failures: [...], measured: {...} }.
//
// To prove the check can fail, put the brand bar back to content width
// (`.brandbar { width: auto }`) and re-run: the brand/dock and toolbar/work
// assertions must both be reported.
(function () {
  "use strict";

  function px(v) { return Math.round(parseFloat(v)); }

  // Minimum insets, as decided in D80. The first fix here was 8px -> 12px, which
  // was correct arithmetically and invisible on screen (4px); the user's reply was
  // 「所以你修了啥我没看见」. These floors make a return to "technically spaced, looks
  // cramped" a failure instead of a judgement call.
  const MIN_SIDE = 12;
  const MIN_VERT = 10;

  window.__checkFrame = function () {
    const failures = [];
    const app = document.querySelector(".app");
    const mb = document.querySelector(".menubar");
    const main = document.querySelector(".main");
    const dock = document.querySelector(".dock");
    if (!app || !mb || !main || !dock) return { failures: ["缺少 .app / .menubar / .main / .dock"], measured: {} };

    const appR = app.getBoundingClientRect();
    const mbStyle = getComputedStyle(mb);
    const mainStyle = getComputedStyle(main);
    const inset = getComputedStyle(document.documentElement).getPropertyValue("--inset").trim();

    const mbPadL = px(mbStyle.paddingLeft), mbPadT = px(mbStyle.paddingTop);
    const mainPadL = px(mainStyle.paddingLeft);

    // 1. The two paddings are one design decision, not two: the frame inset on the
    //    bar and on the work area must be the same value, or the left edge of the
    //    bar and the left edge of the content disagree by a few pixels for no
    //    stated reason.
    if (mbPadL !== mainPadL) {
      failures.push(`顶栏左内边距 ${mbPadL}px 与工作区左内边距 ${mainPadL}px 不同 —— ` +
                    `这两处必须是同一个值（--inset）`);
    }
    if (inset && mbPadL !== px(inset)) {
      failures.push(`顶栏左内边距 ${mbPadL}px 与 --inset（${inset}）不一致`);
    }

    // 2. 顶栏是下面两列的表头：三条边界必须落在列边界上（D93）。
    //
    //    2026-11-16 用户在原图上圈了三处并写明「左边两个框居中、中间两条线左对齐、
    //    右边两条线右对齐、上面一排要协调着动」：
    //      - 品牌那段的框 == 侧栏的框（两边都重合，中心线因此一致）
    //      - 工具栏左缘 == 工作区左缘
    //      - 已连接右缘 == 工作区右缘（含横向滚动条出现时）
    //
    //    这取代了 D82 的「四段间隙等宽」。两者不能同时成立：间隙 = inset 要求
    //    比较 = 菜单右缘 + 17 = 233，而列对齐要求 265。用户 11-13 要前者、11-16
    //    要后者，于是后者作数，间隙改成「外两条 = inset、内两条 = 列间距(8)」。
    //    间隙仍然测出来放在 measured 里，但它不再是断言 —— 由列推导出来的数字
    //    本来就是两个来源。
    const cmp = document.getElementById("s-compare-btn");
    const toolbar = document.querySelector(".toolbar");
    const work = document.querySelector(".work");
    const nav = mb.querySelector("nav");
    const nowEl = document.getElementById("now");
    const connEl = document.getElementById("conn");
    const stopEl = document.getElementById("s-stop-btn");
    const brandEl = mb.querySelector(".brandbar");

    // 只在真正并排的布局里断言：窄屏时 .main 堆叠，下面没有列可对齐。
    const sideBySide = window.innerWidth > 700 && work;
    if (sideBySide && brandEl && toolbar && connEl) {
      const R = x => Math.round(x);
      const b = brandEl.getBoundingClientRect();
      const d = dock.getBoundingClientRect();
      const t = toolbar.getBoundingClientRect();
      const wk = work.getBoundingClientRect();
      const c = connEl.getBoundingClientRect();

      if (R(b.left) !== R(d.left) || R(b.right) !== R(d.right)) {
        failures.push(`顶栏品牌段 [${R(b.left)},${R(b.right)}] 与侧栏 [${R(d.left)},${R(d.right)}] 不重合 —— ` +
                      `两段必须是同一个矩形，否则两个框看起来一宽一窄`);
      }
      if (R(t.left) !== R(wk.left)) {
        failures.push(`工具栏左缘 ${R(t.left)}px 与工作区左缘 ${R(wk.left)}px 差 ${R(t.left - wk.left)}px`);
      }
      if (R(c.right) !== R(wk.right)) {
        failures.push(`「已连接」右缘 ${R(c.right)}px 与工作区右缘 ${R(wk.right)}px 差 ` +
                      `${R(c.right - wk.right)}px（含滚动条情形；--main-gutter 没跟上？）`);
      }
    }

    let barGaps = null;
    if (cmp && nav && connEl && inset) {
      const g = {
        "左边框→菜单": Math.round(parseFloat(getComputedStyle(mb).paddingLeft) + 1),
        "菜单→比较": Math.round(cmp.getBoundingClientRect().x - nav.getBoundingClientRect().right),
        "停止→时钟": nowEl && nowEl.getClientRects().length && stopEl
          ? Math.round(nowEl.getBoundingClientRect().x - stopEl.getBoundingClientRect().right) : null,
        "连接→右边框": Math.round(appR.right - connEl.getBoundingClientRect().right),
      };
      barGaps = g;
    }
    let dLeft = null;
    if (cmp && work) {
      dLeft = Math.round(cmp.getBoundingClientRect().x - work.getBoundingClientRect().x);
    }

    // 3. Single row at 1000px and wider. The internal toolbar gaps became
    //    var(--inset) in D82, which pushed the required minimum width up: with the
    //    old 176px button minimum the bar wrapped at 1006px (measured 65px -> 121px).
    //    Fixing the minimum to 160px restored one row there; this guards it.
    const kidsEarly = [...mb.children].filter(el => el.getClientRects().length);
    if (window.innerWidth >= 1000 && kidsEarly.length) {
      const bs = kidsEarly.map(el => el.getBoundingClientRect());
      const oneRow = Math.max(...bs.map(b => b.top)) < Math.min(...bs.map(b => b.bottom));
      if (!oneRow) {
        failures.push(`视口 ${window.innerWidth}px 时顶栏换成了多行（实测 ${Math.round(mb.getBoundingClientRect().height)}px 高）`);
      }
    }

    // 3. Side insets should match: content inset left == content inset right.
    //    Read from the menubar's visible children rather than from the padding,
    //    because the padding alone would pass a bar whose right-hand cluster
    //    floated somewhere in the middle.
    //
    //    Only meaningful when everything fits on one row. Once the bar wraps
    //    (toolbar gets flex-basis:100% below 700px), each row starts at the left
    //    inset and the right side of the row is legitimately empty - measuring
    //    "right inset" there reports the empty space as an asymmetry. Measured:
    //    a 695px viewport produced 左 13px、右 632px against a correct layout.
    const kids = [...mb.children].filter(el => el.getClientRects().length);
    let sideGap = null;
    if (kids.length) {
      const boxes = kids.map(el => el.getBoundingClientRect());
      // "One row" is about vertical overlap, not equal tops: the children are
      // different heights (40px toolbar, 29px brand, 17px clock), so grouping by
      // top coordinate reported four rows for a single-row bar and silently
      // skipped the symmetry assertion. Measured: it reported rows: 4 at 1163px.
      const oneRow = Math.max(...boxes.map(b => b.top)) < Math.min(...boxes.map(b => b.bottom));
      const first = Math.round(Math.min(...boxes.map(b => b.x)) - appR.x);
      for (const b of boxes) {
        const x = Math.round(b.x - appR.x);
        if (x < MIN_SIDE) {
          failures.push(`顶栏内容离窗框只有 ${x}px（下限 ${MIN_SIDE}），窗框圆角 13px —— 看着像被挤进角里`);
          break;
        }
      }
      if (oneRow) {
        // 两边都量到窗框，且右边扣掉工作区滚动条的宽度。
        //
        // 不扣会误报：`.main` 出现滚动条时顶栏靠 --main-gutter 让出同样的宽度，
        // 那是正确行为（顶栏右缘因此落在工作区右缘上），不是不对称。
        // 也不能拿工作区右缘当基准 —— 左边量的是窗框，两个基准不同就没有意义
        // （实测那样会报「左 17、右 0」）。
        const mainEl = document.querySelector(".main");
        const gutter = mainEl ? Math.max(0, mainEl.offsetWidth - mainEl.clientWidth) : 0;
        const last = Math.round(appR.right - Math.max(...boxes.map(b => b.right)) - gutter);
        sideGap = { left: first, right: last, gutter: gutter };
        if (Math.abs(first - last) > 1) {
          failures.push(`顶栏左右留白不对称：左 ${first}px、右 ${last}px（右已扣除滚动条 ${gutter}px）`);
        }
      } else {
        // Wrapped bar: every row starts at the left inset and the rest of each row
        // is legitimately empty, so there is no right inset to compare.
        sideGap = { left: first, right: null, wrapped: true };
      }
    }

    // 4. Vertical: the tallest child (the 40px toolbar buttons) has to sit centred,
    //    so the gap above and below match.
    let vGap = null;
    if (kids.length) {
      const tops = kids.map(el => el.getBoundingClientRect().top);
      const bottoms = kids.map(el => el.getBoundingClientRect().bottom);
      const above = Math.round(Math.min(...tops) - appR.y);
      const below = Math.round(mb.getBoundingClientRect().bottom - Math.max(...bottoms));
      vGap = { above, below };
      if (Math.abs(above - below) > 1) {
        failures.push(`顶栏上留白 ${above}px、下留白 ${below}px 不对称`);
      }
      if (above < MIN_VERT) {
        failures.push(`顶栏上留白只有 ${above}px（下限 ${MIN_VERT}），按钮几乎贴着窗框`);
      }
    }

    // 5. The dock is one continuous surface with 1px separators. A gap between the
    //    panels means a duplicate .dock rule is winning.
    const dockGap = getComputedStyle(dock).gap;
    const panels = [...dock.querySelectorAll(".panel")];
    let panelGap = null;
    if (panels.length > 1) {
      panelGap = Math.round(panels[1].getBoundingClientRect().y - panels[0].getBoundingClientRect().bottom);
      if (panelGap > 1) {
        failures.push(`侧栏两块面板之间空出 ${panelGap}px（gap: ${dockGap}）—— 侧栏应是一整块，只用 1px 分隔线`);
      }
    }

    // 6. Nothing may stick out horizontally.
    if (document.documentElement.scrollWidth > window.innerWidth) {
      failures.push(`页面出现横向溢出：${document.documentElement.scrollWidth} > ${window.innerWidth}`);
    }

    const measured = {
      viewport: window.innerWidth,
      insetVar: inset,
      menubarPad: `${mbPadT} ${mbPadL}`,
      mainPadLeft: mainPadL,
      barGaps,
      compareVsWork: dLeft,
      sideGap, vGap, panelGap, menubarHeight: Math.round(mb.getBoundingClientRect().height),
    };

    if (failures.length) {
      console.error("frame: %d failure(s)", failures.length);
      failures.forEach(f => console.error("  " + f));
    } else {
      console.log("frame: OK - inset %spx, compare/work aligned, sidebar continuous", mbPadL);
    }
    return { failures, measured };
  };
})();
