package main

import (
	"fmt"
	"os"

	launcher "athena-launcher/internal/launcher"
)

func main() {
	if err := launcher.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "athena-launcher:", err)
		os.Exit(1)
	}
}
