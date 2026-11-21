//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"pentest/internal/commentscan"
)

type entry struct {
	File string `json:"file"`
	commentscan.Comment
}

func main() {
	paths := os.Args[1:]
	if len(paths) == 0 {
		out, err := exec.Command("git", "ls-files", "--", "*.go").Output()
		if err != nil {
			fmt.Fprintln(os.Stderr, "git ls-files:", err)
			os.Exit(2)
		}
		paths = strings.Fields(string(out))
	}
	enc := json.NewEncoder(os.Stdout)
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		comments, err := commentscan.ScanGo(path, src)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		for _, c := range comments {
			if err := enc.Encode(entry{File: path, Comment: c}); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
		}
	}
}
