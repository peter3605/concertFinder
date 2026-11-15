package config

import (
	"testing"
	"time"
)

func TestParseWeekday(t *testing.T) {
	tests := []struct {
		raw     string
		want    time.Weekday
		wantErr bool
	}{
		{"", time.Thursday, false},
		{"  ", time.Thursday, false},
		{"thu", time.Thursday, false},
		{"Thursday", time.Thursday, false},
		{"MON", time.Monday, false},
		{"sunday", time.Sunday, false},
		{"sat", time.Saturday, false},
		{"thurs", time.Thursday, true},
		{"4", time.Thursday, true},
		{"funday", time.Thursday, true},
	}
	for _, tc := range tests {
		got, err := parseWeekday(tc.raw, time.Thursday)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseWeekday(%q) err = %v, wantErr %v", tc.raw, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("parseWeekday(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// Unset is Thursday, and a typo stops startup rather than quietly sending
// the digest on a day nobody picked.
func TestLoadDigestWeekday(t *testing.T) {
	t.Setenv("DIGEST_WEEKDAY", "")
	c := loadDefaults(t)
	if c.DigestWeekday != time.Thursday {
		t.Fatalf("default DigestWeekday = %v, want Thursday", c.DigestWeekday)
	}
	for _, e := range c.Validate() {
		if e == c.digestWeekdayErr && e != nil {
			t.Fatalf("unset DIGEST_WEEKDAY reported as an error: %v", e)
		}
	}

	t.Setenv("DIGEST_WEEKDAY", "tue")
	if c = loadDefaults(t); c.DigestWeekday != time.Tuesday {
		t.Fatalf("DIGEST_WEEKDAY=tue gave %v", c.DigestWeekday)
	}

	t.Setenv("DIGEST_WEEKDAY", "thurs")
	c = loadDefaults(t)
	found := false
	for _, e := range c.Validate() {
		if e != nil && e == c.digestWeekdayErr {
			found = true
		}
	}
	if !found {
		t.Fatal("Validate did not report a malformed DIGEST_WEEKDAY")
	}
}
