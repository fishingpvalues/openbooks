package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/evan-buss/openbooks/server"
	"github.com/evan-buss/openbooks/util"

	"github.com/spf13/cobra"

	"time")

var openBrowser = false
var serverConfig server.Config

func init() {
	desktopCmd.AddCommand(serverCmd)

	serverCmd.Flags().StringVarP(&serverConfig.Port, "port", "p", "5228", "Set the local network port for browser mode.")
	serverCmd.Flags().IntP("rate-limit", "r", 10, "The number of seconds to wait between searches to reduce strain on IRC search servers. Minimum is 10 seconds.")
	serverCmd.Flags().BoolVar(&serverConfig.DisableBrowserDownloads, "no-browser-downloads", false, "The browser won't recieve and download eBook files, but they are still saved to the defined 'dir' path.")
	serverCmd.Flags().StringVar(&serverConfig.Basepath, "basepath", "/", `Base path where the application is accessible. For example "/openbooks/".`)
	serverCmd.Flags().BoolVarP(&openBrowser, "browser", "b", false, "Open the browser on server start.")
	serverCmd.Flags().BoolVarP(&serverConfig.Persist, "persist", "P", false, "Persist eBooks in 'dir'. Default is to delete after sending.")
	serverCmd.Flags().StringVarP(&serverConfig.DownloadDir, "dir", "d", filepath.Join(os.TempDir(), "openbooks"), "The directory where eBooks are saved when persist enabled.")
	serverCmd.Flags().StringVar(&serverConfig.Token, "token", "", "API token. Every route except the static SPA and /openapi.json requires it (also OPENBOOKS_TOKEN env). Empty = single-user mode, no auth.")
	serverCmd.Flags().StringVar(&serverConfig.BindIP, "bind", "127.0.0.1", "Interface to listen on. 127.0.0.1 by default; the docker image passes 0.0.0.0.")
}

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Run OpenBooks in server mode.",
	Long:  "Run OpenBooks in server mode. This allows you to use a web interface to search and download eBooks.",
	PreRun: func(cmd *cobra.Command, args []string) {
		bindGlobalServerFlags(&serverConfig)
		rateLimit, _ := cmd.Flags().GetInt("rate-limit")
		ensureValidRate(rateLimit, &serverConfig)
		// Environment variables fill in values the CLI flags left at
		// their defaults; an explicitly set flag always wins.
		applyServerEnv(&serverConfig, cmd)
		// If cli flag isn't set (default value) check for the presence of an
		// environment variable and use it if found.
		if serverConfig.Basepath == cmd.Flag("basepath").DefValue {
			if envPath, present := os.LookupEnv("BASE_PATH"); present {
				serverConfig.Basepath = envPath
			}
		}
		serverConfig.Basepath = sanitizePath(serverConfig.Basepath)
	},
	Run: func(cmd *cobra.Command, args []string) {
		if openBrowser {
			browserUrl := "http://127.0.0.1:" + path.Join(serverConfig.Port+serverConfig.Basepath)
			util.OpenBrowser(browserUrl)
		}

		server.Start(serverConfig)
	},
}

// applyServerEnv maps environment variables onto the server config for
// the values the CLI flags left at their defaults. A flag explicitly
// set on the command line always wins over the environment.
//
// The OPENBOOKS_* names are the stack convention (potatostack compose
// prefixes every service's vars); the short names are accepted too for
// standalone use.
//
//	ENV                    FLAG            DESCRIPTION
//	OPENBOOKS_PORT / PORT  --port          listen port (default 5228)
//	OPENBOOKS_DIR / DOWNLOAD_DIR --dir     where eBooks are saved
//	OPENBOOKS_PERSIST / PERSIST        --persist       keep eBooks after sending (true/false)
//	OPENBOOKS_RATE_LIMIT / RATE_LIMIT  --rate-limit    seconds between searches (min 10)
//	OPENBOOKS_USER_AGENT / USER_AGENT  --useragent     version string reported to IRC
func applyServerEnv(config *server.Config, cmd *cobra.Command) {
	// envValue returns the first non-empty of the given names.
	envValue := func(names ...string) (string, bool) {
		for _, n := range names {
			if v, ok := os.LookupEnv(n); ok && v != "" {
				return v, true
			}
		}
		return "", false
	}

	if v, ok := envValue("OPENBOOKS_PORT", "PORT"); ok && !cmd.Flags().Changed("port") {
		config.Port = v
	}
	if v, ok := envValue("OPENBOOKS_DIR", "DOWNLOAD_DIR"); ok && !cmd.Flags().Changed("dir") {
		config.DownloadDir = v
	}
	if v, ok := envValue("OPENBOOKS_PERSIST", "PERSIST"); ok && !cmd.Flags().Changed("persist") {
		switch strings.ToLower(v) {
		case "true", "1", "yes":
			config.Persist = true
		case "false", "0", "no":
			config.Persist = false
		default:
			fmt.Fprintf(os.Stderr, "ignoring invalid PERSIST=%q (want true or false)\n", v)
		}
	}
	if v, ok := envValue("OPENBOOKS_RATE_LIMIT", "RATE_LIMIT"); ok && !cmd.Flags().Changed("rate-limit") {
		if n, err := strconv.Atoi(v); err == nil {
			ensureValidRate(n, config)
		} else {
			fmt.Fprintf(os.Stderr, "ignoring invalid RATE_LIMIT=%q (want an integer)\n", v)
		}
	}
	if v, ok := envValue("OPENBOOKS_USER_AGENT", "USER_AGENT"); ok && !cmd.Flags().Changed("useragent") {
		config.UserAgent = v
	}
	// PotatoStack v5.2: the Wanted watchlist re-search interval. A value
	// below the 1m floor is clamped by the poller; 0 = disable the
	// poller (the REST endpoints still work, entries are not
	// re-searched automatically).
	if v, ok := envValue("OPENBOOKS_WANTED_POLL_INTERVAL", "WANTED_POLL_INTERVAL"); ok {
		if d, err := time.ParseDuration(v); err == nil {
			config.WantedPollInterval = d
		} else {
			fmt.Fprintf(os.Stderr, "ignoring invalid WANTED_POLL_INTERVAL=%q (want a Go duration)\n", v)
		}
	}
}

