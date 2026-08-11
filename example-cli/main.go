package main

import (
	"os"

	"github.com/orq-ai/bartolo/cli"
)

func main() {
	cli.Init(&cli.Config{
		AppName:   "example",
		EnvPrefix: "EXAMPLE",
		Version:   "1.0.0",
	})

	registerGeneratedCommands()

	os.Exit(cli.Execute())
}
