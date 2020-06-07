package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/RoLex/go-dcpp/cmd/dctor/cmd"
)

func main() {
	flag.Parse()
	if err := cmd.Root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
