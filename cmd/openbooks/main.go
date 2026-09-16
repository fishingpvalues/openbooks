package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/davecgh/go-spew/spew"
	"github.com/evan-buss/openbooks/desktop"
	"github.com/evan-buss/openbooks/server"
	"github.com/spf13/cobra"
)

// version will always match the GitHub release versions.
// v5.0.0: PotatoStack patched line (auth, REST API, security fixes).
// v5.1.0: integration layer - Newznab /torznab indexer endpoint, peer
// clients (Prowlarr / Audiobookshelf / Calibre-Web / Readarr), download-
// completion webhook, /api/v1/integrations overview.
// v5.1.1: Retry-After on the 429 rate limit (search + torznab), download
// request validation before the IRC session, v5 delete name policy
// aligned with the legacy handler, expanded /api/v1 test coverage.
// v5.2.0: the persistent Wanted watchlist (POST/GET/DELETE /api/v1/wanted +
// re-search poller), the Atom feed (GET /api/v1/feeds/atom), the OPDS catalog
// (GET /opds), and the unified multi-source search (POST /api/v1/search/unified)
// v5.1.2: GET /api/v1/downloads (book completions) and GET /api/v1/metrics
// (Prometheus text format); the api IRC session re-establishes itself
// after the reader detects a dead connection (previously the first search
// after a VPN bounce silently timed out and every later search hit the
// dead conn); /api/v1/health reports ircConnected.
var version = "5.4.4"

// We only increment ircVersion when irc admins require a fix to be made.
// They can block / permit certain version numbers. ircVersion is the current permitted
// version number.
var ircVersion = "4.3.0"

type GlobalFlags struct {
	UserName  string
	Server    string
	Log       bool
	SearchBot string
	EnableTLS bool
	UserAgent string
}

var debug bool
var globalFlags GlobalFlags
var desktopConfig server.Config

func init() {
	desktopCmd.PersistentFlags().BoolVar(&debug, "debug", false, "Enable debug mode.")
	desktopCmd.PersistentFlags().StringVarP(&globalFlags.UserName, "name", "n", "", "Username used to connect to IRC server.")
	desktopCmd.MarkPersistentFlagRequired("name")
	desktopCmd.PersistentFlags().StringVarP(&globalFlags.Server, "server", "s", "irc.irchighway.net:6697", "IRC server to connect to.")
	desktopCmd.PersistentFlags().BoolVar(&globalFlags.EnableTLS, "tls", true, "Connect to server using TLS.")
	desktopCmd.PersistentFlags().BoolVarP(&globalFlags.Log, "log", "l", false, "Save raw IRC logs for each client connection.")
	desktopCmd.PersistentFlags().StringVar(&globalFlags.SearchBot, "searchbot", "search", "The IRC bot that handles search queries. Try 'searchook' if 'search' is down.")
	desktopCmd.PersistentFlags().StringVarP(&globalFlags.UserAgent, "useragent", "u", fmt.Sprintf("OpenBooks %s", ircVersion), "UserAgent / Version Reported to IRC Server.")

	homeDir, err := os.UserHomeDir()
	if err != nil {
		panic(fmt.Errorf("unable to determine $HOME directory %w", err))
	}
	downloadDir := filepath.Join(homeDir, "Downloads")

	desktopCmd.Flags().StringVarP(&desktopConfig.Port, "port", "p", "5228", "Set the local network port for browser mode.")
	desktopCmd.Flags().IntP("rate-limit", "r", 10, "The number of seconds to wait between searches to reduce strain on IRC search servers. Minimum is 10 seconds.")
	desktopCmd.Flags().StringVarP(&desktopConfig.DownloadDir, "dir", "d", downloadDir, "The directory where eBooks are saved.")
}

var desktopCmd = &cobra.Command{
	Use:   "openbooks",
	Short: "Quickly and easily download eBooks from IRCHighway.",
	Long:  "Runs OpenBooks in desktop mode. This allows you to run OpenBooks like a regular desktop application. This functionality utilizes your OS's native browser renderer and as such may not work on certain operating systems.",
	PreRun: func(cmd *cobra.Command, args []string) {
		bindGlobalServerFlags(&desktopConfig)
		rateLimit, _ := cmd.Flags().GetInt("rate-limit")
		ensureValidRate(rateLimit, &desktopConfig)
		desktopConfig.DisableBrowserDownloads = true
		desktopConfig.Basepath = "/"
		desktopConfig.Persist = true
	},
	Run: func(cmd *cobra.Command, args []string) {
		if debug {
			spew.Dump(desktopConfig)
		}

		go server.Start(desktopConfig)
		desktop.StartWebView(fmt.Sprintf("http://127.0.0.1:%s", path.Join(desktopConfig.Port+desktopConfig.Basepath)), debug)
	},
	Version: version,
}

func main() {
	// Don't block if launched from explorer.
	cobra.MousetrapHelpText = ""

	// PotatoStack v5.4.2: the container healthcheck is routed around the
	// client/server command tree on purpose. desktopCmd marks --name (the IRC
	// nick) as a required persistent flag, so a subcommand registered there
	// fails with `required flag(s) "name" not set` before it runs - the first
	// build of this probe did exactly that (docker health = starting, failing
	// streak 2). A liveness probe must not need an IRC identity.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		healthcheckCmd.SetArgs(os.Args[2:])
		if err := healthcheckCmd.Execute(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if err := desktopCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
