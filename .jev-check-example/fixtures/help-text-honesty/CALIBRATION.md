# help-text-honesty calibration

This record is not a fixture: `eval` ignores files that do not end in `.patch`.

- Date: 2026-09-26
- Model: `jev-latest` (a moving alias; the responses reported `jev-1.13.0`)
- Question revision: `.jev-check-example/input/questions/help-text-honesty.json` as committed with this file
- Corpus: 14 pass, 3 `fail/help_flags_exist`, 4 `fail/help_behavior_matches` (Go and Python)
- Command, run twice with the cache bypassed, in a disposable project set up as the README shows:
  `jev-check eval help-text-honesty "$tmp" --no-cache`

| Question | Run 1 lowest-pass | Run 1 highest-fail | Run 2 lowest-pass | Run 2 highest-fail |
| --- | --- | --- | --- | --- |
| `help_behavior_matches` | 0.75 | 0.24 | 0.77 | 0.23 |
| `help_flags_exist` | 0.7 | 0.29 | 0.65 | 0.29 |

Both runs had 0 misses. Over both runs, `help_behavior_matches` separates in `0.24 < t <= 0.75` and `help_flags_exist` in `0.29 < t <= 0.65`. A threshold of 0.47 for the check is inside both intervals, with a margin of at least 0.18 on each side.

## Revisions before these runs

1. With the first wording, `fail/help_behavior_matches/rename_files.go.patch` scored 0.85 and then 0.78, above every pass. Its dry-run path printed the new name and then fell through to `os.Rename`, and the model did not follow that control flow. The fixture now renames inside the `if dry` branch, and the instructions ask to follow the code path of each claimed mode. It then scored 0.11.
2. `fail/help_behavior_matches/linkcheck.go.patch` (the help says exit status 1, the code exits with 4) scored 0.69. The criteria now name an exit status as a concrete claim, with this kind of case as an example. It then scored 0.2 or less. The example is close to the fixture, so this case is less independent than the others.
3. The first `fail/help_flags_exist/apply_plan.go.patch` kept the usage text outside the hunk, so only the hunk header showed it. The usage lines were moved into the `--help` case, so the patch shows them next to the removed `--dry-run` case.

## Limitations

- 21 fixtures, Go and Python only, one model alias, two runs.
- The lowest `help_flags_exist` pass is the deletion-only patch (0.65 and 0.7). Its removed usage lists a flag that its removed parser does not handle, and the model partly judges removed lines. A clean file with a similar removed mismatch may score near the threshold.
- `help_behavior_matches` also scores the flag-mismatch fixtures low (0.34 to 0.49). Eval does not count that, but in the gate a missing flag may fail both questions.
- The model follows simple control flow poorly. A behavior claim that is contradicted only through a fall-through path, and not in one visible branch, can pass.
- A pass means one patch shows no mismatch, not that the help text is complete or correct. Recalibrate after changing the questions or the model.
