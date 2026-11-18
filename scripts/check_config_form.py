r"""配置表单按变体联动、删除配置、作业列表宽度、版本号。

用户报的三个问题（2026-11-18）：
  「作业列表这里显示不全啊，而且好像没用能删除配置的地方呢」
  「配置表单的这个部分选不同的为什么下面都不动，下面应该是根据不同的同步方式
    变化的吧？」

三处都改了，这个脚本检查它们没有被改回去。

做法是读 HTML 判断结构。真正的联动效果由浏览器实测确认过（update 没有删除项、
mirror 出现六个删除字段、two-way 出现四个双向开关），这里守的是「别人重写这一段
时不要悄悄退回去」。

用法：python scripts/check_config_form.py
"""

import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
HTML = ROOT / "src" / "go" / "daemon" / "ui" / "index.html"
GO = ROOT / "src" / "go"

fails: list[str] = []


def need(cond: bool, msg: str) -> None:
    if not cond:
        fails.append(msg)


def main() -> int:
    text = HTML.read_text(encoding="utf-8")

    # ---- 1. 删除配置：接口、按钮、对话框、确认
    for frag, msg in [
        ('id="cfg-delete"', "配置面板缺少「删除」按钮"),
        ('id="dlg-delcfg"', "缺少删除配置对话框"),
        ('id="delcfg-file"', "删除对话框缺少「同时删除文件」勾选框"),
        ('id="delcfg-ok"', "删除对话框缺少确认按钮"),
        ('"/api/config/remove?path="', "界面没有调用删除接口"),
    ]:
        need(frag in text, msg)

    # 勾选框默认不勾：删文件不可逆，不能预选。
    need(
        re.search(r'id="delcfg-file"[^>]*>', text) is not None
        and "checked" not in re.search(r'id="delcfg-file"[^>]*>', text).group(0),
        "「同时删除文件」被预勾选了；删除文件不可逆，默认必须为关",
    )

    # 后端默认也不删文件。
    go = (GO / "daemon" / "configs.go").read_text(encoding="utf-8")
    need("func (a *API) handleConfigRemove(" in go,
         "后端缺少 handleConfigRemove")
    need('Get("delete_file") == "true"' in go,
         "后端没有把 delete_file 当成显式开关")
    need("func (d *Daemon) RemoveConfig(" in go,
         "后端缺少 Daemon.RemoveConfig")

    # ---- 2. 配置表单按变体联动
    need("const DELETION_WHY" in text,
         "缺少 DELETION_WHY：删除项隐藏时不给理由，用户会以为功能丢了")
    need(
        re.search(r'if v === "mirror"\s*\n?\s*\?', text) is not None
        or 'v === "mirror"' in text,
        "syncFields 没有按 mirror 判断是否显示删除设置",
    )
    need(
        'variantSel.addEventListener("change"' in text,
        "变体下拉没有 change 处理，选了不会重画",
    )
    # 隐藏字段必须保留原值，否则 mirror -> update -> mirror 会清掉回收目录名。
    need(
        "prev && prev[key]" in text,
        "collectSync 没有回退到原值：被隐藏的字段会被清空",
    )

    # ---- 3. 作业列表不再被整条路径撑开
    need("function baseName(" in text,
         "缺少 baseName：作业列表会用整条路径撑开「配置」列")
    need('class="sub mono jt-file"' in text,
         "作业列表没有用 jt-file 截断文件名")
    need("#tab-jobs td.jt-when" in text,
         "时间列缺少 nowrap 约束，日期会被压成一个字符")
    need("overflow: hidden; text-overflow: ellipsis" in text,
         "缺少 ellipsis：长文件名不会显示省略")

    # ---- 4. 版本号
    ver = (GO / "version" / "version.go").read_text(encoding="utf-8")
    need(re.search(r'const Version = "\d+\.\d+\.\d+"', ver) is not None,
         "version.Version 不是 x.y.z 形式")
    info = (GO / "daemon" / "info.go").read_text(encoding="utf-8")
    need('json:"product_version"' in info,
         "InfoView 没有 product_version 字段")
    need('id="brand-ver"' in text, "顶栏没有版本号元素")
    need("function loadBrandVersion(" in text, "没有取版本的代码")

    # 关于窗口要区分「版本」和「构建」：一个是发布号，一个是提交号。
    need('row("版本"' in text and 'row("构建"' in text,
         "关于窗口没有把版本号与构建号分成两行")

    # ---- 5. 对话框里不许出现「奇怪的东西」
    #
    # 用户划掉了 2026-11-18 那一版里的这句：
    #   会自动补 .toml。已存在的文件会被拒绝 —— 另存为不该悄悄覆盖。
    # 它解释的是实现者的顾虑，不是用户要做的事。同类的一起去掉。
    for gone, why in [
        ("另存为不该悄悄覆盖", "「另存为不该悄悄覆盖」这种解释设计意图的话又回来了"),
        ("而不是隐藏", "「…而不是隐藏」这种解释设计意图的话又回来了"),
        ("说明它压根没传给", "口语化的「压根」又回来了"),
    ]:
        need(gone not in text, why)

    # 保存失败必须是可读的中文，且不带整条路径。
    need("function plainConfigError(" in text,
         "缺少 plainConfigError：服务端的 toml 措辞会被原样显示")
    # 注意：JS 里这行是正则字面量，源码中带反斜杠（remote\\.root），
    # 所以这里要和源码一字不差，否则就是检查工具自己在报假问题。
    for pat in (r'local\\.root is required', r'remote\\.root is required'):
        need(re.search(pat, text) is not None,
             f"plainConfigError 没有处理 {pat}")
    need("plainConfigError(e.message)" in text,
         "有地方还在直接显示 e.message")

    # 另存为要先用 incompletePair 拦一次，而不是等服务器拒绝。
    need(
        re.search(r"saveas-ok\"\)\.addEventListener[\s\S]{0,900}incompletePair\(\)", text)
        is not None,
        "另存为没有在提交前检查目录对是否填全",
    )

    # 后端要把「the edited config is not valid: <路径>: 」这两层剥掉。
    need("func saveErrText(" in text or "func saveErrText(" in go,
         "后端缺少 saveErrText")
    need("the edited config is not valid: " in go,
         "saveErrText 没有识别要剥掉的那层包装")

    if fails:
        for f in fails:
            print("FAIL:", f)
        return 1
    print("OK: 删除配置 / 变体联动 / 作业列表宽度 / 版本号 四处结构完好")
    return 0


if __name__ == "__main__":
    sys.exit(main())
