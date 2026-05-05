// Command messenger runs the udisend messenger client. The CLI is
// cobra-based:
//
//	udisend run                      — start the messenger
//	udisend public enable [addr]     — switch to public mode
//	udisend public disable           — back to loopback
//	udisend public reset             — wipe auth + profile
//	udisend status                   — print state snapshot
//
// All flags are optional; storage defaults to the XDG config directory.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:           "udisend",
		Short:         "Decentralised P2P messenger",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// Persistent --storage flag inherited by every subcommand.
	root.PersistentFlags().String("storage", defaultStorageDir(), "config directory")

	root.AddCommand(
		newRunCommand(),
		newPublicCommand(),
		newStatusCommand(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
