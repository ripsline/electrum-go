package electrum

import (
    "bytes"
    "encoding/hex"
    "fmt"

    "github.com/btcsuite/btcd/chaincfg/chainhash"

    "electrum-go/internal/storage"
)

type MerkleProof struct {
    BlockHeight int32    `json:"block_height"`
    Pos         int      `json:"pos"`
    Merkle      []string `json:"merkle"`
}

func GetTransactionMerkleProof(db *storage.DB, txid []byte,
    height int32) (*MerkleProof, error) {

    txids, err := db.GetBlockTxids(height)
    if err != nil {
        return nil, err
    }
    if len(txids) == 0 {
        return nil, fmt.Errorf("no txids for height %d", height)
    }

    pos := -1
    for i, id := range txids {
        if bytes.Equal(id, txid) {
            pos = i
            break
        }
    }
    if pos < 0 {
        return nil, fmt.Errorf("tx not found in block at height %d", height)
    }

    branch := buildMerkleBranch(txids, pos)

    return &MerkleProof{
        BlockHeight: height,
        Pos:         pos,
        Merkle:      branch,
    }, nil
}

func buildMerkleBranch(txids [][]byte, pos int) []string {
    if len(txids) == 1 {
        return []string{}
    }

    level := make([][32]byte, len(txids))
    for i, txid := range txids {
        copy(level[i][:], txid)
    }

    branch := make([]string, 0)

    for len(level) > 1 {
        sibling := pos ^ 1
        if sibling < len(level) {
            branch = append(branch, hex.EncodeToString(level[sibling][:]))
        }

        next := make([][32]byte, (len(level)+1)/2)
        for i := 0; i < len(level); i += 2 {
            left := level[i]
            right := left
            if i+1 < len(level) {
                right = level[i+1]
            }

            combined := append(left[:], right[:]...)
            hash := chainhash.DoubleHashB(combined)
            copy(next[i/2][:], hash)
        }

        level = next
        pos = pos / 2
    }

    return branch
}