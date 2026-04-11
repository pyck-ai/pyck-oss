package nats

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go/micro"
	"github.com/nats-io/nkeys"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/tenant"
)

type authService struct {
	logger        log.Logger
	streamName    string
	issuerKeyPair nkeys.KeyPair
	authn         authn.Authenticator
}

func newAuthService(ctx context.Context, streamName string, authProvider authn.Authenticator, keyPair nkeys.KeyPair) *authService {
	return &authService{
		logger:        *log.ForContext(ctx),
		streamName:    streamName,
		authn:         authProvider,
		issuerKeyPair: keyPair,
	}
}

func (service *authService) Handle(r micro.Request) {
	logger := service.logger

	defer func() {
		if rec := recover(); rec != nil {
			logger.Error().
				Interface("panic", rec).
				Msg("Recovered from panic in auth handler")

			err := r.Error("500", fmt.Sprintf("Internal server error: %v", rec), nil)
			if err != nil {
				logger.Err(err).Msg("Error responding to request after panic")
			}
		}
	}()

	logger.Debug().
		Str("request_subject", r.Subject()).
		Str("request_reply", r.Reply()).
		Str("request_data", string(r.Data())).
		Msg("Auth handler received request")

	rc, err := jwt.DecodeAuthorizationRequestClaims(string(r.Data()))
	if err != nil {
		logger.Err(err).
			Str("raw_data", string(r.Data())).
			Msg("Failed to decode auth request")
		err = r.Error("500", fmt.Sprintf("Failed to decode auth request: %v", err.Error()), nil)
		if err != nil {
			logger.Err(err).Msg("Error responding to request")
		}
		return
	}
	logger.Debug().
		Interface("claims", rc).
		Interface("server", rc.Server).
		Interface("connect_options", rc.ConnectOptions).
		Strs("server_tags", rc.Server.Tags).
		Msg("Decoded auth request details")

	if rc.ConnectOptions.Token == "" {
		logger.Error().Msg("Missing token in request")
		err = r.Error("401", "Authentication failed: Missing user token", nil)
		if err != nil {
			logger.Err(err).Msg("Error responding to request")
		}
		return
	}
	logger.Debug().Msg("Found token in request")

	user, err := service.authn.Authenticate(context.TODO(), rc.ConnectOptions.Token)
	if err != nil {
		logger.Err(err).Msg("Token authorization failed")
		err = r.Error("401", fmt.Sprintf("Authentication failed: %v", err.Error()), nil)
		if err != nil {
			logger.Err(err).Msg("Error responding to request")
		}
		return
	}
	logger.Debug().Interface("user", user).Msg("User authorized successfully")

	userNkey := rc.UserNkey
	serverId := rc.Server.ID

	// TODO(michael): This is just a work-around for now. Ideally, this handler
	// should make use of the same middleware logic as the HTTP and GRPC
	// handlers do. This would also allow easier integration with RBAC.
	userCtx := authn.Context(context.TODO(), &user)
	tenantIDs, err := tenant.ParseHeaders(userCtx, http.Header(r.Headers()))
	if err != nil {
		err = r.Error("401", "Authentication failed: Missing tenant headers", nil)
		if err != nil {
			logger.Err(err).Msg("Error responding to request")
		}
		return
	}

	tenantCtx := tenant.Context(userCtx, tenantIDs...)
	tenantID := request.ForContext(tenantCtx).MutationTenantID()

	// Define the fixed consumer suffixes
	consumerSuffixes := []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}

	// Initialize permission pattern arrays
	allowedPubPatterns := []string{
		"$JS.API.INFO", // Allow JetStream API info requests
		fmt.Sprintf("%s.%s.>", service.streamName, tenantID), // Allow publishing to tenant's own subjects
		// REQUIRED for stream info
		"$JS.API.STREAM.NAMES",
		fmt.Sprintf("$JS.API.STREAM.INFO.%s", service.streamName),
	}

	allowedSubPatterns := []string{
		"_INBOX.>",     // Required for async JetStream responses
		"$JS.API.INFO", // Allow JetStream API info requests
		fmt.Sprintf("$JS.API.STREAM.INFO.%s", service.streamName), // Allow stream info requests
		fmt.Sprintf("%s.%s.>", service.streamName, tenantID),      // Allow subscribing to tenant's own subjects
	}

	// Add explicit patterns for each consumer name - build both pub and sub patterns in one loop
	for _, suffix := range consumerSuffixes {
		consumerName := fmt.Sprintf("%s--%s", tenantID, suffix)

		// Define all the patterns for this consumer
		consumerPatterns := []string{
			// Consumer creation
			fmt.Sprintf("$JS.API.CONSUMER.CREATE.%s.%s.%s.%s.>",
				service.streamName, consumerName, service.streamName, tenantID),

			// Consumer deletion
			fmt.Sprintf("$JS.API.CONSUMER.DELETE.%s.%s",
				service.streamName, consumerName),

			// Consumer operations - MSG.NEXT
			fmt.Sprintf("$JS.API.CONSUMER.MSG.NEXT.%s.%s",
				service.streamName, consumerName),

			// Consumer operations - INFO
			fmt.Sprintf("$JS.API.CONSUMER.INFO.%s.%s",
				service.streamName, consumerName),

			// Consumer operations - ACK
			fmt.Sprintf("$JS.ACK.%s.%s.>",
				service.streamName, consumerName),
		}

		// Add all patterns to both pub and sub arrays
		allowedPubPatterns = append(allowedPubPatterns, consumerPatterns...)
		allowedSubPatterns = append(allowedSubPatterns, consumerPatterns...)
	}

	// Add denied patterns -  Keep these, but they are less important now
	deniedPubPatterns := []string{
		"$JS.API.STREAM.LIST.>",
		"$JS.API.STREAM.NAMES.>", // Redundant, but harmless
		// Adding explicit deny patterns for consumer listing operations
		"$JS.API.CONSUMER.LIST.>",
		"$JS.API.CONSUMER.NAMES.>",
		fmt.Sprintf("%s.*.crud.>", service.streamName),
	}

	// Log the key components and patterns
	logger.Debug().
		Str("user_nkey", userNkey).
		Str("server_id", serverId).
		Str("tenant_id", tenantID.String()).
		Interface("connect_options", rc.ConnectOptions).
		Interface("server_info", rc.Server).
		Strs("allowed_pub_patterns", allowedPubPatterns).
		Strs("allowed_sub_patterns", allowedSubPatterns).
		Msg("Creating permissions")

	claims := jwt.NewUserClaims(userNkey)
	claims.Audience = "pyck" // Todo: make that dynamic from env files
	claims.Name = tenantID.String()

	userPermissions := jwt.Permissions{
		Pub: jwt.Permission{
			Allow: jwt.StringList(allowedPubPatterns),
			Deny:  jwt.StringList(deniedPubPatterns),
		},
		Sub: jwt.Permission{
			Allow: jwt.StringList(allowedSubPatterns),
			Deny:  jwt.StringList(deniedPubPatterns),
		},
	}

	claims.Permissions = userPermissions

	token, err := service.validateAndSign(claims)
	service.Respond(r, userNkey, serverId, token, err)
}

