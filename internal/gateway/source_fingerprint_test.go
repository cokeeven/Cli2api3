package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSourceFingerprintDisabledAndTrustedProxyBoundary(t *testing.T) {
	f, err := NewSourceFingerprinter(false, nil)
	if err != nil || f != nil {
		t.Fatal("disabled collection")
	}
	f, err = NewSourceFingerprinter(true, []string{"127.0.0.0/8", "10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://localhost/v1/chat/completions", nil)
	r.RemoteAddr = "192.0.2.1:99"
	direct := f(r)
	r.Header.Set("X-Forwarded-For", "203.0.113.1")
	if f(r) != direct {
		t.Fatal("untrusted caller forged source")
	}
	r.RemoteAddr = "127.0.0.1:99"
	r.Header.Set("X-Forwarded-For", "192.0.2.1,10.0.0.1")
	if f(r) != direct {
		t.Fatal("trusted proxy chain did not resolve")
	}
	r.Header.Set("X-Forwarded-For", "203.0.113.1,192.0.2.1")
	if f(r) != direct {
		t.Fatal("untrusted intermediate header accepted")
	}
	if len(direct) != 36 || strings.Contains(direct, "192.0.2.1") {
		t.Fatal("raw address exposed")
	}
	other, _ := NewSourceFingerprinter(true, nil)
	r.RemoteAddr = "192.0.2.1:99"
	if other(r) == direct {
		t.Fatal("cross-process identifier reused")
	}
	if _, err = NewSourceFingerprinter(true, []string{"all"}); err == nil {
		t.Fatal("invalid trust boundary")
	}
}
