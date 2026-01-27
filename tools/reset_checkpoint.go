// reset_checkpoint.go is a utility tool for managing the server's chain state.
//
// This tool allows you to:
// 1. Reset the checkpoint to a specific height (reindex from that point)
// 2. Delete the checkpoint entirely (full reindex from start height)
// 3. View the current checkpoint status
//
// IMPORTANT: Only run this tool while the server is STOPPED.
// Running it while the server is active will cause data corruption.
//
// Usage:
//   go run tools/reset_checkpoint.go -db ./data/index.db -height 50000
//   go run tools/reset_checkpoint.go -db ./data/index.db -height -1  (delete checkpoint)
//   go run tools/reset_checkpoint.go -db ./data/index.db -status     (view status)

package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "log"
    "os"

    "github.com/cockroachdb/pebble"
)

// Checkpoint matches the structure in internal/storage/storage.go
type Checkpoint struct {
    Height    int32  `json:"height"`
    BlockHash string `json:"block_hash"`
}

func main() {
    // Parse command-line flags
    dbPath := flag.String("db", "./data/index.db", "Path to Pebble database")
    height := flag.Int("height", -9999, "Height to reset to (-1 = delete checkpoint)")
    status := flag.Bool("status", false, "Show current checkpoint status and exit")
    force := flag.Bool("force", false, "Skip confirmation prompt")

    flag.Usage = func() {
        fmt.Fprintf(os.Stderr, "Electrum-Go Checkpoint Reset Tool\n\n")
        fmt.Fprintf(os.Stderr, "Usage: %s [options]\n\n", os.Args[0])
        fmt.Fprintf(os.Stderr, "Options:\n")
        flag.PrintDefaults()
        fmt.Fprintf(os.Stderr, "\nExamples:\n")
        fmt.Fprintf(os.Stderr, "  View status:     %s -db ./data/index.db -status\n", os.Args[0])
        fmt.Fprintf(os.Stderr, "  Reset to height: %s -db ./data/index.db -height 50000\n", os.Args[0])
        fmt.Fprintf(os.Stderr, "  Delete checkpoint: %s -db ./data/index.db -height -1\n", os.Args[0])
        fmt.Fprintf(os.Stderr, "\n⚠️  WARNING: Only run this while the server is STOPPED!\n")
    }

    flag.Parse()

    // Validate arguments
    if !*status && *height == -9999 {
        flag.Usage()
        os.Exit(1)
    }

    // Check if database exists
    if _, err := os.Stat(*dbPath); os.IsNotExist(err) {
        log.Fatalf("❌ Database not found at %s", *dbPath)
    }

    // Open database
    db, err := pebble.Open(*dbPath, &pebble.Options{})
    if err != nil {
        log.Fatalf("❌ Failed to open database: %v", err)
    }
    defer db.Close()

    // Load current checkpoint
    currentCheckpoint, err := loadCheckpoint(db)
    if err != nil {
        log.Printf("⚠️  No existing checkpoint found")
        currentCheckpoint = nil
    }

    // Status mode - just show current state
    if *status {
        showStatus(db, currentCheckpoint)
        return
    }

    // Confirm action
    if !*force {
        if !confirmAction(*height, currentCheckpoint) {
            log.Println("❌ Aborted")
            return
        }
    }

    // Perform the reset
    if *height == -1 {
        // Delete checkpoint entirely
        if err := deleteCheckpoint(db); err != nil {
            log.Fatalf("❌ Failed to delete checkpoint: %v", err)
        }
        log.Println("✅ Checkpoint deleted")
        log.Println("   Server will reindex from configured start height on next startup")
    } else {
        // Reset to specific height
        if err := resetToHeight(db, int32(*height)); err != nil {
            log.Fatalf("❌ Failed to reset checkpoint: %v", err)
        }
        log.Printf("✅ Checkpoint reset to height %d", *height)
        log.Println("   Server will reindex from this height on next startup")
    }

    // Show warning about undo data
    log.Println()
    log.Println("⚠️  Note: Undo data and UTXO entries above this height still exist.")
    log.Println("   The server will overwrite them during reindexing.")
    log.Println("   For a clean reset, delete the entire database directory instead.")
}

// loadCheckpoint retrieves the current checkpoint from the database.
func loadCheckpoint(db *pebble.DB) (*Checkpoint, error) {
    value, closer, err := db.Get([]byte("c"))
    if err == pebble.ErrNotFound {
        return nil, fmt.Errorf("no checkpoint found")
    }
    if err != nil {
        return nil, err
    }
    defer closer.Close()

    var checkpoint Checkpoint
    if err := json.Unmarshal(value, &checkpoint); err != nil {
        return nil, fmt.Errorf("invalid checkpoint data: %w", err)
    }

    return &checkpoint, nil
}

