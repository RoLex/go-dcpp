package main

import (
	"os"

	"github.com/RoLex/go-dcpp/cmd/go-hub/cmd"
)

func main() {
	if err := cmd.Root.Execute(); err != nil {
		os.Exit(1)
	}
}
