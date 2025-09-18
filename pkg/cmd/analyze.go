//go:build analyze
// +build analyze

package cmd

import (
	"errors"
	"fmt"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
)

var analyzeCmd = &cobra.Command{
	Use:   "analyze [snapshot.tar.gz]",
	Short: "Analyze a captured snapshot using AI or local heuristics",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := args[0]

		serviceType, _ := cmd.Flags().GetString("service-type")
		useAI, _ := cmd.Flags().GetBool("ai")
		apiKey := os.Getenv("OPENAI_API_KEY")

		fmt.Printf("🔍 Analyzing snapshot: %s\n", path)
		if useAI && apiKey == "" {
			return errors.New("AI analysis requested but OPENAI_API_KEY is not set")
		}

		tempDir, err := os.MkdirTemp("", "xdsnap-analysis")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tempDir)

		if err := extractTarGz(path, tempDir); err != nil {
			return fmt.Errorf("failed to extract snapshot: %w", err)
		}

		logPath := filepath.Join(tempDir, "consul-dataplane-logs.txt")
		logs, err := os.ReadFile(logPath)
		if err != nil {
			return fmt.Errorf("failed to read logs: %w", err)
		}

		prompt := buildPrompt(string(logs), serviceType)
		if useAI {
			resp, err := callOpenAI(prompt, apiKey)
			if err != nil {
				return err
			}
			fmt.Println("\n🤖 AI Summary:\n" + resp)
		} else {
			fmt.Println("🧠 Local analysis not yet implemented. Use --ai for OpenAI-based insight.")
		}

		return nil
	},
}
