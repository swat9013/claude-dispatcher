#!/usr/bin/env python3
# vendor 元: swat9013/swat-skills の skills/design/sud-design/scripts/check-statechart.py (commit d8b5836c13a5330608e87784a078ce97990239dd)。
# 版はこの file の commit で固定する。直すなら vendor 元を直してから写し、上の commit を書き換える (CONTRIBUTING.md の「gate」)。
# 元の shebang (uv run --script) は外し、標準ライブラリだけで動くので python3 (3.11 以上) で撃つ。test は scripts/checkstatechart
# 以下は vendor 元の 6 行目以降をそのまま写したもの。
"""PlantUML 状態遷移図の 状態×イベント 組合せを網羅検査する。

設計段階の状態機械は「描いて満足」で終わりやすく、未定義の (状態, イベント) 組合せは
図の見た目からは欠落と判別できない。本 script は状態遷移図から状態とイベントを機械的に
列挙し、全組合せを生成して実際の遷移と突き合わせる:

1. **未定義組合せ** — どの遷移も定義されていない (状態, イベント) は、`uncovered`
   宣言 (下記) で理由を明記していなければ違反。上げないと決めた組合せに理由を要求する
   ことで、「定義漏れ」と「設計判断としての未定義」を区別可能にする
2. **残骸宣言** — 遷移が実在する組合せへの `uncovered` 宣言、実在しない状態 / イベントを
   指す宣言は違反 (遷移が後から足された・改名された残骸)
3. **到達不能状態** — 初期状態 `[*]` から遷移をたどって到達できない状態は違反

対応する記法 (この範囲で書く。複合状態・並行領域は対象外):

    [*] --> StateA
    StateA --> StateB : event1
    StateA -up-> StateC : event2 [guard] / action
    StateB --> [*]
    state "表示名" as StateD
    ' uncovered: StateB x event2 — <未定義にする理由>

イベント名は遷移ラベルの `[guard]` / `/ action` より前の部分。ラベル無しの状態間遷移は
自動遷移として到達可能性にだけ数え、組合せ表には載せない。

用法:
    check-statechart.py <statechart.puml>

Exit 0 = 違反なし / 1 = 違反あり or 検査不能 / 2 = 引数エラー。
"""

from __future__ import annotations

import argparse
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path

# `A --> B : label` / `A -up-> B` / `[*] --> A` の遷移行。
TRANSITION = re.compile(
    r"^\s*(?P<src>\[\*\]|[\w.]+)\s*"
    r"-+(?:up|down|left|right|u|d|l|r)?-*>\s*"
    r"(?P<dst>\[\*\]|[\w.]+)\s*"
    r"(?::\s*(?P<label>.+?))?\s*$"
)
# `state "表示名" as X` / `state X` の宣言行。`state X {` (複合状態) は対象外なので拾わない。
STATE_DECL = re.compile(r'^\s*state\s+(?:"[^"]*"\s+as\s+)?(?P<name>[\w.]+)\s*$')
# `' uncovered: <state> x <event> — <理由>` の宣言行。理由は必須。
UNCOVERED_DECL = re.compile(
    r"^\s*'\s*uncovered:\s*(?P<state>[\w.]+)\s+x\s+(?P<event>\S+)\s+[—-]\s+(?P<reason>\S.*)$"
)
# `' uncovered:` で始まるのに上の形に合わない行 (理由欠落など) を落とすための検出。
UNCOVERED_PREFIX = re.compile(r"^\s*'\s*uncovered:")
INITIAL = "[*]"


@dataclass
class Statechart:
    states: set[str] = field(default_factory=set)
    events: set[str] = field(default_factory=set)
    # (src, event) -> dst。自動遷移 (ラベル無し) は event=None で edges にだけ入る。
    covered: set[tuple[str, str]] = field(default_factory=set)
    edges: list[tuple[str, str]] = field(default_factory=list)
    uncovered_decls: list[tuple[str, str, int]] = field(default_factory=list)
    has_initial: bool = False
    problems: list[str] = field(default_factory=list)


def event_name(label: str) -> str | None:
    """遷移ラベルからイベント名を取り出す。guard / action だけのラベルは None."""
    name = re.split(r"[\[/]", label, maxsplit=1)[0].strip()
    return name or None


