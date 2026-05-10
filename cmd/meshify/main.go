package main

import (
	"fmt"
	"io"
	"meshify/internal/cli"
	"os"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer, stderr io.Writer) error {
	return cli.Execute(args, stdout, stderr, version)
}