func (service *authService) Respond(req micro.Request, userNKey string, serverId string, userJWT string, err error) {
	logger := service.logger

	rc := jwt.NewAuthorizationResponseClaims(userNKey)
	rc.Audience = serverId
	rc.Jwt = userJWT
	if err != nil {
		rc.Error = err.Error()
	}

	// Decode the nested JWT to log its contents instead of the raw token
	var decodedUserClaims *jwt.UserClaims
	if userJWT != "" {
		var err error
		decodedUserClaims, err = jwt.DecodeUserClaims(userJWT)
		if err != nil {
			logger.Err(err).Msg("Failed to decode user JWT for logging")
		}
	}

	// Create a copy of response claims without the JWT for logging
	rcForLogging := *rc
	rcForLogging.Jwt = "[REDACTED]"

	// Log the response claims before encoding, but with decoded JWT instead of raw
	logger.Debug().
		Str("user_nkey", userNKey).
		Str("server_id", serverId).
		Interface("decoded_user_claims", decodedUserClaims). // Log decoded claims instead of raw JWT
		Interface("response_claims", rcForLogging).          // Use the copy with redacted JWT
		Msg("Preparing response claims")

	token, err := rc.Encode(service.issuerKeyPair)
	if err != nil {
		logger.Err(err).
			Interface("response_claims", rcForLogging). // Use the copy with redacted JWT
			Msg("error encoding response jwt:")
		return
	}

	// Verify the token can be decoded before sending
	_, err = jwt.DecodeAuthorizationResponseClaims(token)
	if err != nil {
		logger.Err(err).Msg("Generated token fails verification")
		return
	}

	logger.Debug().Msg("Sending verified response token")

	err = req.Respond([]byte(token))
	if err != nil {
		logger.Err(err).Msg("error responding to request")
	}
}

func (service *authService) validateAndSign(claims *jwt.UserClaims) (string, error) {
	// Validate the claims.
	vr := jwt.CreateValidationResults()
	claims.Validate(vr)
	if len(vr.Errors()) > 0 {
		return "", errors.Join(vr.Errors()...)
	}

	// Sign it with the issuer key since this is non-operator mode.
	return claims.Encode(service.issuerKeyPair)
}
