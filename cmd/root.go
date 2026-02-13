package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	zlogger "github.com/zarf-dev/zarf/src/pkg/logger"
)

var version = "dev"
var (
	logLevelCLI      string
	logFormatCLI     string
	isColorDisabled  bool
	isVerboseEnabled bool
)

func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "keel",
		Short:         "Generate UDS/Zarf dist artifacts from a Dockerfile",
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if logLevelCLI == "" {
				logLevelCLI = "info"
			}
			level, err := zlogger.ParseLevel(logLevelCLI)
			if err != nil {
				return err
			}
			cfg := zlogger.Config{
				Level:       level,
				Format:      zlogger.Format(logFormatCLI),
				Destination: zlogger.DestinationDefault,
				Color:       zlogger.Color(!isColorDisabled),
			}
			l, err := zlogger.New(cfg)
			if err != nil {
				return err
			}
			zlogger.SetDefault(l)
			cmd.SetContext(zlogger.WithContext(cmd.Context(), l))
			l.Debug("logger initialized", "level", level.String(), "format", logFormatCLI, "color", !isColorDisabled)
			return nil
		},
	}

	rootCmd.Version = version
	rootCmd.PersistentFlags().StringVarP(&logLevelCLI, "log-level", "l", "info", "Set log level: debug, info, warn, error")
	rootCmd.PersistentFlags().StringVar(&logFormatCLI, "log-format", "console", "Set log format: console, json, dev")
	rootCmd.PersistentFlags().BoolVar(&isColorDisabled, "no-color", false, "Disable terminal color output")
	rootCmd.PersistentFlags().BoolVarP(&isVerboseEnabled, "verbose", "v", false, "Enable verbose build logging (equivalent to --log-level=debug for build output)")
	rootCmd.AddCommand(newGenCmd())
	rootCmd.AddCommand(newVersionCmd())

	return rootCmd
}

func Execute(ctx context.Context) int {
	cmd, err := NewRootCmd().ExecuteContextC(ctx)
	if err == nil {
		return 0
	}
	// If logger setup failed before command execution, fallback to stderr.
	if zlogger.Default() == nil {
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	zlogger.Default().Error(err.Error())
	_ = cmd
	return 1
}
