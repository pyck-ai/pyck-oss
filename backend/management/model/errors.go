package model

import "errors"

var (
	// ErrDeviceUnassociated is returned when a device is not associated with a location.
	ErrDeviceUnassociated = errors.New("device must be associated with a location before check-in")

	// ErrDeviceInUse is returned when another user is already checked in on a device.
	ErrDeviceInUse = errors.New("another user is already checked in on this device")
)