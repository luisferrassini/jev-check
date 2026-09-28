# coding-style calibration

This record is not a fixture: `eval` ignores files that do not end in `.patch`.

- Date: 2026-09-28
- Model: requested `jev-latest` (a moving alias); every response named `jev-1.13.0`
- Question revision: `.jev-check-example/input/questions/coding-style.json` and `CODING_STYLE.md` as committed with this file
- Corpus: 5 pass, 2 `fail/clear_names`, 2 `fail/actionable_errors`
- Command, run twice with the cache bypassed, in a disposable project set up as the README shows:
  `jev-check eval coding-style "$tmp" --no-cache`

| Question | Run 1 lowest-pass | Run 1 highest-fail | Run 2 lowest-pass | Run 2 highest-fail |
| --- | --- | --- | --- | --- |
| `actionable_errors` | 0.84 | 0.09 | 0.85 | 0.08 |
| `clear_names` | 0.87 | 0.18 | 0.85 | 0.16 |

Both runs had 0 misses at threshold 0.5. Over both runs, `actionable_errors` separates in `0.09 < t <= 0.84` and `clear_names` in `0.18 < t <= 0.85`. A threshold of 0.5 is inside both intervals, with a margin of at least 0.32 on each side.

## What changed since the 2026-09-26 record

The questions no longer copy the rules. Each criterion now says that an added or changed line "violates rule <id> as the document in `coding_style` defines it", and keeps the yes for no relevant code or not enough evidence. The rules, their accepted cases, and their violations live only in `CODING_STYLE.md`. The corpus did not change. The scores moved by at most 0.04, and 0.5 still separates both questions.

An earlier draft of `fail/clear_names/sync.go.patch` scored 0.52 and 0.64. Its short function had a comment that explained the names, so the case was ambiguous under the rule. The fixture was rewritten so the generic names cross a function boundary with nothing to explain them.

Limitations: nine fixtures, one model version, two runs. A pass is a judgment over one patch. Recalibrate after changing the questions, `CODING_STYLE.md`, or the model.
