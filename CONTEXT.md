# jev-check

A CLI that gates staged Git changes by asking Jev questions about each file.

## Language

**Check**:
A named set of questions about one topic, such as `public-release`.
_Avoid_: Test, rule

**Question**:
One item in a check, such as `english_only`, that Jev answers for each file. Its type is yes/no (noul), choice, or score.
_Avoid_: Axis, criterion

**Criteria**:
The descriptions of what each possible answer to a question means, such as one for true and one for false.
_Avoid_: Rule

**Answer**:
The value Jev returns for one question about one file, such as a probability of `0.12`.
_Avoid_: Result, score

**Verdict**:
The ok or FAIL for one yes/no answer, found by comparing it with a threshold. Choice and score answers have no verdict.
_Avoid_: Result, status

**State**:
Everything Jev reads in a request besides the questions, such as the project description, the file tree, and one file's patch.
_Avoid_: Context, project context

**Default state**:
A fixed state saved beside a check, used by `ask` when no file is given.

**Config**:
The project's file that describes the project and lists the checks the gate runs, each with its threshold.
_Avoid_: Project context, settings
