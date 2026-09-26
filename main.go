// jev-check asks Jev calibrated yes/no checks about files and gates commits on them.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const usage = `Usage: jev-check <command> [args]

Commands:
  init [DIR]                create DIR/project-context.json (default: .)
  list [DIR]                list the bundled checks and DIR/input/questions/
  ask <check> [options]     ask one check (see jev-check ask --help)
  context [DIR]             print the project state Jev sees for DIR
  secrets PATCH...          scan patches for secrets, including removed lines
  judge OUTPUT THRESHOLD [QUESTION=THRESHOLD]...
                            judge a saved answer again, without the API
  gate [DIR]                run DIR's checks on its staged files
  eval <check> [DIR]        test a check's thresholds on DIR/fixtures/<check>/
  doctor [DIR]              show DIR's settings and setup problems, offline

ask, gate, and eval read the API key, endpoint, and model from the project's
.jev-check/.env and from nothing else (see jev-check doctor --help).

Exit codes: 0 ok, 1 a check failed or a request looked like it held a secret,
2 usage or API error. A request that looks like it holds a secret is never
sent, saved, cached, or printed.
`

type command func(args []string, stdout, stderr io.Writer) (int, error)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	commands := map[string]command{
		"init": initCmd, "list": listCmd, "ask": askCmd, "context": contextCmd,
		"secrets": secretsCmd, "judge": judgeCmd, "gate": gateCmd, "eval": evalCmd, "doctor": doctorCmd,
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if isHelp(args[0]) || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "jev-check: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	code, err := cmd(args[1:], stdout, stderr)
	var found secretsFound
	if errors.As(err, &found) {
		fmt.Fprintln(stdout, found)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "jev-check %s: %v\n", args[0], err)
		return 2
	}
	return code
}

func isHelp(arg string) bool { return arg == "-h" || arg == "--help" }

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("invalid JSON in %s: %w", path, err)
	}
	return nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// splitNUL splits git's -z output into paths.
func splitNUL(out string) []string {
	if out == "" {
		return []string{}
	}
	return strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
}
