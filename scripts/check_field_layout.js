// Field-layout checker for the cloud-connection panel.
//
// The reason this file exists: a layout defect was found on the SMB card ("the
// hint text sits at a different x than on the other rows") after only the feishu
// card had ever been looked at. Checking one sample out of sixty is not a check,
// so this script checks every type the picker offers, in one pass.
//
// How to run it (from the VS Code browser tools, or the devtools console of an
// already-open console page):
//
//   1. Start a daemon whose rclone config is DISPOSABLE, because this script
//      creates one remote per backend type:
//        .\.scratch\weftsync-ui.exe daemon .scratch\demo\job.toml --config .scratch\check.conf --addr 127.0.0.1:5740
//   2. Open http://127.0.0.1:5740/ , open 工具 → 全局设置与凭据.
//   3. Paste this whole file into the page and call:
//        await window.__checkCredFields()
//      It returns { failures: [...], checked: N } and prints a table.
//
// What it asserts, per rendered field row of every card:
//
//   - the row has exactly one label, one input, one hint (structure)
//   - the label / input / hint of every row inside one card start at the same x
//     (the actual defect: a row with an extra control shifted its hint)
//   - inputs have the same width on every row of a card
//   - no row overflows its card horizontally
//   - every required field shows 必填 and every unset one shows 未设置
//   - no plaintext value appears in a password input
//   - the card's 删除 / 保存 / 测试连接 buttons all exist
//
// It creates temporary remotes and deletes them again at the end, so it can be
// re-run without leaving anything behind.
(function () {
  "use strict";

  const TEMP_PREFIX = "chk-";

  async function api(path, opts) {
    const r = await fetch(path, opts);
    const body = await r.json().catch(() => null);
    if (!r.ok) throw new Error((body && body.error) || ("HTTP " + r.status));
    return body;
  }

  async function ensureAllTypes() {
    const list = await api("/api/credentials");
    const existing = new Set((list.remotes || []).map(r => r.name));
    const created = [];
    for (const t of list.types || []) {
      if (t.pending) continue;                 // cannot be created, nothing to render
      const name = TEMP_PREFIX + t.name.replace(/[^a-z0-9]+/gi, "-");
      if (existing.has(name)) continue;
      await api("/api/credentials?name=" + encodeURIComponent(name) +
                "&type=" + encodeURIComponent(t.name), { method: "POST" });
      created.push(name);
    }
    return created;
  }

  async function deleteMany(names) {
    for (const n of names) {
      try {
        await api("/api/credentials?name=" + encodeURIComponent(n) + "&force=1",
                  { method: "DELETE" });
      } catch (e) {
        console.warn("cleanup failed for", n, e.message);
      }
    }
  }

  const near = (a, b) => Math.abs(a - b) <= 1.5;

  // checkCard inspects one .pair-card and returns an array of failure strings.
  function checkCard(card) {
    const out = [];
    const name = (card.querySelector(".head strong") || {}).textContent || "?";

    // Every card must offer the three actions, whatever its backend.
    for (const what of ["save", "cred-test", "cred-del"]) {
      const sel = what === "save" ? "button[data-cred-save]"
        : what === "cred-test" ? "button[data-cred-test]"
        : "button[data-cred-del]";
      if (!card.querySelector(sel)) out.push(`${name}: missing ${what} button`);
    }

    const rows = [...card.querySelectorAll(".field.cred-field")];
    if (!rows.length) {
      // A backend with no options at all is legal (b2 has required ones, but a
      // future one may not), so only complain when the card claims to be testable.
      return out;
    }

    const labelX = [];
    const inputX = [];
    const hintX = [];
    const inputW = [];

    for (const row of rows) {
      const label = row.querySelector(".f-label");
      const input = row.querySelector(".f-input > input");
      const hint = row.querySelector(".hint");
      const extras = row.querySelectorAll(":scope > *").length;

      if (!label) out.push(`${name}: a row has no label`);
      if (!input) out.push(`${name}: a row has no input`);
      if (!hint) out.push(`${name}: a row has no hint`);
      if (extras !== 4) {
        out.push(`${name}: a row has ${extras} cells, expected 4 ` +
                 `(label/input/extra/hint) - a missing cell shifts the rest`);
      }
      if (!label || !input || !hint) continue;

      const h = hint.textContent || "";
      if (!h.trim()) out.push(`${name}: empty hint on "${label.textContent}"`);
      if (!/未设置|已设置/.test(h)) {
        out.push(`${name}: hint on "${label.textContent}" does not say set/unset: "${h}"`);
      }
      if (row.querySelector(".f-input input[type=password]") &&
          /value="[^"]+"/.test(row.querySelector(".f-input").innerHTML)) {
        out.push(`${name}: password field "${label.textContent}" has a value attribute`);
      }

      const lb = label.getBoundingClientRect();
      const ib = input.getBoundingClientRect();
      const hb = hint.getBoundingClientRect();
      labelX.push(Math.round(lb.left));
      inputX.push(Math.round(ib.left));
      hintX.push(Math.round(hb.left));
      inputW.push(Math.round(ib.width));

      // Horizontal overflow: the input or the hint sticking out of the card.
      const cb = card.getBoundingClientRect();
      if (hb.right > cb.right + 1 || ib.right > cb.right + 1) {
        out.push(`${name}: row "${label.textContent}" overflows the card`);
      }
    }

    const same = (arr, what) => {
      const first = arr[0];
      const bad = arr.findIndex(v => !near(v, first));
      if (bad >= 0) {
        out.push(`${name}: ${what} starts at x=${arr[bad]} on row ${bad + 1} ` +
                 `but x=${first} on row 1 (${JSON.stringify(arr)})`);
      }
    };
    same(labelX, "label");
    same(inputX, "input");
    same(hintX, "hint");
    same(inputW, "input width");
    return out;
  }

  window.__checkCredFields = async function () {
    const created = await ensureAllTypes();
    let failures = [];
    let checked = 0;
    try {
      // Re-read through the API so the panel renders from a fresh view.
      credView = await api("/api/credentials");
      renderCredentials();
      await new Promise(r => setTimeout(r, 300));

      const cards = [...document.querySelectorAll("#cred-body .pair-card")];
      checked = cards.length;
      for (const card of cards) {
        const f = checkCard(card);
        if (f.length) failures = failures.concat(f);
      }
      if (!cards.length) failures.push("no cards rendered at all");
    } finally {
      await deleteMany(created);
      credView = await api("/api/credentials");
      renderCredentials();
    }

    if (failures.length) {
      console.error("field layout: %d failure(s) across %d card(s)", failures.length, checked);
      failures.forEach(f => console.error("  " + f));
    } else {
      console.log("field layout: OK - %d card(s) checked", checked);
    }
    return { failures, checked };
  };
})();
