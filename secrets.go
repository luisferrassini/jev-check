package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/luisferrassini/jev-check/internal/secretscan"
)

func secretsCmd(args []string, stdout, _ io.Writer) (int, error) {
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Fprintln(stdout, "Usage: jev-check secrets PATCH...   exit 0 clean, 1 looks like a secret, 2 error")
		return 0, nil
	}
	if len(args) == 0 {
		return 0, errors.New("expected at least one PATCH")
	}
	found := 0
	for _, path := range args {
		patch, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		for _, report := range secretscan.ScanSecrets(path, string(patch)) {
			fmt.Fprintln(stdout, report)
			found = 1
		}
	}
	return found, nil
}
