package installation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
)

// MappingChecksum hashes an ordered recovery mapping without exposing UFVKs.
// Callers must add wallet IDs in lexical order and indices in ascending order.
type MappingChecksum struct {
	h hash.Hash
}

func NewMappingChecksum(network, installationID string) *MappingChecksum {
	h := sha256.New()
	fmt.Fprintf(h, "juno-gateway-address-registry-v1\nnetwork=%s\ninstallation_id=%s\n", network, installationID)
	return &MappingChecksum{h: h}
}

func (c *MappingChecksum) Add(walletID string, index uint32, address string) {
	fmt.Fprintf(c.h, "%s\x00%d\x00%s\n", walletID, index, address)
}

func (c *MappingChecksum) Sum() string {
	return hex.EncodeToString(c.h.Sum(nil))
}
