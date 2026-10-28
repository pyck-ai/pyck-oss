package nats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	natsio "github.com/nats-io/nats.go"
)

// userInfoSubject answers with the requester's account; every account imports it.
const userInfoSubject = "$SYS.REQ.USER.INFO"

var (
	// ErrTenantAccountHoldsStreams is returned when the tenant account is the
	// services' account.
	ErrTenantAccountHoldsStreams = errors.New("the NATS tenant account is the services' account")

	errUserInfo = errors.New("no account in the NATS user info")
)

// RequireSeparateTenantAccount fails when tenantAccount is the account of nc,
// a service connection. Tenant clients placed there could have the answers to
// their requests stored in any stream, and nothing else would notice.
func RequireSeparateTenantAccount(ctx context.Context, nc *natsio.Conn, tenantAccount string) error {
	msg, err := nc.RequestWithContext(ctx, userInfoSubject, nil)
	if err != nil {
		return fmt.Errorf("ask the NATS server for the services' account: %w", err)
	}

	var response struct {
		Data struct {
			Account string `json:"account"`
		} `json:"data"`
		Error *struct {
			Description string `json:"description"`
		} `json:"error"`
	}
	if err := json.Unmarshal(msg.Data, &response); err != nil {
		return fmt.Errorf("decode the NATS user info: %w", err)
	}
	if response.Error != nil {
		return fmt.Errorf("%w: %s", errUserInfo, response.Error.Description)
	}
	if response.Data.Account == "" {
		return errUserInfo
	}

	if response.Data.Account == tenantAccount {
		return fmt.Errorf("%w: %q", ErrTenantAccountHoldsStreams, tenantAccount)
	}

	return nil
}
