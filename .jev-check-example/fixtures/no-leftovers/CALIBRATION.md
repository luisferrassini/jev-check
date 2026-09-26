# no-leftovers calibration

This record is not a fixture: `eval` ignores files that do not end in `.patch`.

- Date: 2026-09-26
- Model: `jev-latest` (a moving alias; the exact version was not exposed)
- Question revision: `.jev-check-example/input/questions/no-leftovers.json` as committed with this file
- Corpus: 15 pass, 3 `fail/no_debug_prints`, 3 `fail/no_commented_out_code`, 3 `fail/no_bare_todos`, 3 `fail/no_temporary_skips`. The patches are in Go, Python, JavaScript, TypeScript, Rust, Java, shell, and Markdown.
- Command, run five times with the cache bypassed, in a disposable project set up as the README shows:
  `jev-check eval no-leftovers "$tmp" --no-cache`

Lowest-pass / highest-fail per run:

| Question | Run 1 | Run 2 | Run 3 | Run 4 | Run 5 |
| --- | --- | --- | --- | --- | --- |
| `no_bare_todos` | 0.94 / 0.04 | 0.94 / 0.04 | 0.94 / 0.04 | 0.94 / 0.03 | 0.94 / 0.04 |
| `no_commented_out_code` | 0.94 / 0.03 | 0.94 / 0.03 | 0.94 / 0.03 | 0.94 / 0.03 | 0.93 / 0.03 |
| `no_debug_prints` | 0.82 / 0.02 | 0.83 / 0.02 | 0.84 / 0.02 | 0.78 / 0.02 | 0.81 / 0.02 |
| `no_temporary_skips` | 0.9 / 0.03 | 0.89 / 0.03 | 0.9 / 0.03 | 0.89 / 0.03 | 0.9 / 0.02 |

Every run had 0 misses. Runs 1 to 3 used threshold 0.5. Runs 4 and 5 used 0.43 and were made after the files were recreated from the same source, with the same content. Over all runs, `no_bare_todos` separates in `0.04 < t <= 0.94`, `no_commented_out_code` in `0.03 < t <= 0.93`, `no_debug_prints` in `0.02 < t <= 0.78`, and `no_temporary_skips` in `0.03 < t <= 0.89`. All four intervals share `0.04 < t <= 0.78`. A threshold of 0.41, the midpoint of that shared interval, is inside every interval, with a margin of at least 0.37 on each side.

## Revisions before these runs

None. The first wording and corpus had 0 misses, so nothing was changed. These choices were made before the first run:

1. Each question judges one kind of leftover and says to ignore the other three, so a fixture for one rule does not pull down the others.
2. The pass side holds a hard negative for each rule: logger calls at debug level (Go `slog`, Java SLF4J), prints and echo lines that are a CLI's or script's output, prose comments that name functions and values, a Go doc comment with an indented usage example, TODOs with an issue number or an owner, skips guarded by the operating system, `testing.Short()`, or a missing environment variable, a Markdown guide that quotes every kind of leftover, and two patches that only remove leftovers.
3. "Context" for a TODO is defined as an issue reference, an owner, or a specific statement of the remaining work. A temporary skip is one with no condition and no tracking issue, or a focus marker such as `it.only`.

Limitations: 27 fixtures, one model alias, five runs. The cases are short and clear; borderline cases were not tested, such as a TODO with a vague sentence, an unconditional skip with only a reason, a commented-out line in a sample config file, or `print` in a small script whose purpose is unclear. `no_debug_prints` has the lowest pass scores (0.78 to 0.84), so it is the question most likely to drift. The gate sends one file's patch at a time, so a question cannot see whether a print is covered by a CLI contract in another file. A pass means one patch shows none of these leftovers, not that the code is ready. Recalibrate after changing the questions or the model.
