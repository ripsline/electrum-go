package main

import (
    "context"
    "log"
)

// haltForOperator prints actionable recovery instructions for an
// ErrPrunedGap and parks the goroutine until ctx is cancelled.
// The Electrum listener must be stopped before calling this so no
// clients can connect to a server that cannot serve.
func haltForOperator(ctx context.Context, err error, dbPath string) {
    log.Println()
    log.Println("❌ " + err.Error())
    log.Println()
    log.Println("   The index database has fallen behind bitcoind's prune window.")
    log.Println("   Blocks in this range have been pruned and cannot be indexed.")
    log.Println("   The Electrum server will NOT accept client connections.")
    log.Println()
    log.Printf("   To fix: delete the index database and restart the service.")
    log.Printf("   The server will re-anchor at the current chain tip.")
    log.Println()
    log.Println("     sudo systemctl stop electrum-go")
    log.Printf("     sudo rm -rf %s", dbPath)
    log.Println("     sudo systemctl start electrum-go")
    log.Println()
    log.Println("⏸️  Waiting for operator intervention (send SIGTERM to stop)...")

    <-ctx.Done()
}
