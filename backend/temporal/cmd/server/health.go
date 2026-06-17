package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/urfave/cli/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// workflowServiceName is the gRPC health service name of the Temporal
// frontend, matching what `temporal operator cluster health` used to check
// before the CLI was removed from the temporalio/server image in 1.30.
const workflowServiceName = "temporal.api.workflowservice.v1.WorkflowService"

var ErrFrontendNotServing = errors.New("temporal frontend is not serving")

func checkHealth(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(c.Context, 10*time.Second)
	defer cancel()

	address := c.String("address")
	if strings.HasPrefix(address, ":") {
		address = "127.0.0.1" + address
	}

	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return cli.Exit(fmt.Errorf("failed to connect to %q: %w", address, err), 1)
	}

	defer conn.Close()

	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{
		Service: workflowServiceName,
	})
	if err != nil {
		return cli.Exit(fmt.Errorf("health check against %q failed: %w", address, err), 1)
	}

	if status := resp.GetStatus(); status != healthpb.HealthCheckResponse_SERVING {
		return cli.Exit(fmt.Errorf("%w: status %s at %q", ErrFrontendNotServing, status, address), 1)
	}

	fmt.Println("SERVING")

	return nil
}
