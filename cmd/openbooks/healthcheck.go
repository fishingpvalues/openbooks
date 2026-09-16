package main

// PotatoStack v5.4.2: `openbooks healthcheck` - the probe the container's
// compose healthcheck runs. The runtime image is distroless, so there is no
// shell to curl with; this binary is the only executable inside the container.
//
// This command is deliberately NOT registered on desktopCmd. That command tree
// marks --name (the IRC nick) as a required PERSISTENT flag, so any subcommand
// added to it fails with `required flag(s) "name" not set` before it runs -
// measured on the first container build (health status `starting`, failing
// streak 2). A liveness probe must not depend on an IRC identity, so main()
// routes `openbooks healthcheck ...` here before it ever reaches the client
// tree. See cmd/openbooks/main.go.
//
// The exit code is the contract: 0 = the HTTP endpoint answered 200,
// 1 = it did not (the reason goes to stderr and into `docker inspect
// .State.Health`). See server/healthcheck.go for why ircConnected is
// deliberately not part of the decision.

import (
	"fmt"
	"os"
	"time"

	"github.com/evan-buss/openbooks/server"
	"github.com/spf13/cobra"
)

var healthcheckConfig struct {
	URL     string
	Token   string
	Timeout time.Duration
}

func init() {
	healthcheckCmd.Flags().StringVar(&healthcheckConfig.URL, "url", "",
		"Health endpoint to probe. Default: http://127.0.0.1:<port>/api/v1/health.")
	healthcheckCmd.Flags().StringVar(&healthcheckConfig.Token, "token", "",
		"API token. Defaults to $OPENBOOKS_TOKEN.")
	healthcheckCmd.Flags().DurationVar(&healthcheckConfig.Timeout, "timeout", server.HealthProbeTimeout,
		"Probe budget.")
}

var healthcheckCmd = &cobra.Command{
	Use:   "healthcheck",
	Short: "Probe the local HTTP endpoint; exit 0 only when it answers 200.",
	Long: `Probe the local HTTP endpoint and exit non-zero when it does not answer 200.

This is what the container healthcheck calls: the runtime image is distroless,
so this binary is the only executable available inside it. The token comes from
--token or OPENBOOKS_TOKEN, like the server.

Only HTTP liveness is checked, never ircConnected: the IRC session is
established lazily and rebuilt after a reconnect, so restarting on it would
drop a working session. Session health is alerted on separately.`,
	// No usage dump and no duplicate "Error:" line in the health log on a
	// failed probe - the single stderr line main() prints is the whole
	// message, and docker records it in .State.Health.
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		url := healthcheckConfig.URL
		if url == "" {
			// serverConfig.Port is only filled in when the server subcommand
			// parses its flags, so resolve the default here rather than in the
			// flag definition (package init order would leave it empty).
			port := serverConfig.Port
			if port == "" {
				port = "5228"
			}
			url = fmt.Sprintf("http://127.0.0.1:%s/api/v1/health", port)
		}

		token := healthcheckConfig.Token
		if token == "" {
			token = os.Getenv("OPENBOOKS_TOKEN")
		}

		if err := server.ProbeHealth(url, token, healthcheckConfig.Timeout); err != nil {
			return err
		}
		fmt.Printf("ok: %s\n", url)
		return nil
	},
}
