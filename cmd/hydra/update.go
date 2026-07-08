package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"hydra/internal/update"
)

// runUpdate replaces the running binary with the latest release.
func runUpdate() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	latest, err := update.LatestTag(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hydra: could not check for updates:", err)
		os.Exit(1)
	}
	if version != "dev" && !update.IsNewer(version, latest) {
		fmt.Printf("hydra is already up to date (%s)\n", version)
		return
	}
	fmt.Printf("Updating hydra %s → %s …\n", version, latest)
	if err := update.Apply(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "hydra:", err)
		os.Exit(1)
	}
	fmt.Printf("✓ updated to %s\n", latest)
}
