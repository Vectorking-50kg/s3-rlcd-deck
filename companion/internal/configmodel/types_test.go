package configmodel

import "testing"

func TestValidateWebSettingsKeepsLANExposureExplicit(t *testing.T) {
	tests := []struct {
		name     string
		settings WebSettings
		valid    bool
	}{
		{name: "loopback default", settings: WebSettings{ManagementAddress: DefaultManagementAddress}, valid: true},
		{name: "LAN address without opt in", settings: WebSettings{ManagementAddress: "0.0.0.0:7777"}},
		{name: "LAN opt in without exact origin", settings: WebSettings{ManagementAddress: "0.0.0.0:7777", AllowLAN: true}},
		{name: "LAN explicit", settings: WebSettings{ManagementAddress: "0.0.0.0:7777", AllowLAN: true, AllowedOrigin: "https://companion.example.test"}, valid: true},
		{name: "origin credentials", settings: WebSettings{ManagementAddress: "0.0.0.0:7777", AllowLAN: true, AllowedOrigin: "https://user@companion.example.test"}},
		{name: "origin path", settings: WebSettings{ManagementAddress: "0.0.0.0:7777", AllowLAN: true, AllowedOrigin: "https://companion.example.test/path"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ValidateWebSettings(test.settings); got != test.valid {
				t.Fatalf("ValidateWebSettings(%#v) = %v, want %v", test.settings, got, test.valid)
			}
		})
	}
}

func TestValidateDeviceProfileRequiresCanonicalBoundedPublicMetadata(t *testing.T) {
	valid := DeviceProfile{
		DeviceID: "deck-12345678", FirmwareVersion: "0.2.0-dev",
		Board: "esp32-s3-rlcd-4.2", Capabilities: []string{"display", "serial"},
		LastSeenUTC: "2026-08-15T12:00:00Z",
	}
	if !ValidateDeviceProfile(valid) {
		t.Fatal("valid public Device Profile was rejected")
	}
	unsorted := valid
	unsorted.Capabilities = []string{"serial", "display"}
	if ValidateDeviceProfile(unsorted) {
		t.Fatal("unsorted capabilities were accepted")
	}
	nonCanonical := valid
	nonCanonical.LastSeenUTC = "2026-08-15T20:00:00+08:00"
	if ValidateDeviceProfile(nonCanonical) {
		t.Fatal("non-canonical UTC was accepted")
	}
}
