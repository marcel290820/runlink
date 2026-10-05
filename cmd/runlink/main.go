package main

import (
	"fmt"
	"os"

	"runlink/internal/buildinfo"
	"runlink/internal/frontend"
)

func main() { os.Exit(run(os.Args[1:])) }
func run(args []string) int {
	if len(args) == 0 || len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		fmt.Println("Usage: runlink [help | version | assets-check]\n\nShare bounded tasks from your machine. Publishing is not implemented yet.\n  version       Print version\n  assets-check  Verify the embedded GUI and loader manifest")
		return 0
	}
	if len(args) == 1 {
		switch args[0] {
		case "version", "--version":
			fmt.Println("runlink", buildinfo.Version)
			return 0
		case "assets-check":
			if err := frontend.VerifyAssets(); err != nil {
				fmt.Fprintln(os.Stderr, "embedded asset verification failed")
				return 1
			}
			fmt.Println("embedded assets verified")
			return 0
		}
	}
	fmt.Fprintln(os.Stderr, "unknown command; use runlink --help")
	return 2
}
