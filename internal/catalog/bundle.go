package catalog

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

// BundleDir is where this repository keeps the published checks. It is tracked,
// unlike the repository's own .jev-check/, so a clone can build the bundle.
const BundleDir = ".jev-check-example"

// Bundled holds the published checks as an fs.FS. The main package sets it in init,
// because go:embed only reads the package's own folder. It is nil until then.
var Bundled fs.FS

func BundledPath(kind, name string) string {
	return BundleDir + "/input/" + kind + "/" + name + ".json"
}

// InputDir returns dir/.jev-check/input/<kind>, the folder that holds a project's checks or states.
func InputDir(dir, kind string) string {
	return filepath.Join(dir, workspace.Dir, "input", kind)
}

// InputPath returns dir/.jev-check/input/<kind>/<name>.json.
func InputPath(dir, kind, name string) string {
	return filepath.Join(InputDir(dir, kind), name+".json")
}

// BundledNames returns the names of the bundled checks, sorted.
func BundledNames() []string {
	files, _ := fs.Glob(Bundled, BundleDir+"/input/questions/*.json")
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = strings.TrimSuffix(path.Base(f), ".json")
	}
	slices.Sort(names)
	return names
}

func IsBundled(name string) bool { return slices.Contains(BundledNames(), name) }

// missingCheck explains a named check that project/.jev-check/input/ does not hold.
func missingCheck(project, name string) error {
	file := InputPath(project, "questions", name)
	if !IsBundled(name) {
		return fmt.Errorf("no check %q: %s does not exist (run jev-check list)", name, file)
	}
	add := "jev-check add " + name
	if cwd, _ := filepath.Abs("."); cwd != project {
		add = "jev-check add --dir " + fsutil.ShellQuote(project) + " " + name
	}
	return fmt.Errorf("no check %q: %s does not exist; copy the bundled one with: %s", name, file, add)
}

// Add copies the named bundled checks into dir/.jev-check/input/, keeping any file already there.
// It checks every name before it writes anything.
func Add(dir string, names []string, stdout io.Writer) error {
	for _, name := range names {
		if !CheckName.MatchString(name) {
			return fmt.Errorf("%q: check names use only letters, digits, - and _", name)
		}
		if !IsBundled(name) {
			return fmt.Errorf("no bundled check %q (run jev-check list)", name)
		}
	}
	for _, name := range names {
		for _, kind := range []string{"questions", "states"} {
			data, err := fs.ReadFile(Bundled, BundledPath(kind, name))
			if errors.Is(err, fs.ErrNotExist) && kind == "states" {
				continue
			} else if err != nil {
				return err
			}
			if err := fsutil.InstallFile(InputPath(dir, kind, name), string(data), stdout); err != nil {
				return err
			}
		}
	}
	return nil
}
