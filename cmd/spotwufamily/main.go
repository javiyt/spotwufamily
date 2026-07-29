package main

import (
	"os"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
