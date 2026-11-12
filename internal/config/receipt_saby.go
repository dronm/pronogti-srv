package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/dronm/pronogti/internal/fiscal"
)

type SabyDeviceConfig struct {
	DriverBaseURL      string `json:"driver_base_url"`
	DeviceID           string `json:"device_id"`
	RegistrationNumber string `json:"registration_number"`
}

var sabyRegistrationNumber = regexp.MustCompile(`^[0-9]{11,16}$`)
var sabyDeviceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func (c ReceiptEmailConfig) validateSabyDevices() error {
	if len(c.SabyDevices) == 0 {
		return errors.New("receipt_email.saby_devices is required when receipt email is enabled")
	}
	seen := make(map[[2]string]bool, len(c.SabyDevices))
	for i, device := range c.SabyDevices {
		if !fiscal.ValidDriverBaseURL(device.DriverBaseURL) || !sabyDeviceID.MatchString(device.DeviceID) ||
			!sabyRegistrationNumber.MatchString(device.RegistrationNumber) {
			return fmt.Errorf("receipt_email.saby_devices[%d] requires a driver URL, device ID and numeric KKT registration number", i)
		}
		key := [2]string{strings.TrimRight(device.DriverBaseURL, "/"), device.DeviceID}
		if seen[key] {
			return fmt.Errorf("receipt_email.saby_devices[%d] repeats a driver URL and device ID", i)
		}
		seen[key] = true
	}
	return nil
}

func (c ReceiptEmailConfig) SabyRegistrationNumber(driverBaseURL, deviceID string) string {
	base := strings.TrimRight(driverBaseURL, "/")
	for _, device := range c.SabyDevices {
		if device.DeviceID == deviceID && strings.TrimRight(device.DriverBaseURL, "/") == base {
			return device.RegistrationNumber
		}
	}
	return ""
}
