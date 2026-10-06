// Command runlink is the owner CLI. Publishing is not implemented yet.
package main

import (
	"fmt"
	"os"

	"runlink/internal/buildinfo"
)

const usage = `Usage: runlink [help | version]

Share bounded tasks from your machine. Publishing is not implemented yet.
  version  Print version
`

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		args = []string{"help"}
	}
	if len(args) == 1 {
		switch args[0] {
		case "help", "--help", "-h":
			fmt.Print(usage)
			return 0
		case "version", "--version":
			fmt.Println("runlink", buildinfo.Version)
			return 0
		}
	}
	fmt.Fprintln(os.Stderr, "unknown command; use runlink --help")
	return 2
}
