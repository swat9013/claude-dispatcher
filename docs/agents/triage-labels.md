# Triage Labels

The skills speak in terms of five canonical triage roles. This file maps those roles to the actual label strings used in this repo's issue tracker.

| Label in mattpocock/skills | Label in our tracker | Meaning                                  |
| -------------------------- | -------------------- | ---------------------------------------- |
| `needs-triage`             | `needs-triage`       | Maintainer needs to evaluate this issue  |
| `needs-info`               | `needs-info`         | Waiting on reporter for more information |
| `ready-for-agent`          | `ready-for-agent`    | Fully specified, ready for an AFK agent  |
| `ready-for-human`          | `ready-for-human`    | Requires human implementation            |
| `wontfix`                  | `wontfix`            | Will not be actioned                     |

## 深掘り待ち (この repo で足した role)

上の 5 role に無い role として、深掘り待ちを 1 つ足す。

| Role   | Label in our tracker | Meaning                                                                    |
| ------ | -------------------- | -------------------------------------------------------------------------- |
| 深掘り待ち | `need-grilling`      | 着手可にする前に、深掘りの対話 (grilling) で決めることが残っている issue |

- triage で、選択肢を 2〜4 個に絞れない論点を抱えた issue に付ける。triage は済んでいるので `needs-triage` は外す
- 深掘りの対話は、user が任意のセッションで grill 系の skill を呼んで行う。論点が固まったら、決定を issue に記録し、`need-grilling` を外して着手可 label を付ける (付ける前に `ready-for-agent-review` を通す)

When a skill mentions a role (e.g. "apply the AFK-ready triage label"), use the corresponding label string from this table.

Edit the right-hand column to match whatever vocabulary you actually use.
