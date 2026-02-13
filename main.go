package main

import (
	"context"
	"os"

	"github.com/willswire/keel/cmd"
)

func main() {
	os.Exit(cmd.Execute(context.Background()))
}
