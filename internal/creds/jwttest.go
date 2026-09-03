package creds

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// MakeJWT produces "base64url(header).base64url(payload).signature" from the
// given claims. The signature segment is a fixed placeholder, not
// cryptographically signed — this is for test fixtures only.
func MakeJWT(claims map[string]any) string {
	header := map[string]any{"alg": "HS256", "typ": "JWT"}
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString
	return fmt.Sprintf("%s.%s.dummy_signature", enc(h), enc(p))
}
