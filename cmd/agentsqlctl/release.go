package main

import (
	"encoding/json"
	"github.com/cuipengdba/agentsql/internal/releaseverify"
	"github.com/spf13/cobra"
)

func newReleaseCommand() *cobra.Command {
	var directory, publicKey, version string
	command := &cobra.Command{Use: "release", Short: "Verify offline release artifacts"}
	verify := &cobra.Command{
		Use: "verify", Short: "Verify all release assets using an independently trusted public key", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			report, err := releaseverify.Verify(command.Context(), directory, publicKey, version)
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(report)
		},
	}
	verify.Flags().StringVar(&directory, "directory", "", "local, immutable directory containing exactly 15 release assets")
	verify.Flags().StringVar(&publicKey, "public-key", "", "independently trusted release public-key JSON outside the asset directory")
	verify.Flags().StringVar(&version, "version", "", "expected version, for example v4.1.0 (required; prevents accidental downgrade)")
	for _, name := range []string{"directory", "public-key", "version"} {
		_ = verify.MarkFlagRequired(name)
	}
	command.AddCommand(verify)
	return command
}
