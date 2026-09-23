#!/usr/bin/env python3
"""在发布准备阶段显式同步 Skill 的 metadata.version。

CLI、connector 和 Skill 使用同一发布版本。先准备 connector/npm/lock 元数据、
Changelog 和 Skill，再用 make release-check 校验；make release/release-build
不再自动 bump、调用本脚本写版本或提交源码。

metadata.version 是包内声明；安装态 .version 标记由自更新器维护。
本脚本只处理 SKILL.md，两空格缩进的 metadata.version 行保持原格式。

用法:
    python3 scripts/sync-skill-version.py 1.4.2          # 准备时显式写入
    python3 scripts/sync-skill-version.py 1.4.2 --check  # 只校验，不一致时非零退出
"""

import argparse
import pathlib
import re
import sys

SKILL = pathlib.Path(__file__).resolve().parent.parent / ".agents" / "skills" / "pcbpilot" / "SKILL.md"
# 只匹配 frontmatter 里 metadata 块下的 version 行(两空格缩进),不会误伤正文。
PATTERN = re.compile(r'(?m)^(  version:\s*)"([^"]*)"$')


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("version", help="版本号,不带 v 前缀(如 1.0.3);带了也会被剥掉")
    ap.add_argument("--check", action="store_true", help="只校验是否已同步,不写入")
    args = ap.parse_args()

    want = args.version.lstrip("v")
    if not SKILL.is_file():
        print(f"error: {SKILL} 不存在", file=sys.stderr)
        return 1

    text = SKILL.read_text(encoding="utf-8")
    match = PATTERN.search(text)
    if not match:
        print(f"error: {SKILL} 的 frontmatter 里找不到 metadata.version —— "
              "是不是 frontmatter 被改过?", file=sys.stderr)
        return 1

    have = match.group(2)
    if have == want:
        print(f"  skill version 已是 {want}")
        return 0

    if args.check:
        print(f"error: skill version 是 {have},期望 {want} —— 跑 "
              f"`python3 scripts/sync-skill-version.py {want}` 同步", file=sys.stderr)
        return 1

    SKILL.write_text(PATTERN.sub(lambda m: f'{m.group(1)}"{want}"', text, count=1), encoding="utf-8")
    print(f"  skill version {have} → {want}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
