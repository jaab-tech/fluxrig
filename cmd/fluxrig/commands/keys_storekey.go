// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/utils/path"
)

// keysStoreKeyCmd prints the store key a cluster key gives.
var keysStoreKeyCmd = &cobra.Command{
	Use:   "store-key [cluster-key-path]",
	Short: "Print the message store key derived from a cluster key",
	Long: `Prints the key that encrypts the Mixer's message store when snake.store_key_file is
not set. Save it to a file with mode 0600 and name that file in snake.store_old_key_file
before you replace the cluster key: the store is unreadable without it.

The output is the key and a newline, nothing else, so it can be redirected to a file.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		safePath, err := path.Sanitize(args[0])
		if err != nil {
			return fmt.Errorf("invalid path: %w", err)
		}
		cluster, err := pki.LoadClusterKey(safePath)
		if err != nil {
			return fmt.Errorf("failed to load cluster key: %w", err)
		}
		key, err := cluster.DeriveStoreKey()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), key)
		return err
	},
}

func init() {
	keysCmd.AddCommand(keysStoreKeyCmd)
}
