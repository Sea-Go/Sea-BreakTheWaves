package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

func randID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
