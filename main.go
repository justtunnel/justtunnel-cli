package main

import (
	"os"

	"github.com/justtunnel/justtunnel-cli/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
