package cli

import (
	"fmt"
	"io"

	"github.com/jpequegn/release-observability-sentinel/internal/version"
)

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printHelp(stdout)
		return 0
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version.Current)
		return 0
	case "help", "-h", "--help":
		printHelp(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		printHelp(stderr)
		return 2
	}
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, "release-observability-sentinel")
	fmt.Fprintln(w, "usage: sentinel <command>")
	fmt.Fprintln(w, "commands: version, help")
}
