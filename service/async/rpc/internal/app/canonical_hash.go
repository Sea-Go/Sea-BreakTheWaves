package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

// canonicalInputHash is the raw-wire JCS SHA-256 input hash shared with
// Sea-RideTheWind communityfact.CanonicalHash and DataCenter
// platformjson.Canonical. The golden vector file under testdata/ is
// byte-identical across the three repositories and pins all implementations.
func canonicalInputHash(raw []byte) (string, error) {
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalEventHash fails closed unless the RTW authority's event and the
// DataCenter event canonicalize to identical bytes, then returns their shared
// input hash.
func canonicalEventHash(authorityEvent, dcEvent any) (string, error) {
	authorityRaw, err := json.Marshal(authorityEvent)
	if err != nil {
		return "", err
	}
	dcRaw, err := json.Marshal(dcEvent)
	if err != nil {
		return "", err
	}
	authorityCanonical, err := jsoncanonicalizer.Transform(authorityRaw)
	if err != nil {
		return "", err
	}
	dcCanonical, err := jsoncanonicalizer.Transform(dcRaw)
	if err != nil || !bytes.Equal(authorityCanonical, dcCanonical) {
		return "", ErrFactDeliveryContract
	}
	sum := sha256.Sum256(authorityCanonical)
	return hex.EncodeToString(sum[:]), nil
}
