package workbuddy

import "testing"

func TestDecodeCredentialDeviceToken(t *testing.T) {
	// nested 路径
	nested := []byte(`{"account":{"uid":"u1"},"auth":{"accessToken":"tok","deviceToken":"dt-nested"}}`)
	c1, err := DecodeCredential(nested)
	if err != nil || c1.DeviceToken != "dt-nested" {
		t.Fatalf("nested: got %q err=%v", c1.DeviceToken, err)
	}
	// flat 路径
	flat := []byte(`{"access_token":"tok","uid":"u1","device_token":"dt-flat"}`)
	c2, err := DecodeCredential(flat)
	if err != nil || c2.DeviceToken != "dt-flat" {
		t.Fatalf("flat: got %q err=%v", c2.DeviceToken, err)
	}
	// camel 路径
	camel := []byte(`{"accessToken":"tok","uid":"u1","deviceToken":"dt-camel"}`)
	c3, err := DecodeCredential(camel)
	if err != nil || c3.DeviceToken != "dt-camel" {
		t.Fatalf("camel: got %q err=%v", c3.DeviceToken, err)
	}
	// 无 device_token → 空串
	none := []byte(`{"access_token":"tok","uid":"u1"}`)
	c4, _ := DecodeCredential(none)
	if c4.DeviceToken != "" {
		t.Fatalf("none: got %q want empty", c4.DeviceToken)
	}
}
