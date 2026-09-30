// jev-check asks Jev calibrated yes/no checks about files and gates commits on them.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/luisferrassini/jev-check/internal/secretscan"
)

const usage = `Usage: jev-check <command> [args]

Commands:
  init [DIR]                set up DIR/.jev-check/ with its config and checks (default: .)
  add [--dir DIR] <check>...
                            copy bundled checks into DIR/.jev-check/input/
  list [DIR]                list DIR/.jev-check/input/questions/ and the bundled checks
  ask <check> [options]     ask one check (see jev-check ask --help)
  state [DIR]               print the project state Jev sees for DIR
  secrets PATCH...          scan patches for secrets, including removed lines
  judge OUTPUT THRESHOLD [QUESTION=THRESHOLD]...
                            judge a saved answer again, without the API
  gate [DIR]                run DIR's checks on its staged files
  eval <check> [DIR]        test a check's thresholds on DIR/.jev-check/fixtures/<check>/
  doctor [DIR]              show DIR's settings and setup problems, offline

Every file jev-check reads or writes in a project is under .jev-check/. ask,
gate, and eval run only the checks in .jev-check/input/, and read the API key,
endpoint, and model from .jev-check/.env and from nothing else (see
jev-check doctor --help).

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
		"init": initCmd, "add": addCmd, "list": listCmd, "ask": askCmd, "state": stateCmd,
		"context": func([]string, io.Writer, io.Writer) (int, error) {
			return 0, errors.New("renamed to state; run jev-check state [DIR]")
		},
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
	var found secretscan.Found
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