// showStatus displays the current database status.
func showStatus(db *pebble.DB, checkpoint *Checkpoint) {
    fmt.Println("╔══════════════════════════════════════════════════════════════╗")
    fmt.Println("║              Electrum-Go Database Status                     ║")
    fmt.Println("╚══════════════════════════════════════════════════════════════╝")
    fmt.Println()

    if checkpoint == nil {
        fmt.Println("  Checkpoint:     (none)")
        fmt.Println("  Status:         Fresh database, no blocks indexed")
    } else {
        fmt.Printf("  Checkpoint:     Block %d\n", checkpoint.Height)
        fmt.Printf("  Block Hash:     %s\n", checkpoint.BlockHash)
    }

    // Count various entries
    utxoCount := countPrefix(db, []byte{'u'})
    historyCount := countPrefix(db, []byte{'h'})
    undoCount := countPrefix(db, []byte{'d'})
    headerCount := countPrefix(db, []byte{'b'})
    mempoolCount := countPrefix(db, []byte{'m'})
    txIndexCount := countPrefix(db, []byte{'t'})

    fmt.Println()
    fmt.Println("  Database Statistics:")
    fmt.Printf("    UTXOs:        %d\n", utxoCount)
    fmt.Printf("    History:      %d entries\n", historyCount)
    fmt.Printf("    Headers:      %d\n", headerCount)
    fmt.Printf("    Undo records: %d\n", undoCount)
    fmt.Printf("    TX index:     %d\n", txIndexCount)
    fmt.Printf("    Mempool:      %d\n", mempoolCount)

    // Estimate size
    fmt.Println()
    fmt.Println("  Estimated storage:")
    fmt.Printf("    UTXOs:        ~%.1f MB\n", float64(utxoCount*44)/1024/1024)
    fmt.Printf("    History:      ~%.1f MB\n", float64(historyCount*73)/1024/1024)
    fmt.Printf("    Headers:      ~%.1f MB\n", float64(headerCount*85)/1024/1024)
}

// countPrefix counts the number of keys with a given prefix.
func countPrefix(db *pebble.DB, prefix []byte) int64 {
    iter, err := db.NewIter(&pebble.IterOptions{
        LowerBound: prefix,
        UpperBound: prefixUpperBound(prefix),
    })
    if err != nil {
        return 0
    }
    defer iter.Close()

    var count int64
    for iter.First(); iter.Valid(); iter.Next() {
        count++
    }

    return count
}

// prefixUpperBound computes the upper bound for prefix iteration.
func prefixUpperBound(prefix []byte) []byte {
    if len(prefix) == 0 {
        return nil
    }

    end := make([]byte, len(prefix))
    copy(end, prefix)

    for i := len(end) - 1; i >= 0; i-- {
        end[i]++
        if end[i] != 0 {
            return end
        }
    }

    return nil
}

// confirmAction prompts the user for confirmation.
func confirmAction(height int, current *Checkpoint) bool {
    fmt.Println()
    fmt.Println("⚠️  WARNING: This will modify the database!")
    fmt.Println()

    if current != nil {
        fmt.Printf("  Current checkpoint: Block %d\n", current.Height)
    } else {
        fmt.Println("  Current checkpoint: (none)")
    }

    if height == -1 {
        fmt.Println("  Action: DELETE checkpoint (full reindex)")
    } else {
        fmt.Printf("  Action: RESET to height %d\n", height)
        if current != nil && int32(height) > current.Height {
            fmt.Println()
            fmt.Println("  ⚠️  Target height is HIGHER than current!")
            fmt.Println("     This may cause issues. Are you sure?")
        }
    }

    fmt.Println()
    fmt.Print("  Type 'yes' to confirm: ")

    var response string
    fmt.Scanln(&response)

    return response == "yes"
}

// deleteCheckpoint removes the checkpoint from the database.
func deleteCheckpoint(db *pebble.DB) error {
    return db.Delete([]byte("c"), pebble.Sync)
}

// resetToHeight sets the checkpoint to a specific height.
func resetToHeight(db *pebble.DB, height int32) error {
    // Create checkpoint with empty hash
    // The server will detect the mismatch and reindex from this height
    checkpoint := Checkpoint{
        Height:    height,
        BlockHash: "", // Empty hash forces revalidation
    }

    data, err := json.Marshal(checkpoint)
    if err != nil {
        return fmt.Errorf("failed to marshal checkpoint: %w", err)
    }

    return db.Set([]byte("c"), data, pebble.Sync)
}