def parse_statechart(path: Path, text: str) -> Statechart:
    chart = Statechart()
    for lineno, raw in enumerate(text.splitlines(), start=1):
        line = raw.strip()
        if not line:
            continue
        decl = UNCOVERED_DECL.match(raw)
        if decl:
            chart.uncovered_decls.append((decl.group("state"), decl.group("event"), lineno))
            continue
        if UNCOVERED_PREFIX.match(raw):
            chart.problems.append(
                f"[statechart uncovered malformed] {path}:{lineno} — uncovered 宣言の形は "
                f"`' uncovered: <state> x <event> — <理由>` (理由まで必須): {line!r}"
            )
            continue
        if line.startswith("'"):
            continue
        state_decl = STATE_DECL.match(raw)
        if state_decl:
            chart.states.add(state_decl.group("name"))
            continue
        transition = TRANSITION.match(raw)
        if not transition:
            continue
        src, dst = transition.group("src"), transition.group("dst")
        label = transition.group("label")
        if src == INITIAL:
            chart.has_initial = True
        else:
            chart.states.add(src)
        if dst != INITIAL:
            chart.states.add(dst)
        chart.edges.append((src, dst))
        if label is not None and src != INITIAL:
            name = event_name(label)
            if name is not None:
                chart.events.add(name)
                chart.covered.add((src, name))
    return chart


def reachable_states(chart: Statechart) -> set[str]:
    """初期状態から遷移をたどって届く状態の集合。"""
    adjacency: dict[str, set[str]] = {}
    for src, dst in chart.edges:
        adjacency.setdefault(src, set()).add(dst)
    seen: set[str] = set()
    frontier = [INITIAL]
    while frontier:
        node = frontier.pop()
        for nxt in adjacency.get(node, ()):
            if nxt not in seen and nxt != INITIAL:
                seen.add(nxt)
                frontier.append(nxt)
    return seen


def coverage_violations(path: Path, chart: Statechart) -> list[str]:
    violations: list[str] = []
    declared = {(state, event) for state, event, _ in chart.uncovered_decls}
    for state in sorted(chart.states):
        for event in sorted(chart.events):
            combo = (state, event)
            if combo not in chart.covered and combo not in declared:
                violations.append(
                    f"[statechart uncovered] {path} — 組合せ ({state} x {event}) に遷移が無く、"
                    f"uncovered 宣言も無い (定義するか、理由を宣言する)"
                )
    for state, event, lineno in chart.uncovered_decls:
        if (state, event) in chart.covered:
            violations.append(
                f"[statechart decl stale] {path}:{lineno} — ({state} x {event}) には遷移が"
                f"実在する (宣言は残骸。消す)"
            )
        if state not in chart.states:
            violations.append(
                f"[statechart decl unknown] {path}:{lineno} — 状態 {state} は図に無い"
            )
        if event not in chart.events:
            violations.append(
                f"[statechart decl unknown] {path}:{lineno} — イベント {event} は図に無い"
            )
    return violations


def reachability_violations(path: Path, chart: Statechart) -> list[str]:
    if not chart.has_initial:
        return [f"[statechart no initial] {path} — 初期遷移 `[*] --> <state>` が無い"]
    unreachable = chart.states - reachable_states(chart)
    return [
        f"[statechart unreachable] {path} — 状態 {state} に初期状態から到達できない"
        for state in sorted(unreachable)
    ]


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("path", help="検査する PlantUML 状態遷移図 (.puml)")
    return p.parse_args(argv)


def main(args: argparse.Namespace) -> int:
    path = Path(args.path)
    try:
        text = path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        print(f"[statechart unreadable] {path}: {exc}", file=sys.stderr)
        return 1
    chart = parse_statechart(path, text)
    problems = list(chart.problems)
    if not chart.states:
        problems.append(f"[statechart empty] {path} — 状態が 1 つも読み取れない (対応記法は --help)")
    else:
        problems.extend(reachability_violations(path, chart))
        problems.extend(coverage_violations(path, chart))
    for line in problems:
        print(line, file=sys.stderr)
    if problems:
        return 1
    combos = len(chart.states) * len(chart.events)
    print(
        f"ok: {path} — 状態 {len(chart.states)} × イベント {len(chart.events)} = "
        f"{combos} 組合せ (遷移 {len(chart.covered)} / uncovered 宣言 {len(chart.uncovered_decls)})"
    )
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main(parse_args()))
    except KeyboardInterrupt:
        sys.exit(130)
