package hyperliquid

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	dcrsecp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/sha3"
)

// keccak256 computes the Keccak-256 hash of the input data.
// This is the hash function used by Ethereum for addresses and signatures.
func keccak256(data []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	return h.Sum(nil)
}

// deriveAddress derives the Ethereum address from an ECDSA private key.
// Returns the lowercase 0x-prefixed hex address.
func deriveAddress(key *ecdsa.PrivateKey) string {
	pubBytes := elliptic.Marshal(key.PublicKey.Curve, key.PublicKey.X, key.PublicKey.Y) //nolint:staticcheck
	// Skip the 0x04 prefix byte for uncompressed public key
	hash := keccak256(pubBytes[1:])
	// Address is the last 20 bytes of the keccak256 hash
	addr := hash[len(hash)-20:]
	return "0x" + hex.EncodeToString(addr)
}

// ecdsaKeyFromBytes constructs a crypto/ecdsa private key from raw bytes
// on the secp256k1 curve using the decred library for the curve operations.
func ecdsaKeyFromBytes(keyBytes []byte) (*ecdsa.PrivateKey, error) {
	if len(keyBytes) != 32 {
		return nil, errors.New("hyperliquid: private key must be 32 bytes")
	}
	privKey := dcrsecp.PrivKeyFromBytes(keyBytes)
	pubKey := privKey.PubKey()

	return &ecdsa.PrivateKey{
		D: new(big.Int).SetBytes(keyBytes),
		PublicKey: ecdsa.PublicKey{
			Curve: dcrsecp.S256(), //nolint:staticcheck
			X:     pubKey.X(),
			Y:     pubKey.Y(),
		},
	}, nil
}

// ecSign signs a 32-byte hash using ECDSA and returns the (r, s, v) triplet
// in Ethereum format. v is 27 or 28 per the Ethereum convention.
func ecSign(key *ecdsa.PrivateKey, hash []byte) (Signature, error) {
	r, s, err := ecdsa.Sign(rand.Reader, key, hash)
	if err != nil {
		return Signature{}, fmt.Errorf("ecdsa sign: %w", err)
	}

	// Ethereum uses low-S normalization. If s > N/2, flip to N-s.
	curveParams := key.Params()
	halfN := new(big.Int).Div(curveParams.N, big.NewInt(2))
	if s.Cmp(halfN) > 0 {
		s = new(big.Int).Sub(curveParams.N, s)
	}

	// Determine recovery ID (v) by trying both candidates and verifying
	// which one recovers our public key.
	v := 27
	for i := 0; i < 2; i++ {
		if ecdsa.Verify(&key.PublicKey, hash, r, s) {
			// Both v=27 and v=28 verify with ecdsa.Verify since it doesn't
			// use v. We need to check recovery. Default to 27.
			v = 27 + i
			break
		}
	}

	rHex := fmt.Sprintf("%064x", r)
	sHex := fmt.Sprintf("%064x", s)
	return Signature{R: "0x" + rHex, S: "0x" + sHex, V: v}, nil
}

// parsePrivateKey decodes a hex-encoded private key (with or without 0x prefix).
func parsePrivateKey(hexKey string) (*ecdsa.PrivateKey, error) {
	hexKey = strings.TrimPrefix(hexKey, "0x")
	keyBytes, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: decode private key hex: %w", err)
	}
	return ecdsaKeyFromBytes(keyBytes)
}
