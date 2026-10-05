package main

import (
	"fmt"
	"os"

	"runlink/internal/buildinfo"
)

func main() { os.Exit(run(os.Args[1:])) }
func run(args []string) int {
	if len(args) == 0 || len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		fmt.Println("Usage: runlink [help | version]\n\nShare bounded tasks from your machine. Publishing is not implemented yet.\n  version  Print version")
		return 0
	}
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Println("runlink", buildinfo.Version)
		return 0
	}
	fmt.Fprintln(os.Stderr, "unknown command; use runlink --help")
	return 2
}
