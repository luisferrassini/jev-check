---
name: jev-check
description: Gate staged task changes on a project's Jev checks, or change those checks. Use in a project that has a .jev-check/project-context.json. It is a policy gate, not a substitute for the project's own tests.
---

# jev-check

`jev-check gate <project>` asks Jev yes/no questions about each staged file in `<project>` and prints `gate: PASS` (exit 0) or `gate: FAIL` (exit 1). Treat it like a test suite with a small retry budget. Setup and install: https://github.com/luisferrassini/jev-check#readme

## 1. Choose the executable and the project

- The executable is the path the user gave you, quoted. Otherwise it is `jev-check` on `PATH`. Run it directly, never through `eval` or `sh -c`. If `command -v jev-check` finds nothing, report that it is missing, link the setup guide above, and stop this skill.
- Run `jev-check --help` first when you are not sure which binary you have.
- Build it with `go build -o jev-check .` only when the task changes jev-check's own Go source, in its own checkout.
- `<project>` is the folder that holds `.jev-check/`, where every jev-check file lives. Pass it explicitly. The tool does not search parent folders. No `.jev-check/project-context.json` is a setup problem, and so is a `project-context.json` left at the project root by an older version: report it and link the setup guide. So is `no check "<name>": ... does not exist`, which means `.jev-check/input/questions/` lacks a check the config names: report it and suggest `jev-check init <project>`. Leave rules and thresholds to the user.

The user's approval to run the gate covers reruns inside the retry budget below.

## 2. Stage only the task's changes

The gate judges everything staged, including what the user staged before you started. Keep the user's index intact.

1. Run `git status --porcelain` and `git diff --cached`. Write down what was already staged and which paths the task changed. Note untracked files, deletions, renames, and files that are partly staged.
2. Stage each task file by its literal path: `git --literal-pathspecs add -- 'path/one' 'path two'`. This handles spaces, leading hyphens, and `*`, `?`, `[`, `:` in names. Use a whole-file add only when the whole unstaged change in that file is yours. Never use `git add -A`, `git add .`, or a glob for this gate.
3. When a task file also holds someone else's edits, stage only your hunks: copy the file header and your hunks from `git diff -- <file>` into a patch file, then run `git apply --cached <patch>`. If you cannot tell whose hunk is whose, leave that file unstaged, report the overlap, and go on with other work. If the task intentionally changes hunks that were already staged, report the overlap, and proceed only if existing authorization clearly covers that change. Keep the index as the user left it: resetting, stashing, or restoring it is out of bounds.
4. Run `git diff --cached` again. Done when the staged set is the user's earlier staged work plus exactly your task's hunks.

## 3. Run the gate and act on the result

Run `jev-check gate <project>` and keep stdout, stderr, and the exit code. Say in your report that the result covers every staged file, not only yours. Leave unrelated files alone even when they fail.

| Result | Action |
| --- | --- |
| Exit 0, `gate: PASS` | Report it next to the project's own tests and checks. A pass does not prove the code works, and excluded or skipped files were not judged. |
| Exit 0, `nothing staged` | A no-op. If your changes need checking, fix the staging and rerun. Never report a no-op as a pass. |
| Exit 1, `FAIL` | Each `FAIL <probability> <question>` under `== <check> <file>` names one problem. Report the check, file, question, probability, and threshold from `.jev-check/project-context.json`. Fix a real problem in your own change, then stage the fix again. |
| Exit 1, `SECRET` | `SECRET <location> line N looks like <kind>`: something in the request looks like a secret, and nothing was sent. It can be in a patch, a file name, the project fields, the tree, or a check's questions. Remove it from content you own. Report the location and kind, never the value. |
| Exit 2 | A usage, setup, or API error, not a judgment. stderr names the cause. Fix a known local setup problem if the task allows it, else report the blocker. |

### Retry budget

For one task scope the gate runs at most **three** times: the first run and two reruns. Each rerun needs a concrete change to code, staging, or setup since the last run. An unchanged request gets the same cached answer for 24 hours, and `--no-cache` only buys another sample of the same judgment, so rerun with a change or not at all. Retry an exit 2 only after you have fixed its cause. When the budget runs out, report the remaining findings and what you finished. More runs need a new instruction or a changed task.

### A FAIL that looks wrong

Stop the loop. Show the file, question, probability, threshold, and why the file does not have that problem. Call it a suspected false positive, not a fact. Keep the policy as it is: thresholds, questions, `exclude`, `skip`, models, endpoints (`JEV_CHECK_ENDPOINT`), and fixtures change only in a check-maintenance task the user asked for.

A pass does not authorize a commit, a reset, or work outside the task.

## Changing a check (only when asked)

A check is `.jev-check/input/questions/<name>.json` in the project; jev-check runs no other copy. Its threshold is in `.jev-check/project-context.json`, and `.jev-check/fixtures/<name>/` proves the threshold.

1. Add the case that prompted the change: a missed problem as `.jev-check/fixtures/<name>/fail/<question>/<file>.patch`, a wrongly failed clean file as `.jev-check/fixtures/<name>/pass/<file>.patch`. Make each patch in a disposable repository: stage the file at its real path there and run `git diff --cached --relative -- <file>`. That keeps unrelated staged work in the project untouched.
2. Edit the question so yes is the good outcome, with concrete examples in `criteria`.
3. Run `jev-check eval <name> <project>`. Done when it exits 0 with `eval: 0 misses`, there is at least one pass fixture, every yes/no question has a fail fixture, and for each question `highest-fail < threshold <= lowest-pass`. A pass fixture scoring exactly the threshold passes.
4. If no threshold separates a question's fixtures, report the scores and a proposed redesign. The user decides whether to reword, drop, or re-threshold it.
5. Run the project's tests and the gate (sections 2 and 3). Report any open eval or gate problem.

Try a draft question without the gate: `jev-check ask draft.json --file <patch> --dry-run`, then without `--dry-run` for a live answer.
