#!/usr/bin/env python3
# vendor 元: swat9013/swat-skills の skills/design/sud-design/scripts/check-statechart.py (commit 8678b5e7164e27766c5455f90564e1853f1773c4)。
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
4. **行き止まり** — 自分以外へ出ていく遷移が 1 本も無い状態 (自分へ戻る遷移しか持たない
   状態を含む) は、final state (`State --> [*]`) と宣言していなければ違反。終わる状態と、
   遷移の書き忘れで抜け出せない状態を区別する
5. **終端に届かない状態** — 図に final state が 1 つでもあれば、どの状態からも final state
   へ遷移をたどって届くことを要求する。final state を持たない図 (画面遷移のように終わりの
   無い状態機械) には要求しない
6. **非決定** — 同じ状態から同じイベントで別々の遷移先へ向かう遷移は、guard で分かれて
   いなければ違反。guard が同じ遷移どうしと、guard 無しの遷移と同じイベントの他の遷移
   (guard 無しは常に発火できるので、どの guard とも重なる) が対象。guard が違えば正当な
   分岐として数える。ラベル無しの自動遷移と guard だけのラベル (`: [ok]`) はイベント名が
   空の遷移として比べる (イベント名の無い `State --> [*]` は final state の宣言なので除く)

対応する記法 (この範囲で書く。複合状態・並行領域は対象外):

    [*] --> StateA
    StateA --> StateB : event1
    StateA -up-> StateC : event2 [guard] / action
    StateB --> [*]                      (StateB を final state と宣言する)
    state "表示名" as StateD
    ' uncovered: StateB x event2 — <未定義にする理由>

例はすべて `@startuml` 〜 `@enduml` の中に書く。矢印には方向語と `[色・線種]` を足してよい
(`StateA -[#red,dashed]left-> StateB : event3`)。図に描かれない行は数えない: `@startuml` 〜
`@enduml` の外、block comment (`/' ... '/`) の中、複数行 note (`note left of X` / `note as N` /
`note on link` 〜 `end note`) の中、複数行の見出し類 (`legend` 〜 `endlegend` など) の中、
`[hidden]` を付けた矢印の線 (両端の状態は数える)。

イベント名は遷移ラベルの `[guard]` / `/ action` より前の部分。guard はイベント名の直後の
`[...]` で、中に `/` や入れ子の `[...]` を含んでよい。ラベル無しの状態間遷移と guard だけの
ラベルの遷移は、到達可能性・行き止まり・終端到達・非決定にだけ数え、組合せ表には載せない。
`--> [*]` にラベルを付けると、そのラベルはイベントとして組合せ表に入り、他の全状態との
組合せに遷移か uncovered 宣言が要る。

用法:
    check-statechart.py <statechart.puml>

Exit 0 = 違反なし / 1 = 違反あり or 検査不能 / 2 = 引数エラー。

invariant は上の 1〜6 (2 は「遷移が実在する組合せへの宣言」と「実在しない状態 / イベントを指す宣言」の
2 句に、それぞれ拒否テストを 1 本ずつ持つ) に、次を足したもの:

10. 理由の無い (形の崩れた) `uncovered` 宣言は違反
11. 矢印を含むのに対応記法の遷移として読めない行は違反 (黙って飛ばさない)
12. 図に描かれない行 (上の「数えない」行) の遷移は数えない

と、検査不能で落とす (何も検査せずに通さない) 次の 3 つ:

