// The secure binary exposes only the four service endpoints plus a loopback
// health/status route. It has no administration UI and no remote operator API.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GMWalletApp/epusdt/internal/securegateway"
)

func main() {
	path := flag.String("config", "/config/gateway.json", "private configuration path")
	mode := flag.String("mode", "serve", "serve, init, or health")
	flag.Parse()
	if *mode == "health" {
		client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
		resp, err := client.Get("http://127.0.0.1:8080/healthz")
		if err != nil {
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 204 {
			os.Exit(1)
		}
		return
	}
	// Print the revision before anything else can fail, so an operator reading
	// `docker logs` can tell which source snapshot is running — including on the
	// fail-closed path when the private configuration is missing. This also
	// keeps the linker from dropping the symbol that -X writes to.
	fmt.Fprintf(os.Stderr, "[build] secure gateway revision=%s\n", securegateway.BuildRevision())
	c, err := securegateway.ReadConfig(*path)
	if err != nil {
		fail(err)
	}
	if *mode != "serve" && *mode != "init" {
		fail(fmt.Errorf("unknown mode"))
	}
	if *mode == "init" {
		if err := securegateway.InitStorage(c); err != nil {
			fail(err)
		}
	}
	unlock, err := securegateway.Lock(c)
	if err != nil {
		fail(err)
	}
	defer unlock()
	close, err := securegateway.Open(c, *mode == "init")
	if err != nil {
		fail(err)
	}
	defer close()
	if *mode == "init" {
		fmt.Println("Dedicated storage initialized; payments remain disabled in the merchant.")
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := securegateway.Serve(ctx, c); err != nil {
		fail(err)
	}
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
