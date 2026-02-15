package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/manager/cas"
	"github.com/spf13/cobra"
)

var specCmd = &cobra.Command{
	Use:   "spec",
	Short: "Manage ISO8583/Bento specifications",
}

var specImportCmd = &cobra.Command{
	Use:   "import <file>",
	Short: "Import a local spec file into the store",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		file := args[0]
		name, _ := cmd.Flags().GetString("name")
		tag, _ := cmd.Flags().GetString("tag")

		storePath, _ := cmd.Flags().GetString("store-dir")
		if storePath == "" {
			home, _ := os.UserHomeDir()
			storePath = filepath.Join(home, ".fluxrig", "store")
		}

		mgr, err := manager.NewManager(storePath)
		if err != nil {
			return err
		}

		hash, finalName, finalTag, err := mgr.Import(context.Background(), file, name, tag)
		if err != nil {
			return err
		}

		fmt.Printf("Imported %s -> %s\n", cas.Ref(finalName, finalTag, hash), hash)
		return nil
	},
}

var specListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all stored specs",
	RunE: func(cmd *cobra.Command, args []string) error {
		storePath, _ := cmd.Flags().GetString("store-dir")
		if storePath == "" {
			home, _ := os.UserHomeDir()
			storePath = filepath.Join(home, ".fluxrig", "store")
		}

		mgr, err := manager.NewManager(storePath)
		if err != nil {
			return err
		}

		list, err := mgr.List(context.Background())
		if err != nil {
			return err
		}

		fmt.Println("NAME\tTAG\tHASH")
		for _, a := range list {
			fmt.Printf("%s\t%s\t%s\n", a.Name, a.Tag, a.Hash)
		}
		return nil
	},
}

var specExportCmd = &cobra.Command{
	Use:   "export <name:tag> <output-file>",
	Short: "Export a CAS spec to a local file",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		urn := args[0]
		outputPath := args[1]

		storePath, _ := cmd.Flags().GetString("store-dir")
		if storePath == "" {
			home, _ := os.UserHomeDir()
			storePath = filepath.Join(home, ".fluxrig", "store")
		}

		mgr, err := manager.NewManager(storePath)
		if err != nil {
			return err
		}

		if err := mgr.Export(context.Background(), urn, outputPath); err != nil {
			return err
		}

		fmt.Printf("Exported %s -> %s\n", urn, outputPath)
		return nil
	},
}

func init() {
	// Persistent flag inherited by all subcommands
	specCmd.PersistentFlags().String("store-dir", "", "CAS store directory (default: ~/.fluxrig/store)")

	specImportCmd.Flags().String("name", "", "Logical name of the spec")
	specImportCmd.Flags().String("tag", "", "Version tag (e.g. v1.0.0)")

	specCmd.AddCommand(specImportCmd)
	specCmd.AddCommand(specListCmd)
	specCmd.AddCommand(specExportCmd)

	rootCmd.AddCommand(specCmd)
}

func GetSpecCmd() *cobra.Command {
	return specCmd
}
