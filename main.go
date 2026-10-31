package main

import (
	"fmt"
	"os"

	"github.com/yuyj109/notion-sync/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", cmd.FormatError(err, os.Stderr))
		os.Exit(1)
	}
}
