package gateway

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// NewSourceFingerprinter returns an optional, process-scoped pseudonym. The
// random HMAC key is never persisted; restarting prevents cross-run tracking.
// Only the IP is considered, never UA, URL, cookies or authentication headers.
func NewSourceFingerprinter(enabled bool, trustedCIDRs []string) (func(*http.Request) string, error) {
	var trusted []netip.Prefix
	for _, raw := range trustedCIDRs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR")
		}
		trusted = append(trusted, prefix)
	}
	if !enabled {
		return nil, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	isTrusted := func(ip netip.Addr) bool {
		for _, prefix := range trusted {
			if prefix.Contains(ip.Unmap()) {
				return true
			}
		}
		return false
	}
	return func(r *http.Request) string {
		if r == nil {
			return ""
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || ip.Zone() != "" {
			return ""
		}
		ip = ip.Unmap()
		// Walk right to left, stopping at the first untrusted hop. A direct
		// caller cannot choose its fingerprint by forging forwarding headers.
		if isTrusted(ip) {
			raw := r.Header.Get("X-Forwarded-For")
			if len(raw) > 2048 {
				return ""
			}
			hops := strings.Split(raw, ",")
			if len(hops) > 32 {
				return ""
			}
			for i := len(hops) - 1; i >= 0 && isTrusted(ip); i-- {
				next, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
				if err != nil || next.Zone() != "" {
					break
				}
				ip = next.Unmap()
			}
		}
		hash := hmac.New(sha256.New, key)
		_, _ = hash.Write([]byte(ip.String()))
		return "src_" + hex.EncodeToString(hash.Sum(nil)[:16])
	}, nil
}
