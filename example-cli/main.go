package main

import (
	"github.com/orq-ai/bartolo/cli"
)

func main() {
	cli.Init(&cli.Config{
		AppName:   "example",
		EnvPrefix: "EXAMPLE",
		Version:   "1.0.0",
	})

	registerGeneratedCommands()

	cli.Root.Execute()
}
