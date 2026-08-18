package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairingv2"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	discovery, err := pairingv2.NewDiscovery(pairingv2.DiscoveryConfig{
		Source: pairingv2.NewMDNSSource(),
	})
	if err != nil {
		fail(err)
	}
	candidates, err := discovery.Scan(ctx)
	if err != nil {
		fail(err)
	}
	if len(candidates) != 1 {
		fail(fmt.Errorf("expected exactly one Pairing v2 Deck, found %d", len(candidates)))
	}
	fmt.Printf("Discovered %s\n", candidates[0].Label)
	fmt.Print("Enter the six-digit code shown on Deck: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		fail(fmt.Errorf("read pairing code: %w", err))
	}
	code := []byte(strings.TrimSpace(line))
	defer clear(code)
	selection, err := discovery.Resolve(candidates[0].Reference)
	if err != nil {
		fail(err)
	}
	proofClient := pairingv2.NewProofClient(nil)
	var proofError error
	for _, route := range selection.Routes {
		result, routeErr := proofClient.Prove(ctx, route, code)
		if routeErr == nil {
			fmt.Printf("Security2 proof verified in %s\n", result.Elapsed.Round(time.Millisecond))
			return
		}
		proofError = routeErr
	}
	fail(fmt.Errorf("all Pairing v2 routes failed: %w", proofError))
}

func clear(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
