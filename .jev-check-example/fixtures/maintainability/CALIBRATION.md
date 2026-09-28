# maintainability calibration

This record is not a fixture: `eval` ignores files that do not end in `.patch`.

- Date: 2026-09-28
- Model: requested `jev-latest` (a moving alias); every response named `jev-1.13.0`
- Question revision: `.jev-check-example/input/questions/maintainability.json` as committed with this file
- Corpus: 17 pass, 2 `fail/no_redundant_forwarding`, 2 `fail/no_mixed_output_channels`
- Command, run twice with the cache bypassed, in a disposable project set up as the README shows:
  `jev-check eval maintainability "$tmp" --no-cache`

| Question | Run 1 lowest-pass | Run 1 highest-fail | Run 2 lowest-pass | Run 2 highest-fail |
| --- | --- | --- | --- | --- |
| `no_mixed_output_channels` | 0.86 | 0.13 | 0.86 | 0.14 |
| `no_redundant_forwarding` | 0.79 | 0.15 | 0.78 | 0.15 |

Both runs had 0 misses at threshold 0.49. Over both runs, `no_mixed_output_channels` separates in `0.14 < t <= 0.86` (midpoint 0.5) and `no_redundant_forwarding` in `0.15 < t <= 0.78` (midpoint 0.465). A threshold of 0.49 for the check is inside both intervals, with a margin of at least 0.29 on each side.

## Paired fixtures

Three pass fixtures each differ from one fail fixture in the behavior that decides the question:

| Pass | Fail | The one difference |
| --- | --- | --- |
| `pass/hosts.go.patch` | `fail/no_redundant_forwarding/allow.go.patch` | the closure is called twice, for a host and a proxy |
| `pass/summary_line.go.patch` | `fail/no_redundant_forwarding/summary.go.patch` | the closure adds an indent to the result |
| `pass/check_report.go.patch` | `fail/no_mixed_output_channels/check_json.go.patch` | the debug line goes to stderr |

`pass/export_cmd.go.patch` and `fail/no_mixed_output_channels/export_items.go.patch` were already such a pair.

## Revisions before these runs

On 2026-09-28, the new pairs were first run with the question from 2026-09-26. `pass/summary_line.go.patch` scored 0.47 and 0.50 on `no_redundant_forwarding`, so run 1 had a miss at 0.49. The fixture is correct under the rule: the closure changes its result.

1. The criteria now say that a literal joined to the returned value, or another call around it, changes the result. Two runs gave lowest-pass 0.58 and 0.57 for that fixture. 0.49 then separated, but with a margin of 0.08 on the pass side.
2. The instructions now ask the model to compare what the closure returns with the call inside it, after counting calls. The fixture scored 0.84 and 0.88, and the runs in the table use this wording.

Earlier revisions, recorded on 2026-09-26:

1. The first wording asked about forwarding helpers in general. The model failed accepted cases: a documented exported operation, a package-level helper with no visible callers, and helpers used more than once (lowest-pass 0.26). The question now names the only case a patch can establish, a local closure with its whole enclosing function visible, and it asks to count calls first.
2. The callback-adapter fixture had a closure with the same signature as the function it called, so it really was redundant. It was replaced by a real adapter, a `context.AfterFunc` callback that drops `Close`'s error.
3. The repeated-use fixture was a plain alias of `strconv.Quote`, and later a printing helper that was also unclear for the output question. It was replaced by a validation closure that is called three times and has no output.

Limitations: 21 fixtures, Go only, one model version, two runs. A pass means one patch shows no violation of these two rules, not that the project is maintainable. Recalibrate after changing the questions or the model.
