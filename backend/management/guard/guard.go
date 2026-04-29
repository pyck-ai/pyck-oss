package guard

import (
	"context"
	"time"

	"github.com/pyck-ai/pyck/backend/common/guards"
	"github.com/pyck-ai/pyck/backend/common/log"

	managementapi "github.com/pyck-ai/pyck/backend/management/api"
)

const (
	checkTimeout  = 10 * time.Second
	retryInterval = 5 * time.Second
)

// WaitForManagement blocks until the management service is reachable and
// (when versionCheck is true) reports the same version as localVersion.
// It retries every 5 seconds for up to 5 minutes. In dev mode (both versions
// are "dev"), the version check is skipped.
func WaitForManagement(ctx context.Context, client managementapi.Client, localVersion string, versionCheck bool) error {
	return guards.New().
		WithTimeout(5 * time.Minute).
		Add(guards.Check{
			Name:          "management-service",
			Timeout:       checkTimeout,
			RetryInterval: retryInterval,
			CheckFunc: func(ctx context.Context) (bool, error) {
				info, err := client.GetManagementServiceInfo(ctx)
				if err != nil {
					log.ForContext(ctx).Warn().Err(err).Msg("waiting for management service...")
					return false, nil
				}

				if !versionCheck {
					log.ForContext(ctx).Info().Msg("management service ready (version check disabled)")
					return true, nil
				}

				remoteVersion := info.GetManagementServiceInfo().GetVersion()

				if localVersion == remoteVersion {
					log.ForContext(ctx).Info().
						Str("version", localVersion).
						Msg("management service ready, versions match")
					return true, nil
				}

				log.ForContext(ctx).Warn().
					Str("local", localVersion).
					Str("remote", remoteVersion).
					Msg("version mismatch, retrying...")

				return false, nil
			},
		}).
		Wait(ctx)
}
