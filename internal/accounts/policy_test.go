package accounts

import "testing"

func boolPtr(v bool) *bool { return &v }

func TestDefaultAccountPolicyValues(t *testing.T) {
	if DefaultMaxInFlightValue(0) != DefaultMaxInFlight || DefaultMaxInFlightValue(-1) != DefaultMaxInFlight {
		t.Fatal("max inflight must default to 4")
	}
	if DefaultMaxInFlightValue(6) != 6 {
		t.Fatal("explicit max inflight must be kept")
	}
	if DefaultPriorityValue(0) != DefaultPriority || DefaultPriorityValue(80) != 80 {
		t.Fatal("priority defaults to 50 unless set")
	}
	if !DefaultDropSystemPrompt(nil) {
		t.Fatal("drop system prompt defaults on")
	}
	off := false
	if DefaultDropSystemPrompt(&off) {
		t.Fatal("explicit drop system prompt false must stick")
	}
	for _, provider := range []string{"workbuddy", "trae", "qoder", "devin", "codex", "command", "zhipu", ""} {
		if DefaultDropSystemPromptForProvider(provider, nil) {
			t.Fatalf("%s must default to preserving system prompts", provider)
		}
	}
	if DefaultDropSystemPromptForProvider("Trae", &off) {
		t.Fatal("explicit drop false must win over the trae default")
	}
	if !DefaultDropSystemPromptForProvider("workbuddy", boolPtr(true)) {
		t.Fatal("explicit drop true must win over the workbuddy default")
	}
	if DefaultWorkBuddyAutoCheckin(nil) {
		t.Fatal("auto check-in defaults off")
	}
}

func TestResolveWorkBuddyCheckinTimeInheritsDefault(t *testing.T) {
	got, err := ResolveWorkBuddyCheckinTime("", "08:30")
	if err != nil || got != "08:30" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	got, err = ResolveWorkBuddyCheckinTime("10:15", "08:30")
	if err != nil || got != "10:15" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestValidateModelContextLength(t *testing.T) {
	for _, tt := range []struct {
		value int
		ok    bool
	}{
		{-1, false},
		{0, true},
		{1023, false},
		{1024, true},
		{4000000, true},
		{4000001, false},
	} {
		err := ValidateModelContextLength(tt.value)
		if tt.ok && err != nil {
			t.Fatalf("value=%d err=%v", tt.value, err)
		}
		if !tt.ok && err == nil {
			t.Fatalf("value=%d expected error", tt.value)
		}
	}
}

func TestValidateAccountProxyRejectsQoderSOCKS(t *testing.T) {
	if err := ValidateAccountProxy("qoder", "global", "socks5://proxy.example:1080"); err == nil {
		t.Fatal("qoder socks must fail")
	}
	if err := ValidateAccountProxy("workbuddy", "cn", "socks5://proxy.example:1080"); err != nil {
		t.Fatalf("workbuddy socks: %v", err)
	}
}
