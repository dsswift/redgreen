package provider

import "time"

// ValidTimeZone says whether name is an IANA time zone this build knows.
func ValidTimeZone(name string) bool {
	if name == "" || name == "Local" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}
