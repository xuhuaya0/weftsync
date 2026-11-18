r"""打开配置对话框：不是我们的 .toml 不该出现，出现的要给一行能读的理由。

用户报的问题（2026-11-18，截图）：
  「为什么打不开啊，而且写这么奇怪的标注」

截图里那一行是

  pyproject.toml  (打不开: parse C:\Users\Jlsemi-62\OneDrive\_PC_Sy)

两处都错了：
  1. pyproject.toml 根本不是 weftsync 配置，它没有「打不开」，是「不是这个」。
  2. 理由被 slice(0, 40) 从头切，切掉的是路径，真正的理由在末尾。

这个脚本检查界面不再从中间截断，并把跳过的文件数说出来。
后端的行为由 go test 的两个用例守着：
  TestListConfigFilesReportsBrokenFiles
  TestListConfigFilesErrorIsShortAndPathFree

用法：python scripts/check_open_dialog.py
"""

import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
HTML = ROOT / "src" / "go" / "daemon" / "ui" / "index.html"

fails: list[str] = []


def need(cond: bool, msg: str) -> None:
    if not cond:
        fails.append(msg)


def main() -> int:
    text = HTML.read_text(encoding="utf-8")

    # 1. 不再把理由切断。slice(0, N) 作用在错误信息上是从路径开始切，
    #    理由在末尾，等于把要说的话全部丢掉。
    need(
        "f.error.slice(" not in text,
        "index.html 又在截断错误信息了（f.error.slice(...)）；"
        "截断从路径开始，会把真正的理由切掉",
    )

    # 2. 整句显示，不省略。
    need(
        '${esc(f.error)}' in text,
        "错误信息没有整句渲染：找不到 ${esc(f.error)}",
    )

    # 3. 跳过多少个文件要说出来，而不是无声丢弃。
    need(
        'id="open-skip"' in text,
        "打开配置对话框缺少 #open-skip，跳过的文件不会被说明",
    )
    need(
        'id="saveas-skip"' in text,
        "另存为对话框缺少 #saveas-skip，跳过的文件不会被说明",
    )
    need(
        "v.other" in text,
        "界面没有读服务端的 other 字段，跳过数无从显示",
    )

    # 4. 提示语也不再承诺「列出所有 .toml」——现在只列配置。
    need(
        "只列出 weftsync 配置" in text,
        "打开配置对话框的提示语还在说会列出所有 .toml",
    )
    # 5. 并且不再附带解释设计意图的那半句。用户把它和别的说教一起划掉了：
    #    「能不能把这类奇怪的东西删掉」。
    need(
        "而不是隐藏" not in text,
        "提示语又变回「…而不是隐藏」这种解释设计意图的写法了",
    )

    # 6. 服务端必须真的算出了 other。用关键字判断，不依赖具体实现。
    go = (ROOT / "src" / "go" / "daemon" / "configs.go").read_text(encoding="utf-8")
    need(
        "func looksLikeConfig(" in go,
        "configs.go 缺少 looksLikeConfig：非配置的 .toml 会被当坏配置列出来",
    )
    need(
        "func shortLoadError(" in go,
        "configs.go 缺少 shortLoadError：错误信息会带上整条路径",
    )
    need(
        re.search(r'Other\s+int\s+`json:"other"`', go) is not None,
        'configs.go 的响应里没有 other 字段',
    )
    # 严格模式的原文是解码器的自述，不是文件的问题。
    need(
        "fields in the document are missing in the target struct" in go,
        "configs.go 没有识别解码器的 strict mode 原文，用户会看到一句行话",
    )

    if fails:
        for f in fails:
            print("FAIL:", f)
        return 1
    print("OK: 打开配置对话框只列配置，理由整句显示，跳过数已说明")
    return 0


if __name__ == "__main__":
    sys.exit(main())
