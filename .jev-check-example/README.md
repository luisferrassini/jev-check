# .jev-check

This folder holds everything [jev-check](https://github.com/luisferrassini/jev-check) reads and writes in this project. `jev-check init` created it. Deleting the folder removes jev-check from the project.

## Files

| Path | What it is | In Git |
| --- | --- | --- |
| `project-context.json` | The config: `purpose`, `rules`, `folders`, `exclude`, and the `checks` the gate runs, each with a `threshold`. | yes |
| `input/questions/<check>.json` | The questions of each check. The gate sends exactly these to Jev. | yes |
| `input/states/<check>.json` | An optional default state for `jev-check ask <check>`. The gate does not use it. | yes |
| `fixtures/<check>/` | Optional pass and fail patches for `jev-check eval <check>`. | yes |
| `.env` | `TYPESAFE_API_KEY=<key>`, and optionally `JEV_CHECK_ENDPOINT` and `JEV_CHECK_MODEL`. | no |
| `output/` | Saved requests and answers, and the gate cache in `output/cache/`. | no |
| `.gitignore` | Keeps `.env` and `output/` out of Git. | yes |

jev-check runs only the checks in `input/questions/`. The binary carries copies of the bundled checks, but it uses them only as templates for `jev-check add` and `jev-check init`.

## Common tasks

Run these from the project root.

| Task | Command |
| --- | --- |
| See the checks here and the bundled ones not added yet | `jev-check list` |
| Add a bundled check | `jev-check add <check>`, then add `{ "check": "<check>", "threshold": 0.5 }` to `checks` in `project-context.json` |
| Change a check | Edit `input/questions/<check>.json`. The next gate uses it and skips the cache for it. |
| Write your own check | Create `input/questions/<name>.json` with a `questions` object, and add it to `checks`. |
| Take a newer bundled version of a check | Delete `input/questions/<check>.json` and `input/states/<check>.json`, then run `jev-check add <check>`. |
| Restore a missing file | `jev-check init`. It creates only the files that are missing. |
| Check the setup without calling the API | `jev-check doctor` |
| Gate the staged changes | `jev-check gate` |
| Test a check's thresholds on its fixtures | `jev-check eval <check>` |

Paths inside `project-context.json` (`exclude`, `skip`, `folders`, `coding_style`) are relative to the project root, not to this folder. Keep `.jev-check/` in `exclude`, so the gate does not judge these files.
