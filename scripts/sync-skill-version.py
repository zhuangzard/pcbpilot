#!/usr/bin/env python3
"""在发布准备阶段显式同步全部公开 Skill 的 metadata.version。

CLI、connector 和 Skill 使用同一发布版本；`.agents/skills/public-skills.json`
列出的每个公开 Skill 共用这一个版本。先准备 connector/npm/lock 元数据、
Changelog 和 Skill，再用 make release-check 校验；make release/release-build
不再自动 bump、调用本脚本写版本或提交源码。

metadata.version 是包内声明；安装态 .version 标记由自更新器维护。
本脚本只处理各 SKILL.md，两空格缩进的 metadata.version 行保持原格式。
先校验全部 SKILL.md 再写入，任一缺版本行时不改任何文件。

用法:
    python3 scripts/sync-skill-version.py 1.4.2          # 准备时显式写入
    python3 scripts/sync-skill-version.py 1.4.2 --check  # 只校验，不一致时非零退出
"""

import argparse
import pathlib
import re
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import skill_manifest  # noqa: E402

REPO = pathlib.Path(__file__).resolve().parent.parent
# 只匹配 frontmatter 里 metadata 块下的 version 行(两空格缩进),不会误伤正文。
# 按字节读写，Windows 上也不改行尾。
PATTERN = re.compile(r'(?m)^(  version:\s*)"([^"]*)"(?=\r?$)')


def skill_files(repo: pathlib.Path) -> list[pathlib.Path]:
    manifest = skill_manifest.load(repo)
    return [repo / skill_manifest.SKILLS_ROOT / name / "SKILL.md" for name in manifest.skills]


def sync(repo: pathlib.Path, want: str, check: bool) -> int:
    try:
        files = skill_files(repo)
    except (OSError, ValueError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    plan = []
    for path in files:
        if not path.is_file():
            print(f"error: {path} 不存在", file=sys.stderr)
            return 1
        text = path.read_bytes().decode("utf-8")
        match = PATTERN.search(text)
        if not match:
            print(f"error: {path} 的 frontmatter 里找不到 metadata.version —— "
                  "是不是 frontmatter 被改过?", file=sys.stderr)
            return 1
        plan.append((path, text, match.group(2)))

    stale = [(path, have) for path, _, have in plan if have != want]
    if not stale:
        print(f"  skill version 已是 {want}（{len(plan)} 个 Skill）")
        return 0
    if check:
        for path, have in stale:
            print(f"error: {path.parent.name} skill version 是 {have},期望 {want} —— 跑 "
                  f"`python3 scripts/sync-skill-version.py {want}` 同步", file=sys.stderr)
        return 1
    for path, text, have in plan:
        if have == want:
            continue
        path.write_bytes(PATTERN.sub(lambda m: f'{m.group(1)}"{want}"', text, count=1).encode("utf-8"))
        print(f"  {path.parent.name} skill version {have} → {want}")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("version", help="版本号,不带 v 前缀(如 1.0.3);带了也会被剥掉")
    ap.add_argument("--check", action="store_true", help="只校验是否已同步,不写入")
    ap.add_argument("--repo", type=pathlib.Path, default=REPO, help=argparse.SUPPRESS)
    args = ap.parse_args()
    return sync(args.repo, args.version.lstrip("v"), args.check)


if __name__ == "__main__":
    sys.exit(main())
