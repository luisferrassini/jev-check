# test-quality calibration

This record is not a fixture: `eval` ignores files that do not end in `.patch`.

- Date: 2026-09-26
- Model: `jev-latest` was requested; the responses named `jev-1.13.0`
- Question revision: `.jev-check-example/input/questions/test-quality.json` as committed with this file
- Corpus: 14 pass, 2 `fail/tests_use_entry_point`, 2 `fail/no_sleep_or_network`, 2 `fail/failures_name_what_failed`
- Command, run three times with the cache bypassed, in a disposable project set up as the README shows:
  `jev-check eval test-quality "$tmp" --no-cache`

| Question | Run | Lowest-pass | Highest-fail |
| --- | --- | --- | --- |
| `failures_name_what_failed` | 1 | 0.79 | 0.06 |
| `failures_name_what_failed` | 2 | 0.77 | 0.06 |
| `failures_name_what_failed` | 3 | 0.78 | 0.06 |
| `no_sleep_or_network` | 1 | 0.94 | 0.03 |
| `no_sleep_or_network` | 2 | 0.93 | 0.03 |
| `no_sleep_or_network` | 3 | 0.94 | 0.03 |
| `tests_use_entry_point` | 1 | 0.86 | 0.13 |
| `tests_use_entry_point` | 2 | 0.86 | 0.14 |
| `tests_use_entry_point` | 3 | 0.86 | 0.12 |

All three runs had 0 misses. Over all runs, `failures_name_what_failed` separates in `0.06 < t <= 0.77`, `no_sleep_or_network` in `0.03 < t <= 0.93`, and `tests_use_entry_point` in `0.14 < t <= 0.86`. The tightest bounds are 0.14 (a fail for `tests_use_entry_point`) and 0.77 (a pass for `failures_name_what_failed`). A threshold of 0.45 for the check is inside every interval. Its margin is 0.31 below and 0.32 above.

The pass set holds these hard cases: a fake server from `httptest.NewServer`, an exported handler tested with `httptest.NewRecorder`, a listener on 127.0.0.1, `t.TempDir`, a helper with `t.Helper` defined in the patch, a test that calls helpers such as `wantCode(t, ...)` defined in another file, a table test whose `t.Fatalf` names the case with got and want, `time.After` used as a timeout guard in a `select`, `example.com` URLs used only as data, a patch that removes a `time.Sleep`, a non-test Go file with `time.Sleep`, a non-test Go file with a real `http.Post`, a Python test with `time.sleep`, and a docs file.

## Revisions before these runs

None. The first wording and fixtures scored 0 misses on run 1, and runs 2 and 3 confirmed them without changes.

## Limitations

- 20 fixtures, Go only, one model, three runs on one day.
- `tests_use_entry_point` sees one patch. It cannot see whether a lowercase function is defined in another `_test.go` file. So it treats any function that takes `*testing.T` or `testing.TB` as a test helper. A test helper without that argument, defined in another file, can fail by mistake. It also cannot tell whether a package has a public entry point at all, so it may fail a test of an internal function that has no other way in.
- `no_sleep_or_network` sees only direct calls. It does not follow a call through `run` or a package function to a real host.
- `failures_name_what_failed` accepts `t.Fatal(err)`, trusting that the error names the problem.
- A pass means one patch shows no violation of these three rules, not that the tests are good. Recalibrate after changing the questions or the model.
