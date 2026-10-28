package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRegistrationBudgets(t *testing.T) {
	t.Parallel()

	const (
		wait    = 25 * time.Second
		poll    = 250 * time.Millisecond
		timeout = 10 * time.Second
		lock    = 10 * time.Second
	)

	require.NoError(t, validateRegistrationBudgets(wait, poll, timeout, lock), "the defaults must pass")

	for name, tc := range map[string]struct {
		wait, poll, timeout, lock time.Duration
		want                      error
		knob                      string
	}{
		"zero wait":                 {0, poll, timeout, lock, errNonPositiveInterval, "PYCK_TENANT_REGISTRATION_WAIT"},
		"zero poll":                 {wait, 0, timeout, lock, errNonPositiveInterval, "PYCK_TENANT_REGISTRATION_POLL"},
		"negative poll":             {wait, -time.Second, timeout, lock, errNonPositiveInterval, "PYCK_TENANT_REGISTRATION_POLL"},
		"zero temporal timeout":     {wait, poll, 0, lock, errNonPositiveInterval, "PYCK_TENANT_EVENT_TEMPORAL_CALL_TIMEOUT"},
		"negative temporal timeout": {wait, poll, -time.Second, lock, errNonPositiveInterval, "PYCK_TENANT_EVENT_TEMPORAL_CALL_TIMEOUT"},
		"zero stream lock wait":     {wait, poll, timeout, 0, errNonPositiveInterval, "PYCK_EVENT_STREAM_LOCK_WAIT"},
		"negative stream lock wait": {wait, poll, timeout, -time.Second, errNonPositiveInterval, "PYCK_EVENT_STREAM_LOCK_WAIT"},
		"poll equal to wait":        {wait, wait, timeout, lock, errPollNotBelowWait, "PYCK_TENANT_REGISTRATION_POLL"},
		"poll above wait":           {time.Second, 2 * time.Second, timeout, lock, errPollNotBelowWait, "PYCK_TENANT_REGISTRATION_POLL"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := validateRegistrationBudgets(tc.wait, tc.poll, tc.timeout, tc.lock)
			require.ErrorIs(t, err, tc.want)
			assert.Contains(t, err.Error(), tc.knob, "the error must name the env var to fix")
		})
	}
}
