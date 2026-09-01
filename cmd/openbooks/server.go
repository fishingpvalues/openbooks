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
)

var openBrowser = false
var serverConfig server.Config

func init() {
	desktopCmd.AddCommand(serverCmd)

	serverCmd.Flags().StringVarP(&serverConfig.Port, "port", "p", "5228", "Set the local network port for browser mode.")
	serverCmd.Flags().IntP("rate-limit", "r", 10, "The number of seconds to wait between searches to reduce strain on IRC search servers. Minimum is 10 seconds.")
	serverCmd.Flags().BoolVar(&serverConfig.DisableBrowserDownloads, "no-browser-downloads", false, "The browser won't recieve and download eBook files, but they are still saved to the defined 'dir' path.")
	serverCmd.Flags().StringVar(&serverConfig.Basepath, "basepath", "/", `Base path where the application is accessible. For example "/openbooks/".`)
	serverCmd.Flags().BoolVarP(&openBrowser, "browser", "b", false, "Open the browser on server start.")
	serverCmd.Flags().BoolVar(&serverConfig.Persist, "persist", false, "Persist eBooks in 'dir'. Default is to delete after sending.")
	serverCmd.Flags().StringVarP(&serverConfig.DownloadDir, "dir", "d", filepath.Join(os.TempDir(), "openbooks"), "The directory where eBooks are saved when persist enabled.")
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

// applyServerEnv maps environment variables onto the server config
// for the values the CLI flags left at their defaults. A flag that
// was explicitly set on the command line always wins over the
// environment.
//
//	ENV           FLAG            DESCRIPTION
//	PORT          --port          listen port (default 5228)
//	DOWNLOAD_DIR  --dir           where eBooks are saved
//	PERSIST       --persist       keep eBooks after sending (true/false)
//	RATE_LIMIT    --rate-limit    seconds between searches (min 10)
//	USER_AGENT    --useragent     version string reported to IRC
//	BASE_PATH     --basepath      handled above, listed for completeness
func applyServerEnv(config *server.Config, cmd *cobra.Command) {
	if v, ok := os.LookupEnv("PORT"); ok && v != "" && !cmd.Flags().Changed("port") {
		config.Port = v
	}
	if v, ok := os.LookupEnv("DOWNLOAD_DIR"); ok && v != "" && !cmd.Flags().Changed("dir") {
		config.DownloadDir = v
	}
	if v, ok := os.LookupEnv("PERSIST"); ok {
		switch strings.ToLower(v) {
		case "true", "1", "yes":
			config.Persist = true
		case "false", "0", "no":
			config.Persist = false
		default:
			fmt.Fprintf(os.Stderr, "ignoring invalid PERSIST=%q (want true or false)\n", v)
		}
	}
	if v, ok := os.LookupEnv("RATE_LIMIT"); ok && v != "" && !cmd.Flags().Changed("rate-limit") {
		if n, err := strconv.Atoi(v); err == nil {
			ensureValidRate(n, config)
		} else {
			fmt.Fprintf(os.Stderr, "ignoring invalid RATE_LIMIT=%q (want an integer)\n", v)
		}
	}
	if v, ok := os.LookupEnv("USER_AGENT"); ok && v != "" && !cmd.Flags().Changed("useragent") {
		config.UserAgent = v
	}
}
