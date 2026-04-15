package indexer

import (
    "bytes"
    "crypto/sha256"
    "encoding/hex"
    "testing"
)

func TestComputeScripthash_Length(t *testing.T) {
    got := ComputeScripthash([]byte{0x76, 0xa9})
    if len(got) != 32 {
        t.Fatalf("expected 32 bytes, got %d", len(got))
    }
}

func TestComputeScripthash_Deterministic(t *testing.T) {
    script := []byte{0x76, 0xa9, 0x14, 0x01, 0x02, 0x03}
    a := ComputeScripthash(script)
    b := ComputeScripthash(script)
    if !bytes.Equal(a, b) {
        t.Fatalf("non-deterministic: %x vs %x", a, b)
    }
}

// TestComputeScripthash_ReversedSHA256 pins down the Electrum format:
// the result is SHA256(script) with bytes reversed. This is what the
// Electrum protocol calls a "scripthash" and is distinct from any
// hash Bitcoin Core emits natively.
func TestComputeScripthash_ReversedSHA256(t *testing.T) {
    script := []byte{0xde, 0xad, 0xbe, 0xef}
    sum := sha256.Sum256(script)

    want := make([]byte, 32)
    for i := 0; i < 32; i++ {
        want[i] = sum[31-i]
    }

    got := ComputeScripthash(script)
    if !bytes.Equal(got, want) {
        t.Fatalf("wrong byte order:\ngot  %x\nwant %x", got, want)
    }
}

// TestComputeScripthash_EmptyScript pins a known vector: SHA256("")
// reversed. If this changes, the on-disk index becomes incompatible
// with every existing Electrum client — break loudly.
func TestComputeScripthash_EmptyScript(t *testing.T) {
    const want = "55b852781b9995a44c939b64e441ae2724b96f99c8f4fb9a141cfc9842c4b0e3"
    got := ComputeScripthashHex([]byte{})
    if got != want {
        t.Fatalf("scripthash for empty script changed:\ngot  %s\nwant %s", got, want)
    }
}

func TestComputeScripthash_DistinctScriptsDistinctHashes(t *testing.T) {
    a := ComputeScripthash([]byte{0x01})
    b := ComputeScripthash([]byte{0x02})
    if bytes.Equal(a, b) {
        t.Fatalf("collision on trivially distinct scripts: %x", a)
    }
}

func TestComputeScripthashHex(t *testing.T) {
    raw := ComputeScripthash([]byte{0x42})
    hexStr := ComputeScripthashHex([]byte{0x42})
    decoded, err := hex.DecodeString(hexStr)
    if err != nil {
        t.Fatalf("invalid hex: %v", err)
    }
    if !bytes.Equal(raw, decoded) {
        t.Fatalf("hex form decodes to different bytes:\nraw    %x\ndecoded %x",
            raw, decoded)
    }
}
