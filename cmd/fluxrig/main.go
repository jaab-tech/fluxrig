package main

import (
	"fmt"
	"os"

	"github.com/jaab-tech/fluxrig/cmd/fluxrig/commands"
)

func main() {
	if err := commands.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
