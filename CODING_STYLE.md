# Coding style

Rules for the Go code in this repository. People and the `coding-style` check read this same file.

## Checked by tools

Run these before you commit. They are not Jev's job, and the `coding-style` check does not guess whether they ran.

- Formatting: `gofmt -l .` prints nothing. Fix with `gofmt -w <file>`.
- Static checks: `go vet ./...` passes.
- Tests: `go test ./...` passes.

## Rules Jev judges

Each rule has a stable id. The `coding-style` check asks one question per id, about the added or changed lines of one patch.

### clear_names

A name says what it holds or does, in the context where a reader sees it.

- Good: `patches`, `stagedFiles`, `threshold`, `findFixtures`, `perQuestion`.
- Fine: short names whose role is clear from a few lines around them, such as `i` in a loop, `err`, `f` for a file opened just above, `w` for an `io.Writer`, `c` for the item of a short loop, and receiver names.
- A violation: a single letter or a generic word (`data`, `tmp`, `x`, `val2`, `res`, `doStuff`) for a value that lives across many lines or crosses a function boundary, where the visible code does not say what it holds.
- Do not ask for a rename to suit a taste. Flag only a name a reader cannot understand from the visible code.

### actionable_errors

An error says which operation failed and keeps the cause that helps fix it.

- Good: `fmt.Errorf("writing %s: %w", path, err)`, `errors.New("expected at most one DIR")`, `fmt.Errorf("check %s needs a threshold from 0 to 1", c.Check)`.
- Fine: a plain `return err` when the error already names the operation, such as an `os.ReadFile` error that holds the path, or when the caller adds the context.
- A violation: a message that drops the cause (`errors.New("failed")` in place of `err`), a message that hides which input was wrong (`"invalid"`), or an ignored error that decides the result (`n, _ := strconv.Atoi(arg)` on user input, `f.Close()` unchecked after a write).
- Do not ask to wrap every error.
