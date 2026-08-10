package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
)

// fileSignature computes the HMAC that authorizes access to a private file
// until exp (unix seconds). Any service that shares FK_SIGN_SECRET can build
// URLs locally without calling filekeeper:
//
//	sig = hex(hmac_sha256(secret, "<id>:<exp>"))
//	url = <base>/f/<id>?exp=<exp>&sig=<sig>
func fileSignature(secret []byte, id string, exp int64) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%s:%d", id, exp)
	return hex.EncodeToString(mac.Sum(nil))
}

func verifySignature(secret []byte, id, expStr, sig string, now int64) bool {
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || exp < now {
		return false
	}
	want := fileSignature(secret, id, exp)
	return hmac.Equal([]byte(want), []byte(sig))
}
