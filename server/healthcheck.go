package server

// PotatoStack v5.4.2: the container healthcheck probe.
//
// The runtime image is distroless (no shell, no curl, no wget), so a
// `healthcheck:` in compose has nothing to run except this binary -
// `openbooks healthcheck` is that probe. It GETs the local health endpoint
// with the configured token and exits 0 only on HTTP 200.
//
// What it deliberately does NOT check: ircConnected. The api IRC session is
// established lazily on the first search and re-established after the reader
// sees a dead connection, so it is legitimately false right after a restart
// and briefly false after a VPN bounce. Failing the probe on it would make the
// stack's autoheal restart a healthy container - and drop the very session it
// was about to rebuild. Session health is an ALERTING concern instead:
// openbooks_irc_connected is exported to the VictoriaMetrics side (see
// scripts/openbooks/openbooks-metrics.sh and the OpenbooksIrcSessionDown rule).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HealthProbeTimeout is the default budget for ProbeHealth.
const HealthProbeTimeout = 5 * time.Second

// ProbeHealth GETs rawURL (normally /api/v1/health) and returns nil only when
// the server answers 200. token may be empty (single-user mode).
func ProbeHealth(rawURL, token string, timeout time.Duration) error {
	if rawURL == "" {
		return errors.New("healthcheck: no probe URL configured")
	}
	if timeout <= 0 {
		timeout = HealthProbeTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	if token != "" {
		// Same credential form the API accepts; a tokenless server ignores it.
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	// Drain so the connection can be reused if the probe is ever put on a loop.
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %s answered HTTP %d", rawURL, resp.StatusCode)
	}
	return nil
}
