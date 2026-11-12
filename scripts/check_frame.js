// Frame-layout checker: is there consistent space around the window chrome, and
// does the toolbar still line up with the work column?
//
// Where this came from - the user, looking at the top bar:
//   「为了美观，这部分的上下左右是不是都应该留一些空间」
//
// Measured before the fix: left/right 8px, top/bottom 5px, against a 13px frame
// radius and 16px card padding. Two more defects fell out of the same measurement:
//   - .main padding was also 8, and the alignment between 比较 and the work column
//     depends on those two numbers being equal. Changing one alone breaks it.
//   - .dock was defined twice; the second block carried gap: 8px, which split the
//     "one continuous sidebar" into two panels with a gap between them.
//
// How to run it:
//   1. Start the daemon, open http://127.0.0.1:5730/ in the browser tools.
//   2. addScriptTag({ path: '...\scripts\check_frame.js' })
//   3. await page.evaluate(() => window.__checkFrame())
//
// It returns { failures: [...], measured: {...} }.
//
// To prove the check can fail, set .menubar padding back to "5px 8px" and re-run:
// side insets and the 比较/work left edge must both be reported.
(function () {
  "use strict";

  function px(v) { return Math.round(parseFloat(v)); }

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

    // 1. The two paddings are one design decision, not two. The toolbar's left edge
    //    is brandbar width + menubar padding; the work column's is dock width + gap +
    //    main padding. Equal paddings are what make them coincide.
    if (mbPadL !== mainPadL) {
      failures.push(`顶栏左内边距 ${mbPadL}px 与工作区左内边距 ${mainPadL}px 不同 —— ` +
                    `这两处必须是同一个值（--inset），否则「比较」压不到目录对的左边界`);
    }
    if (inset && mbPadL !== px(inset)) {
      failures.push(`顶栏左内边距 ${mbPadL}px 与 --inset（${inset}）不一致`);
    }

    // 2. The alignment invariant itself.
    const cmp = document.getElementById("s-compare-btn");
    const work = document.querySelector(".work");
    let dLeft = null;
    if (cmp && work) {
      dLeft = Math.round(cmp.getBoundingClientRect().x - work.getBoundingClientRect().x);
      if (dLeft !== 0) {
        failures.push(`「比较」左边缘与工作区左边缘差 ${dLeft}px（应为 0）`);
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
        if (x < 10) {
          failures.push(`顶栏内容离窗框只有 ${x}px，窗框圆角 13px —— 看起来被挤进角里`);
          break;
        }
      }
      if (oneRow) {
        const last = Math.round(appR.right - Math.max(...boxes.map(b => b.right)));
        sideGap = { left: first, right: last };
        if (Math.abs(first - last) > 1) {
          failures.push(`顶栏左右留白不对称：左 ${first}px、右 ${last}px`);
        }
      } else {
        // Wrapped bar: every row starts at the left inset and the rest of each row
        // is legitimately empty, so there is no right inset to compare.
        sideGap = { left: first, right: null, wrapped: true };
        if (first < 10) {
          failures.push(`顶栏换行后内容离窗框只有 ${first}px`);
        }
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
      if (above < 8) {
        failures.push(`顶栏上留白只有 ${above}px，按钮几乎贴着窗框`);
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
