package util

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/pyck-ai/pyck/tests/nats/config"
)

// AsyncErrorChannel is used to capture async errors
var AsyncErrorChannel = make(chan error, 1)

// CreateTestConnection creates a NATS connection with the provided JWT token
func CreateTestConnection(token string) (*nats.Conn, error) {
	natsURL := os.Getenv(config.EnvNatsWSURL)

	// Parse the URL to extract the path
	parsedURL, err := url.Parse(natsURL)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", config.EnvNatsWSURL, err)
	}

	// Extract the path and set it as the ProxyPath if present
	proxyPath := parsedURL.Path
	parsedURL.Path = "" // Remove the path from the URL

	opts := []nats.Option{
		nats.Token(token),
		nats.Name(config.TestClientName),
	}

	x := parsedURL.String()
	fmt.Printf("Connecting to NATS server: %s\n", x)

	// Conditionally add ProxyPath if a path is present
	if proxyPath != "" {
		opts = append(opts, nats.ProxyPath(proxyPath))
	}

	opts = append(opts,
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			select {
			case AsyncErrorChannel <- err:
			default:
			}
			logDebug("Async error: %v", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logDebug("Reconnected to %v", nc.ConnectedUrl())
		}),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			logDebug("Disconnected due to: %v", err)
		}),
		nats.ClosedHandler(func(nc *nats.Conn) {
			logDebug("Connection closed. Reason: %v", nc.LastError())
		}),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
		nats.Timeout(time.Duration(config.DefaultTimeout)*time.Second),
	)

	return nats.Connect(parsedURL.String(), opts...)
}

// WaitForAsyncError waits for an async error that matches the given substring
func WaitForAsyncError(timeout time.Duration, substring string) error {
	select {
	case err := <-AsyncErrorChannel:
		if !strings.Contains(err.Error(), substring) {
			return fmt.Errorf("expected error containing %q, got %q", substring, err.Error())
		}
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for async error containing %q", substring)
	}
}

// GetTenantIDFromJWT extracts the tenant ID from the JWT token
func GetTenantIDFromJWT(token string) (string, error) {
	// Split the token into parts
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid JWT format")
	}

	// Decode the payload (second part)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	// Parse the JSON payload
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("failed to parse JWT claims: %w", err)
	}

	// Extract tenant ID from claims
	resourceOwnerID, ok := claims[config.JWTTenantIDClaim].(string)
	if !ok {
		return "", fmt.Errorf("resourceowner id not found in JWT claims")
	}

	// Generate UUID using SHA1 with NameSpaceOID
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(resourceOwnerID)).String(), nil
}

// CheckPermissions sends a request to the NATS API and checks if it results in a permissions violation
func checkPermissions(nc *nats.Conn, subject string) error {
	if nc.Status() != nats.CONNECTED {
		return fmt.Errorf("nats connection is not active")
	}

	fmt.Printf("checking permissions for subject: %s\n", subject)

	select {
	case <-AsyncErrorChannel:
	default:
	}

	msg, err := nc.Request(subject, nil, 2*time.Second)
	if err != nil {
		if strings.Contains(err.Error(), "nats: timeout") {
			fmt.Printf("timeout in permission check for '%s'. Checking async errors...\n", subject)

			select {
			case asyncErr := <-AsyncErrorChannel:
				if strings.Contains(asyncErr.Error(), "Permissions Violation") {
					return fmt.Errorf(" Permissions Violation detected on subject '%s'", subject)
				}
			case <-time.After(1 * time.Second):
				return fmt.Errorf("final timeout: no response from NATS for '%s'", subject)
			}
		}
		return fmt.Errorf("failed to request permissions check: %v", err)
	}

	if strings.Contains(string(msg.Data), "Permissions Violation") {
		return fmt.Errorf(" Permissions Violation detected on subject '%s'", subject)
	}

	fmt.Printf("permission check passed for subject: %s\n", subject)
	return nil
}

