package control

import "testing"

func TestMaintenanceMessageProjectionOmitsSensitiveHistoricalDetails(t *testing.T) {
	for _, input := range []string{
		"Authorization: Bearer live-secret", "Cookie: session=live-secret", "access_token=live-secret",
		"refreshToken=live-secret", "request https://upstream.invalid/?key=live-secret failed", "{\"raw\":\"upstream payload\"}",
		"api_key: live-secret", "auth_blob=live-secret", "device-token: live-secret",
		"refresh token: opaque-value", "request failed: {\"raw\":\"payload\"}", "sk-abcdefghijklmnop",
	} {
		if got := safeMaintenanceMessage(input); got != "Sensitive upstream details omitted" {
			t.Fatalf("unsafe projection=%q", got)
		}
	}
	if got := safeMaintenanceMessage("签到成功，获得 100 积分"); got != "签到成功，获得 100 积分" {
		t.Fatalf("ordinary message=%q", got)
	}
}
