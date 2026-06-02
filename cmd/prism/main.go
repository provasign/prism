package main

import (
	"os"

	"github.com/provasign/prism/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
