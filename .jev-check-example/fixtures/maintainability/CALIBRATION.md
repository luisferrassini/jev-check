# maintainability calibration

This record is not a fixture: `eval` ignores files that do not end in `.patch`.

- Date: 2026-09-26
- Model: `jev-latest` (a moving alias; the exact version was not exposed)
- Question revision: `.jev-check-example/input/questions/maintainability.json` as committed with this file
- Corpus: 14 pass, 2 `fail/no_redundant_forwarding`, 2 `fail/no_mixed_output_channels`
- Command, run twice with the cache bypassed, in a disposable project set up as the README shows:
  `jev-check eval maintainability "$tmp" --no-cache`

| Question | Run 1 lowest-pass | Run 1 highest-fail | Run 2 lowest-pass | Run 2 highest-fail |
| --- | --- | --- | --- | --- |
| `no_mixed_output_channels` | 0.86 | 0.1 | 0.87 | 0.11 |
| `no_redundant_forwarding` | 0.77 | 0.19 | 0.82 | 0.21 |

Both runs had 0 misses. Over both runs, `no_mixed_output_channels` separates in `0.11 < t <= 0.86` (midpoint 0.485) and `no_redundant_forwarding` in `0.21 < t <= 0.77` (midpoint 0.49). A threshold of 0.49 for the check is inside both intervals, with a margin of at least 0.28 on each side.

## Revisions before these runs

1. The first wording asked about forwarding helpers in general. The model failed accepted cases: a documented exported operation, a package-level helper with no visible callers, and helpers used more than once (lowest-pass 0.26). The question now names the only case a patch can establish, a local closure with its whole enclosing function visible, and it asks to count calls first.
2. The callback-adapter fixture had a closure with the same signature as the function it called, so it really was redundant. It was replaced by a real adapter, a `context.AfterFunc` callback that drops `Close`'s error.
3. The repeated-use fixture was a plain alias of `strconv.Quote`, and later a printing helper that was also unclear for the output question. It was replaced by a validation closure that is called three times and has no output.

Limitations: 18 fixtures, Go only, one model alias, two runs. A pass means one patch shows no violation of these two rules, not that the project is maintainable. Recalibrate after changing the questions or the model.
