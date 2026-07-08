package main

import (
	"flag"
	"fmt"
	"os"

	"hydra/internal/serve"
)

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7717", "listen address (0.0.0.0:7717 for phone access on your network)")
	jobs := fs.Int("jobs", 3, "concurrent headless task workers")
	fs.Parse(args)
	if err := serve.Run(*addr, *jobs); err != nil {
		fmt.Fprintln(os.Stderr, "hydra:", err)
		os.Exit(1)
	}
}
