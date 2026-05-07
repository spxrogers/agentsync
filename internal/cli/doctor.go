package cli

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/paths"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "print environment + adapter detection",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			fmt.Fprintln(w, "agentsync doctor")
			fmt.Fprintln(w, "  AGENTSYNC_HOME:", paths.AgentsyncHome(paths.OSEnv{}))
			fmt.Fprintln(w, "  Go version:    ", runtime.Version())
			fmt.Fprintln(w, "  OS / arch:     ", runtime.GOOS, runtime.GOARCH)
			fmt.Fprintln(w, "")
			fmt.Fprintln(w, "Adapter detection (M0: PATH-only)")
			for _, agent := range []struct {
				name string
				bin  string
			}{
				{"claude", "claude"},
				{"opencode", "opencode"},
				{"codex", "codex"},
				{"cursor", "cursor"},
			} {
				p, err := exec.LookPath(agent.bin)
				if err != nil {
					fmt.Fprintf(w, "  %-10s not found in PATH\n", agent.name)
					continue
				}
				fmt.Fprintf(w, "  %-10s %s\n", agent.name, p)
			}
			return nil
		},
	}
}
