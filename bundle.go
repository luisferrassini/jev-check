package main

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// bundleDir is where this repository keeps the published checks. It is tracked,
// unlike the repository's own .jev-check/, so a clone can build the bundle.
const bundleDir = ".jev-check-example"

// bundled holds the published checks and the README that init writes. A project never
// runs them from here: add and init copy them into the project's .jev-check/input/.
//
//go:embed .jev-check-example/input/questions/*.json .jev-check-example/input/states/*.json .jev-check-example/README.md
var bundled embed.FS

func bundledPath(kind, name string) string {
	return bundleDir + "/input/" + kind + "/" + name + ".json"
}

// inputDir returns dir/.jev-check/input/<kind>, the folder that holds a project's checks or states.
func inputDir(dir, kind string) string {
	return filepath.Join(dir, jevDir, "input", kind)
}

// inputPath returns dir/.jev-check/input/<kind>/<name>.json.
func inputPath(dir, kind, name string) string {
	return filepath.Join(inputDir(dir, kind), name+".json")
}

// bundledNames returns the names of the bundled checks, sorted.
func bundledNames() []string {
	files, _ := fs.Glob(bundled, bundleDir+"/input/questions/*.json")
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = strings.TrimSuffix(path.Base(f), ".json")
	}
	slices.Sort(names)
	return names
}

func isBundled(name string) bool { return slices.Contains(bundledNames(), name) }

// missingCheck explains a named check that project/.jev-check/input/ does not hold.
func missingCheck(project, name string) error {
	file := inputPath(project, "questions", name)
	if !isBundled(name) {
		return fmt.Errorf("no check %q: %s does not exist (run jev-check list)", name, file)
	}
	add := "jev-check add " + name
	if cwd, _ := filepath.Abs("."); cwd != project {
		add = "jev-check add --dir " + shellQuote(project) + " " + name
	}
	return fmt.Errorf("no check %q: %s does not exist; copy the bundled one with: %s", name, file, add)
}

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
	return 0, addChecks(dir, names, stdout)
}

// addChecks copies the named bundled checks into dir/.jev-check/input/, keeping any file already there.
// It checks every name before it writes anything.
func addChecks(dir string, names []string, stdout io.Writer) error {
	for _, name := range names {
		if !checkName.MatchString(name) {
			return fmt.Errorf("%q: check names use only letters, digits, - and _", name)
		}
		if !isBundled(name) {
			return fmt.Errorf("no bundled check %q (run jev-check list)", name)
		}
	}
	for _, name := range names {
		for _, kind := range []string{"questions", "states"} {
			data, err := bundled.ReadFile(bundledPath(kind, name))
			if errors.Is(err, fs.ErrNotExist) && kind == "states" {
				continue
			} else if err != nil {
				return err
			}
			if err := installFile(inputPath(dir, kind, name), string(data), stdout); err != nil {
				return err
			}
		}
	}
	return nil
}

// installFile creates path with content and reports it, or reports that an existing file was kept.
func installFile(path, content string, stdout io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	err := createFile(path, content)
	if errors.Is(err, fs.ErrExist) {
		fmt.Fprintf(stdout, "kept    %s\n", path)
		return nil
	}
	if err == nil {
		fmt.Fprintf(stdout, "created %s\n", path)
	}
	return err
}
