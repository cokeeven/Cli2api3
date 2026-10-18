package accounts

import "testing"

func TestMaintenanceScheduleValidatesWithoutChangingDefaults(t *testing.T) {
	s := DefaultMaintenanceSchedule()
	if s.Validate() != nil || !s.CredentialEnabled || !s.QuotaEnabled || !s.DailyEnabled || s.DailyTime != "22:00" {
		t.Fatal("legacy defaults changed")
	}
	for _, interval := range []int{0, 4, 1441} {
		s = DefaultMaintenanceSchedule()
		s.CredentialIntervalMinutes = interval
		if s.Validate() == nil {
			t.Fatal("invalid interval")
		}
	}
	for _, clock := range []string{"", "24:00", "22:61", "bad"} {
		s = DefaultMaintenanceSchedule()
		s.DailyTime = clock
		if s.Validate() == nil {
			t.Fatal("invalid daily time")
		}
	}
}
