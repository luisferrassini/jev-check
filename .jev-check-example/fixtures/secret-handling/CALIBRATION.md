# secret-handling calibration

This record is not a fixture: `eval` ignores files that do not end in `.patch`.

- Date: 2026-09-26
- Model: `jev-latest` (a moving alias; the exact version was not exposed)
- Question revision: `.jev-check-example/input/questions/secret-handling.json` as committed with this file
- Corpus: 14 pass, 3 `fail/no_logged_credentials`, 3 `fail/no_unvalidated_destination`, in Go, Python, JavaScript, shell, and Markdown. Every fixture passes `jev-check secrets`.
- Command, run twice with the cache bypassed, in a disposable project set up as the README shows (`.jev-check-example/fixtures/secret-handling`, no `CODING_STYLE.md`, `{"check":"secret-handling","threshold":0.5}`):
  `jev-check eval secret-handling "$tmp" --no-cache`

| Question | Run 1 lowest-pass | Run 1 highest-fail | Run 2 lowest-pass | Run 2 highest-fail |
| --- | --- | --- | --- | --- |
| `no_logged_credentials` | 0.83 | 0.08 | 0.79 | 0.1 |
| `no_unvalidated_destination` | 0.7 | 0.27 | 0.72 | 0.31 |

Both runs had 0 misses. Over both runs, `no_logged_credentials` separates in `0.1 < t <= 0.79` and `no_unvalidated_destination` in `0.31 < t <= 0.7`. Only the second interval limits the choice. A threshold of 0.5 for the check is inside both, with a margin of 0.19 above the highest fail and 0.2 below the lowest pass.

The closest cases are these. For `no_unvalidated_destination`, the lowest pass is `pusher.go` (the URL is a plain parameter whose origin the patch does not show), and the highest fail is `pages.js` (it follows a `next_url` from a response body). For `no_logged_credentials`, the lowest pass is `headers.js` (it logs only the header names).

## Revisions before these runs

1. In the first wording, a URL that was a plain function parameter was "not enough to fail". `pusher.go` scored only 0.51 and 0.55 on `no_unvalidated_destination`, just above 0.5. The instructions now say to answer no only when the patch shows both that the URL comes from outside the code and that nothing checks it. The true criterion now says a parameter, a struct field, or a value of unknown origin does not fail, because any check belongs to the caller. After this change, `pusher.go` scored 0.7 to 0.72. No fixture was changed.

## Limitations

- 20 fixtures, one model alias, two runs. Scores vary between runs. Recalibrate after you change the questions or the model.
- The margin on `no_unvalidated_destination` is smaller than on `no_logged_credentials`. A destination taken from a response body scored up to 0.31.
- A pass means that one patch shows no logged credential and no send to an unvalidated URL. It does not mean the project handles secrets safely. The check sees one file at a time. It cannot follow a URL or a credential across files, and a value of unknown origin passes by design.
- The check finds credentials by name and by use, not by value. It does not replace the local secret scan (`jev-check secrets`, which runs before every request). The scan finds secret values in the patch. This check finds code that leaks or misroutes credentials at run time. Neither one covers the other.
- A constant plain-http URL to a remote host passes, because the false criterion needs a URL from outside the code. It has no fixture.