7. 図が読めない (不在・UTF-8 でない) と exit 1
8. 状態が 1 つも読み取れないと exit 1
9. 初期遷移 `[*] --> <state>` が無いと exit 1
"""

from __future__ import annotations

import argparse
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path

# `A --> B : label` / `A -up-> B` / `A -[#red,dashed]left-> B` / `[*] --> A` の遷移行。
# 矢印の中の方向語と `[色・線種]` は描画だけを変えるので、どの順でも読み飛ばす。
TRANSITION = re.compile(
    r"^\s*(?P<src>\[\*\]|[\w.]+)\s*"
    r"(?P<arrow>-+(?:(?:up|down|left|right|u|d|l|r)|\[[^\]]*\])*-*>)\s*"
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
# 上の TRANSITION に合わなかった行が矢印 (`->` / `..>` / `==>` / `<-` 等) を含むかの検出。
# 黙って飛ばすと網羅検査を素通りする。`:` より後 (ラベル・note の本文・状態の説明文) は見ない。
ARROW = re.compile(r"[-.=]>|<[-.=]")
# 矢印を含んでも遷移ではない行 (1 行の見出し類と note。本文に矢印を書いてよい)。
NON_TRANSITION_KEYWORD = re.compile(
    r"^\s*(?:title|caption|header|footer|legend|[rh]?note)\b", re.IGNORECASE
)
# 複数行 note の開始 (render-puml.py と同じく `:` を持たない note 行)。`note "本文" as N` は 1 行で
# 閉じ、`note --> B` は `note` という状態の遷移なので当たらない。
NOTE_BLOCK_START = re.compile(
    r'^\s*(?:r|h)?note\b(?![^:]*:)(?!\s+"[^"]*"\s+as\b)(?!\s*[-.=<])', re.IGNORECASE
)
NOTE_BLOCK_END = re.compile(r"^\s*end\s*(?:r|h)?note\b", re.IGNORECASE)
# 複数行の見出し類 (`legend right` 〜 `endlegend` など。本文を持たない開始行) と、その終了。
HEADING_BLOCK_START = re.compile(
    r"^\s*(?P<kind>title|caption|header|footer|legend)(?:\s+(?:left|right|center|top|bottom))*\s*$",
    re.IGNORECASE,
)
# 初期状態と final state の両方を表す擬似状態 (`[*] --> A` は初期遷移、`A --> [*]` は final の宣言)。
PSEUDO_STATE = "[*]"


@dataclass
class Statechart:
    states: set[str] = field(default_factory=set)
    events: set[str] = field(default_factory=set)
    # 組合せ表に載る (src, event)。イベント名を持つ遷移だけが入る。
    covered: set[tuple[str, str]] = field(default_factory=set)
    edges: list[tuple[str, str]] = field(default_factory=list)
    # (src, event) -> guard -> 遷移先の集合。非決定の検出に使う。イベント名が空の遷移
    # (自動遷移・guard だけのラベル) は event="" で入る。
    targets: dict[tuple[str, str], dict[str, set[str]]] = field(default_factory=dict)
    uncovered_decls: list[tuple[str, str, int]] = field(default_factory=list)
    has_initial: bool = False
    problems: list[str] = field(default_factory=list)


def split_label(label: str) -> tuple[str, str]:
    """遷移ラベルを (イベント名, guard の中身) に分ける。無い側は空文字.

    イベント名は最初の `[` か `/` より前。guard はイベント名の直後の `[...]` で、括弧の
    対応を数えて閉じ位置を決める (guard の中の `/` と入れ子の `[...]` を guard に残す)。
    """
    cut = re.search(r"[\[/]", label)
    if cut is None:
        return label.strip(), ""
    event = label[: cut.start()].strip()
    if label[cut.start()] != "[":
        return event, ""
    depth = 0
    for i in range(cut.start(), len(label)):
        if label[i] == "[":
            depth += 1
        elif label[i] == "]":
            depth -= 1
            if depth == 0:
                return event, label[cut.start() + 1 : i].strip()
    return event, label[cut.start() + 1 :].strip()


def drawn_lines(text: str):
    """plantuml が図に描く行 (@startuml〜@enduml の中で、block comment・複数行 note・複数行の見出し類の外) を (行番号, 行) で返す."""
    in_diagram = in_block_comment = False
    # 複数行 note / 見出し類の中にいる間の、その終了行の形。
    block_end: re.Pattern[str] | None = None
    for lineno, raw in enumerate(text.splitlines(), start=1):
        line = raw.strip()
        if not in_diagram:
            in_diagram = line.startswith("@startuml")
            continue
        if line.startswith("@enduml"):
            # 閉じ忘れた comment / note を次の @startuml ブロックへ持ち越さない。
            in_diagram = in_block_comment = False
            block_end = None
            continue
        if block_end is not None:
            if block_end.match(raw):
                block_end = None
            continue
        if in_block_comment:
            in_block_comment = not line.endswith("'/")
            continue
        if line.startswith("/'"):
            in_block_comment = "'/" not in line[2:]
            continue
        if NOTE_BLOCK_START.match(raw):
            block_end = NOTE_BLOCK_END
            continue
        heading = HEADING_BLOCK_START.match(raw)
        if heading:
            block_end = re.compile(rf"^\s*end\s*{heading.group('kind')}\b", re.IGNORECASE)
            continue
        yield lineno, raw


def parse_statechart(path: Path, text: str) -> Statechart:
    chart = Statechart()
    for lineno, raw in drawn_lines(text):
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
            if ARROW.search(line.split(":", 1)[0]) and not NON_TRANSITION_KEYWORD.match(raw):
                chart.problems.append(
                    f"[statechart unsupported arrow] {path}:{lineno} — 矢印を含むが対応記法の遷移として"
                    f"読めない (対応記法は --help。`B <-- A` は `A --> B` と書く): {line!r}"
                )
            continue
        src, dst = transition.group("src"), transition.group("dst")
        label = transition.group("label")
        if "hidden" in transition.group("arrow"):
            # `[hidden]` の矢印は配置の調整で線だけが描かれない。両端の状態は描かれるので検査に残す。
            chart.states.update(name for name in (src, dst) if name != PSEUDO_STATE)
            continue
        if src == PSEUDO_STATE:
            chart.has_initial = True
        else:
            chart.states.add(src)
        if dst != PSEUDO_STATE:
            chart.states.add(dst)
        chart.edges.append((src, dst))
        if src == PSEUDO_STATE:
            continue
        event, guard = split_label(label) if label is not None else ("", "")
        if not event and dst == PSEUDO_STATE:
            # イベント名の無い `State --> [*]` (`: / cleanup` のような action だけのラベルを含む) は
            # final state の宣言で、遷移として比べない。
            continue
        if event:
            chart.events.add(event)
            chart.covered.add((src, event))
        chart.targets.setdefault((src, event), {}).setdefault(guard, set()).add(dst)
    return chart


def reachable_states(chart: Statechart) -> set[str]:
    """初期状態から遷移をたどって届く状態の集合。"""
    adjacency: dict[str, set[str]] = {}
    for src, dst in chart.edges:
        adjacency.setdefault(src, set()).add(dst)
    seen: set[str] = set()
    frontier = [PSEUDO_STATE]
    while frontier:
        node = frontier.pop()
        for nxt in adjacency.get(node, ()):
            if nxt not in seen and nxt != PSEUDO_STATE:
                seen.add(nxt)
                frontier.append(nxt)
    return seen


def final_states(chart: Statechart) -> set[str]:
    """`State --> [*]` で final state と宣言された状態の集合。"""
    return {src for src, dst in chart.edges if dst == PSEUDO_STATE and src != PSEUDO_STATE}


def termination_violations(path: Path, chart: Statechart) -> list[str]:
    """行き止まりと、final state へ届かない状態を返す。"""
    violations: list[str] = []
    finals = final_states(chart)
    dead_ends = chart.states - {src for src, dst in chart.edges if dst != src}
    for state in sorted(dead_ends):
        violations.append(
            f"[statechart dead end] {path} — 状態 {state} から自分以外へ出ていく遷移が無い "
            f"(図全体の終わりにあたる状態なら `{state} --> [*]` で final state と宣言し、そうでなければ遷移を足す)"
        )
    if not finals:
        return violations
    reverse: dict[str, set[str]] = {}
    for src, dst in chart.edges:
        if src != PSEUDO_STATE and dst != PSEUDO_STATE:
            reverse.setdefault(dst, set()).add(src)
    reaches_final = set(finals)
    frontier = list(finals)
    while frontier:
        node = frontier.pop()
        for prev in reverse.get(node, ()):
            if prev not in reaches_final:
                reaches_final.add(prev)
                frontier.append(prev)
    # 行き止まりは上で名指し済みなので、終端に届かない側では重ねて挙げない。
    for state in sorted(chart.states - reaches_final - dead_ends):
        violations.append(
            f"[statechart no termination] {path} — 状態 {state} から final state "
            f"({', '.join(sorted(finals))}) へ届かない"
        )
    return violations


def determinism_violations(path: Path, chart: Statechart) -> list[str]:
    violations: list[str] = []
    for (src, event), by_guard in sorted(chart.targets.items()):
        trigger = event or "(イベント無し)"
        for guard, dsts in sorted(by_guard.items()):
            if len(dsts) > 1:
                label = f"{trigger} [{guard}]" if guard else trigger
                violations.append(
                    f"[statechart nondeterministic] {path} — ({src} x {label}) の遷移先が "
                    f"{', '.join(sorted(dsts))} に分かれる (guard で分けるか、遷移を 1 本にする)"
                )
        all_dsts = set().union(*by_guard.values())
        if "" in by_guard and len(by_guard) > 1 and len(all_dsts) > 1:
            violations.append(
                f"[statechart nondeterministic] {path} — ({src} x {trigger}) に guard 無しの遷移と "
                f"guard 付きの遷移があり、遷移先が {', '.join(sorted(all_dsts))} に分かれる "
                "(guard 無しの遷移にも guard を付けて排他にする)"
            )
    return violations


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
    # 検査項目と記法の正本を `--help` で読ませるので、docstring の改行 (記法の例は 1 行 1 遷移) を保つ。
    p = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    p.add_argument("path", help="検査する PlantUML 状態遷移図 (.puml)")
    return p.parse_args(argv)


def main(args: argparse.Namespace) -> int:
    path = Path(args.path)
    try:
        text = path.read_text(encoding="utf-8-sig")
    except (OSError, UnicodeDecodeError) as exc:
        print(f"[statechart unreadable] {path}: {exc}", file=sys.stderr)
        return 1
    chart = parse_statechart(path, text)
    problems = list(chart.problems)
    if not chart.states:
        problems.append(f"[statechart empty] {path} — 状態が 1 つも読み取れない (図は `@startuml` 〜 `@enduml` の中だけを読む。対応記法は --help)")
    else:
        problems.extend(reachability_violations(path, chart))
        problems.extend(termination_violations(path, chart))
        problems.extend(determinism_violations(path, chart))
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
