# coding-style calibration

This record is not a fixture: `eval` ignores files that do not end in `.patch`.

- Date: 2026-09-26
- Model: `jev-latest` (a moving alias; the exact version was not exposed)
- Question revision: `input/questions/coding-style.json` and `CODING_STYLE.md` as committed with this file
- Corpus: 5 pass, 2 `fail/clear_names`, 2 `fail/actionable_errors`
- Command, run twice with the cache bypassed, in a disposable project set up as the README shows:
  `jev-check eval coding-style "$tmp" --no-cache`

| Question | Run 1 lowest-pass | Run 1 highest-fail | Run 2 lowest-pass | Run 2 highest-fail |
| --- | --- | --- | --- | --- |
| `actionable_errors` | 0.85 | 0.05 | 0.86 | 0.05 |
| `clear_names` | 0.88 | 0.17 | 0.86 | 0.16 |

Both runs had 0 misses at threshold 0.5. Over both runs, `actionable_errors` separates in `0.05 < t <= 0.85` and `clear_names` in `0.17 < t <= 0.86`. A threshold of 0.5 is inside both intervals, with a margin of at least 0.33 on each side.

An earlier draft of `fail/clear_names/sync.go.patch` scored 0.52 and 0.64. Its short function had a comment that explained the names, so the case was ambiguous under the rule. The fixture was rewritten so the generic names cross a function boundary with nothing to explain them, and both runs above use the rewritten fixture.

Limitations: nine fixtures, one model alias, two runs. A pass is a judgment over one patch. Recalibrate after changing the questions, `CODING_STYLE.md`, or the model.
