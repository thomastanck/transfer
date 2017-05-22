package util

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"
)

func GenerateRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	// Note that err == nil only if we read len(b) bytes.
	if err != nil {
		return nil, err
	}

	return b, nil
}

// GenerateRandomString returns a URL-safe, base64 encoded
// securely generated random string.
func GenerateRandomString(s int) (string, error) {
	b, err := GenerateRandomBytes(s)
	return base64.URLEncoding.EncodeToString(b), err
}

// Like Reset but avoids the race condition
// Ensure you lock sessionsMut when refreshing timeoutTimer's
func RefreshTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		<-t.C
	}
	t.Reset(d)
}

func DropConnection(w http.ResponseWriter) {
	if wr, ok := w.(http.Hijacker); ok {
		conn, _, err := wr.Hijack()
		if err != nil {
			fmt.Fprint(w, err)
		}
		conn.Close()
	}
}