// CreateJetStreamConsumer creates a JetStream consumer with the given parameters
func CreateJetStreamConsumer(ctx context.Context, js jetstream.JetStream, nc *nats.Conn, streamName, consumerName, subject string) (jetstream.Consumer, error) {
	fmt.Printf("checking consumer creation permissions: stream=%s, consumer=%s, subject=%s\n", streamName, consumerName, subject)

	permissionCheck := fmt.Sprintf("$JS.API.CONSUMER.INFO.%s.%s", streamName, consumerName)
	err := checkPermissions(nc, permissionCheck)
	if err != nil {
		fmt.Printf("permission check failed: %v\n", err)
		return nil, err
	}

	select {
	case <-AsyncErrorChannel:
	default:
	}

	fmt.Printf("attempting to create consumer: %s\n", consumerName)
	consumer, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Name:              consumerName,
		Durable:           consumerName,
		AckPolicy:         jetstream.AckExplicitPolicy,
		DeliverPolicy:     jetstream.DeliverAllPolicy,
		FilterSubject:     subject,
		InactiveThreshold: 10 * time.Minute,
	})

	if err != nil && strings.Contains(err.Error(), "context deadline exceeded") {
		fmt.Printf(" JetStream API timeout. Checking async errors for possible 'Permissions Violation'...\n")

		select {
		case asyncErr := <-AsyncErrorChannel:
			if strings.Contains(asyncErr.Error(), "Permissions Violation") {
				return nil, fmt.Errorf(" Permissions Violation detected while creating consumer '%s'", consumerName)
			}
		case <-time.After(1 * time.Second):
			return nil, fmt.Errorf("final timeout: JetStream API did not respond while creating consumer '%s'", consumerName)
		}
	}

	return consumer, err
}

// DeleteJetStreamConsumer deletes a JetStream consumer
func DeleteJetStreamConsumer(ctx context.Context, js jetstream.JetStream, nc *nats.Conn, streamName, consumerName string) error {
	fmt.Printf("attempting to delete consumer: %s from stream %s\n", consumerName, streamName)

	permissionCheck := fmt.Sprintf("$JS.API.CONSUMER.INFO.%s.%s", streamName, consumerName)
	err := checkPermissions(nc, permissionCheck)
	if err != nil {
		fmt.Printf("permission check failed for consumer deletion: %v\n", err)
		return err
	}

	select {
	case <-AsyncErrorChannel:
	default:
	}

	consumer, err := js.Consumer(ctx, streamName, consumerName)
	if err != nil {
		if strings.Contains(err.Error(), "context deadline exceeded") {
			fmt.Printf("timeout while fetching consumer '%s'. Checking async errors...\n", consumerName)

			select {
			case asyncErr := <-AsyncErrorChannel:
				if strings.Contains(asyncErr.Error(), "Permissions Violation") {
					return fmt.Errorf(" Permissions Violation detected while trying to delete consumer '%s'", consumerName)
				}
			case <-time.After(1 * time.Second):
				return fmt.Errorf("final timeout: JetStream API did not respond while fetching consumer '%s'", consumerName)
			}
		}
		return fmt.Errorf("failed to fetch consumer '%s': %v", consumerName, err)
	}

	info, err := consumer.Info(ctx)
	if err != nil {
		fmt.Printf("failed to get info for consumer %s: %v\n", consumerName, err)
		return err
	}

	fmt.Printf("consumer exists. Name: %s, Durable: %s, Stream: %s\n", info.Config.Name, info.Config.Durable, info.Stream)

	if info.Config.Durable == "" {
		return fmt.Errorf("consumer %s is not durable and cannot be deleted", consumerName)
	}

	err = js.DeleteConsumer(ctx, streamName, info.Config.Durable)
	if err != nil {
		fmt.Printf("failed to delete consumer: %v\n", err)
		return err
	}

	fmt.Printf("consumer %s deleted successfully\n", consumerName)
	return nil
}

// CreateCoreNATSSubscription creates a core NATS subscription
func CreateCoreNATSSubscription(nc *nats.Conn, subject string) (*nats.Subscription, error) {
	return nc.SubscribeSync(subject)
}
