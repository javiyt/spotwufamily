package maintenance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TokenTTL is how long a preview token stays valid.
const TokenTTL = 10 * time.Minute

// Token binds a preview (action, params, fingerprint) to an expiry. It is
// "<expiryUnix>.<base64url HMAC-SHA256>" and verifies statelessly.
type Token string

// canonicalParams encodes p deterministically and unambiguously: keys sorted,
// each key and value length-prefixed, pairs joined by '&'.
func canonicalParams(p Params) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = lp(k) + "=" + lp(p[k])
	}
	return strings.Join(parts, "&")
}

// lp length-prefixes s so concatenations cannot be ambiguous.
func lp(s string) string { return strconv.Itoa(len(s)) + ":" + s }

func tokenMAC(secret []byte, name string, p Params, fingerprint string, expiry int64) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(lp(name) + "|" + lp(canonicalParams(p)) + "|" + lp(fingerprint) + "|" + strconv.FormatInt(expiry, 10)))
	return h.Sum(nil)
}

func signToken(secret []byte, name string, p Params, fingerprint string, expiry time.Time) Token {
	exp := expiry.Unix()
	mac := tokenMAC(secret, name, p, fingerprint, exp)
	return Token(strconv.FormatInt(exp, 10) + "." + base64.RawURLEncoding.EncodeToString(mac))
}

// verifyToken reports whether tok was signed for exactly these inputs and has
// not expired at now.
func verifyToken(secret []byte, tok Token, name string, p Params, fingerprint string, now time.Time) bool {
	expStr, macStr, ok := strings.Cut(string(tok), ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || now.Unix() > exp {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(macStr)
	if err != nil {
		return false
	}
	return hmac.Equal(got, tokenMAC(secret, name, p, fingerprint, exp))
}
