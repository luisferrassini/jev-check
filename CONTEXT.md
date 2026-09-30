# jev-check

A CLI that gates staged Git changes by asking Jev questions about each file.

## Language

**Check**:
A named set of questions about one topic, such as `public-release`, kept in the project. Only these run.
_Avoid_: Test, rule

**Bundled check**:
A check that ships inside the binary as a model, copied into a project by `init` or `add`.
_Avoid_: Template, built-in check

**Gated check**:
A check listed in the config, with its threshold, so the gate runs it.
_Avoid_: Enabled check, active check

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

**Threshold**:
The lowest yes/no answer that still gets an ok verdict, set per gated check and optionally per question.

**State**:
Everything Jev reads in a request besides the questions, such as the project description, the file tree, and one file's patch.
_Avoid_: Context, project context

**Default state**:
A fixed state saved beside a check, used by `ask` when no file is given.

**Config**:
The project's file that describes the project and lists the checks the gate runs, each with its threshold.
_Avoid_: Project context, settings
