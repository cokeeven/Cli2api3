package config

import (
	"testing"
	"time"
)

func TestChatDeadlineConfiguration(t *testing.T) {
	for _, name := range []string{"CLI2API_CHAT_HEADERS_TIMEOUT_SECONDS", "CLI2API_CHAT_FIRST_EVENT_TIMEOUT_SECONDS", "CLI2API_CHAT_IDLE_TIMEOUT_SECONDS", "CLI2API_CHAT_TOTAL_TIMEOUT_SECONDS"} {
		t.Setenv(name, "")
	}
	t.Setenv("CLI2API_CHAT_CAPACITY_QUEUE_MAX_WAITERS", "")
	t.Setenv("CLI2API_CHAT_CAPACITY_QUEUE_WAIT_SECONDS", "")
	cfg, err := Load()
	if err != nil || cfg.ChatHeadersTimeout != 45*time.Second || cfg.ChatFirstEventTimeout != 120*time.Second || cfg.ChatIdleTimeout != 90*time.Second || cfg.ChatTotalTimeout != 300*time.Second || cfg.ChatCapacityQueueMaxWaiters != 64 || cfg.ChatCapacityQueueWait != 5*time.Second {
		t.Fatal("incorrect defaults", err)
	}
	for _, value := range []string{"0", "-1", "abc", "3601"} {
		t.Setenv("CLI2API_CHAT_IDLE_TIMEOUT_SECONDS", value)
		if _, err = Load(); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	t.Setenv("CLI2API_CHAT_IDLE_TIMEOUT_SECONDS", "31")
	cfg, err = Load()
	if err != nil || cfg.ChatIdleTimeout != 31*time.Second {
		t.Fatal("override", err)
	}
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"CLI2API_CHAT_CAPACITY_QUEUE_MAX_WAITERS", "-1"},
		{"CLI2API_CHAT_CAPACITY_QUEUE_MAX_WAITERS", "257"},
		{"CLI2API_CHAT_CAPACITY_QUEUE_WAIT_SECONDS", "-1"},
		{"CLI2API_CHAT_CAPACITY_QUEUE_WAIT_SECONDS", "61"},
	} {
		t.Setenv(tc.name, tc.value)
		if _, err := Load(); err == nil {
			t.Fatalf("accepted %s=%s", tc.name, tc.value)
		}
	}
	t.Setenv("CLI2API_CHAT_CAPACITY_QUEUE_MAX_WAITERS", "0")
	t.Setenv("CLI2API_CHAT_CAPACITY_QUEUE_WAIT_SECONDS", "0")
	cfg, err = Load()
	if err != nil || cfg.ChatCapacityQueueMaxWaiters != 0 || cfg.ChatCapacityQueueWait != 0 {
		t.Fatal("zero did not disable the queue", err)
	}
}
