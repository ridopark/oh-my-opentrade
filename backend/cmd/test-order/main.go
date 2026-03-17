package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/ibkr"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

func main() {
	log := zerolog.New(os.Stdout).With().Timestamp().Logger()

	cfg := config.IBKRConfig{
		Host:      "localhost",
		Port:      4002,
		ClientID:  99,
		PaperMode: true,
	}

	adapter, err := ibkr.NewAdapter(cfg, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect failed: %v\n", err)
		os.Exit(1)
	}
	defer adapter.Close()

	ctx := context.Background()

	fmt.Println("\n=== TEST 1: BUY 5 SOXL @ $55.50 (whole shares) ===")
	id1, err := adapter.SubmitOrder(ctx, domain.OrderIntent{
		Symbol:     "SOXL",
		Direction:  domain.DirectionLong,
		Quantity:   5,
		LimitPrice: 55.50,
		OrderType:  "limit",
	})
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
	} else {
		fmt.Printf("Order placed: ID=%s\n", id1)
	}

	time.Sleep(3 * time.Second)

	fmt.Println("\n=== TEST 2: BUY 1.5 AAPL @ $255.00 (fractional → cashQty) ===")
	id2, err := adapter.SubmitOrder(ctx, domain.OrderIntent{
		Symbol:     "AAPL",
		Direction:  domain.DirectionLong,
		Quantity:   1.5,
		LimitPrice: 255.00,
		OrderType:  "limit",
	})
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
	} else {
		fmt.Printf("Order placed: ID=%s\n", id2)
	}

	time.Sleep(5 * time.Second)

	fmt.Println("\n=== Checking statuses ===")
	for _, id := range []string{id1, id2} {
		if id == "" {
			continue
		}
		status, serr := adapter.GetOrderStatus(ctx, id)
		fmt.Printf("Order %s: status=%s err=%v\n", id, status, serr)
	}

	fmt.Println("\n=== Cancelling test orders ===")
	for _, id := range []string{id1, id2} {
		if id == "" {
			continue
		}
		cerr := adapter.CancelOrder(ctx, id)
		fmt.Printf("Cancel %s: err=%v\n", id, cerr)
	}
}
