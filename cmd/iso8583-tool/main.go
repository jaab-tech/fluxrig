package main

import (
	"flag"
	"log"
	"os"
)

var (
	mode = flag.String("mode", "load", "Operation mode: 'load' (Generator) or 'echo' (Server)")
)

func main() {
	flag.Parse()

	switch *mode {
	case "load":
		runLoadMode()
	case "echo":
		runEchoMode()
	default:
		log.SetOutput(os.Stderr)
		log.Fatalf("Unknown mode: %s. Use 'load' or 'echo'", *mode)
	}
}
