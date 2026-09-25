---
name: jev-check
description: Red/green gate for staged files, judged by Jev. Run it before you call a task done or commit, and when you add or change a jev-check question.
---

# jev-check

`./jev-check gate .` works like a test suite for the staged files. It asks Jev yes/no questions about each staged patch and goes green (exit 0) or red (exit 1). Run it from this folder. If `./jev-check` is missing or older than a `.go` file, run `go build -o jev-check .` first.

## The loop

Treat red like a failing test in TDD:

1. Stage the work: `git add -A`.
2. Run `./jev-check gate .`.
3. Green, `gate: PASS`: the task can be called done.
4. Red, `gate: FAIL`: each `FAIL <probability> <question>` under `== <check> <file>` names one problem in that file. `SECRET <file> line N` means the patch looked like it held a secret and was never sent. Fix the file and go back to step 1.
5. Exit 2 is a usage or API error, not red. stderr names the cause. Report it to the user.

Done means green. The questions and thresholds are the spec, so get to green by changing the files. When you believe a FAIL is wrong, stop and show the user the file, the question, and the probability.

## Changing a check

A check is `input/questions/<name>.json`. Its threshold is in `project-context.json`, and `fixtures/<name>/` proves the threshold.

1. Add the case that prompted the change. A problem the check missed goes in `fixtures/<name>/fail/<question>/<file>.patch`. A clean file it failed goes in `fixtures/<name>/pass/<file>.patch`. Make each patch by staging the file at its real path and running `git diff --cached --relative -- <file>`, so it matches what the gate sends.
2. Edit the question. Phrase it so yes is the good outcome, and give concrete examples in `criteria`.
3. Run `./jev-check eval <name>`. Done when it prints `eval: 0 misses` with every question's `highest-fail` below its `threshold` and its `lowest-pass` above it.
4. When no threshold separates a question's fixtures, remove the question.

Try a draft question without the gate: `./jev-check ask draft.json --file <patch>`. After changing a `.go` file, run `go test ./...` and rebuild.
