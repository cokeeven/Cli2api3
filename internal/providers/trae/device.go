package trae

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"strconv"
	"time"
)

// generateDeviceKey creates an EC P-256 keypair for the "device proof" the v3
// code exchange requires. The public key is sent as a PEM SubjectPublicKeyInfo
// block (the shape the client sends as DeviceInfo.DevicePublicKey); the private
// key is stored in the credential so the public key can be re-derived later.
func generateDeviceKey() (privPEM, pubPEM string, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", "", err
	}
	privPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return "", "", err
	}
	pubPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	return privPEM, pubPEM, nil
}

func parseDeviceKey(privPEM string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privPEM))
	if block == nil {
		return nil, fmt.Errorf("device key: not PEM")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if ec, ok := key.(*ecdsa.PrivateKey); ok {
			return ec, nil
		}
	}
	if ec, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return ec, nil
	}
	return nil, fmt.Errorf("device key: not an EC private key")
}

// devicePublicKeyPEM returns the PEM public key for a stored private key.
func devicePublicKeyPEM(privPEM string) string {
	priv, err := parseDeviceKey(privPEM)
	if err != nil {
		return ""
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// deviceProof builds the device-proof object Trae's v3 ExchangeToken endpoint
// now requires. Since ~2026-09 the endpoint rejects requests without a proof
// with 20405 "Device proof required": the proof is the device key's
// ECDSA-P256/SHA-256 signature (base64 DER, low-s normalised) over the canonical
// string
//
//	POST\n{path}\n{ClientID}\n{credential}\n{Timestamp}\n{Nonce}
//
// where {credential} is the refresh token for refreshes and the authorization
// code for first-time code exchange. The nonce is 16 random bytes as hex.
func deviceProof(privPEM, path, clientID, credential string) (map[string]any, error) {
	priv, err := parseDeviceKey(privPEM)
	if err != nil {
		return nil, fmt.Errorf("device proof: %w", err)
	}
	ts := time.Now().Unix()
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("device proof nonce: %w", err)
	}
	nonce := fmt.Sprintf("%x", nonceBytes)
	canonical := "POST\n" + path + "\n" + clientID + "\n" + credential + "\n" + strconv.FormatInt(ts, 10) + "\n" + nonce
	digest := sha256.Sum256([]byte(canonical))
	r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		return nil, fmt.Errorf("device proof sign: %w", err)
	}
	// Low-s normalisation so the signature matches what the desktop client emits.
	if s.Cmp(halfCurveOrder) > 0 {
		s = new(big.Int).Sub(curveOrder, s)
	}
	sigDER, err := encodeECDSASignature(r, s)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"Timestamp": ts,
		"Nonce":     nonce,
		"Signature": base64.StdEncoding.EncodeToString(sigDER),
	}, nil
}

var curveOrder, _ = new(big.Int).SetString("ffffffff00000000ffffffffffffffffbce6faada7179e84f3b9cac2fc632551", 16)
var halfCurveOrder = new(big.Int).Rsh(new(big.Int).Set(curveOrder), 1)

// encodeECDSASignature DER-encodes an ECDSA signature (r, s).
func encodeECDSASignature(r, s *big.Int) ([]byte, error) {
	encodeInt := func(x *big.Int) []byte {
		b := x.Bytes()
		if len(b) == 0 {
			b = []byte{0}
		}
		if b[0]&0x80 != 0 {
			b = append([]byte{0}, b...)
		}
		return append([]byte{0x02, byte(len(b))}, b...)
	}
	rBytes := encodeInt(r)
	sBytes := encodeInt(s)
	body := append(append([]byte{}, rBytes...), sBytes...)
	return append([]byte{0x30, byte(len(body))}, body...), nil
}
