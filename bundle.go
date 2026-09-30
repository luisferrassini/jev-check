package main

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/luisferrassini/jev-check/internal/catalog"
)

// bundled holds the published checks and the README that init writes. A project never
// runs them from here: add and init copy them into the project's .jev-check/input/.
//
//go:embed .jev-check-example/input/questions/*.json .jev-check-example/input/states/*.json .jev-check-example/README.md
var embedded embed.FS

func init() { catalog.Bundled = embedded }

const addUsage = `Usage: jev-check add [--dir DIR] <check>...   (default DIR: .)
Copies bundled checks into DIR/.jev-check/input/questions/<check>.json, with
the default state, if the check has one, in DIR/.jev-check/input/states/.
ask, gate, and eval read checks only from there, so edit the copies to change
a check. A file that already exists is kept, never replaced. To take a newer
bundled version, delete the copy and run add again. jev-check list shows the
bundled checks. Add a check to .jev-check/config.json to gate on it.
`

func addCmd(args []string, stdout, _ io.Writer) (int, error) {
	dir := "."
	var names []string
	for i := 0; i < len(args); i++ {
		switch {
		case isHelp(args[i]):
			fmt.Fprint(stdout, addUsage)
			return 0, nil
		case args[i] == "--dir":
			if i+1 == len(args) {
				return 0, errors.New("--dir needs a folder")
			}
			i++
			dir = args[i]
		case strings.HasPrefix(args[i], "-"):
			return 0, fmt.Errorf("unknown option %s (see --help)", args[i])
		default:
			names = append(names, args[i])
		}
	}
	if len(names) == 0 {
		return 0, errors.New("expected at least one check name (see jev-check list)")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return 0, fmt.Errorf("%s is not a folder", dir)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return 0, err
	}
	return 0, catalog.Add(dir, names, stdout)
}
