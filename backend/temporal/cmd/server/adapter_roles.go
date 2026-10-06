package main

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	temporalconfig "go.temporal.io/server/common/config"
	"go.temporal.io/server/common/primitives"
	"go.temporal.io/server/temporal"
)

// ErrUnknownAdapterService is returned for a PYCK_EVENT_ADAPTER_SERVICES entry
// that is not a Temporal service name.
var ErrUnknownAdapterService = errors.New("unknown temporal service in PYCK_EVENT_ADAPTER_SERVICES")

// ErrAdapterAddressNotServed is returned when the LISTEN adapter is selected
// for this process but dials a local Temporal address that no service of this
// process serves.
var ErrAdapterAddressNotServed = errors.New("postgres LISTEN adapter dials a local temporal address this process does not serve")

// runsListenAdapter reports whether this process runs the PostgreSQL LISTEN
// adapter. adapterServices is PYCK_EVENT_ADAPTER_SERVICES and runningServices
// the Temporal services this process starts. An empty adapterServices means
// every process runs it. Every Temporal pod receives each database
// notification, so running it in one role keeps the pods from each publishing
// the same state change.
//
// An entry that is not a Temporal service name is an error, even when this
// process would not run that service, so a typo fails everywhere.
func runsListenAdapter(adapterServices, runningServices []string) (bool, error) {
	wanted := make([]string, 0, len(adapterServices))

	for _, name := range adapterServices {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		if !slices.Contains(temporal.Services, name) {
			return false, fmt.Errorf("%w: %q (valid: %s)",
				ErrUnknownAdapterService, name, strings.Join(temporal.Services, ", "))
		}

		wanted = append(wanted, name)
	}

	if len(wanted) == 0 {
		return true, nil
	}

	for _, name := range runningServices {
		if slices.Contains(wanted, name) {
			return true, nil
		}
	}

	return false, nil
}

// decideListenAdapter extends runsListenAdapter with the address check: the
// adapter dials adapterAddr (TEMPORAL_ADDRESS) before it listens, and for a
// local address that dial reaches this very process. servedPorts are the gRPC
// ports of the running services that serve the WorkflowService (frontend,
// internal-frontend; see workflowServicePorts).
//
// It returns run=true to start the adapter. With run=false, skipReason says
// why, for the caller to log. A process selected through
// PYCK_EVENT_ADAPTER_SERVICES whose local address is not served returns
// ErrAdapterAddressNotServed, because the operator asked for a listener there
// and it could never connect. With PYCK_EVENT_ADAPTER_SERVICES empty the
// process is skipped instead, since the setting did not ask for it. A
// non-local address is not checked.
func decideListenAdapter(
	adapterServices, runningServices []string,
	adapterAddr string,
	servedPorts []int,
) (run bool, skipReason string, err error) {
	selected, err := runsListenAdapter(adapterServices, runningServices)
	if err != nil {
		return false, "", err
	}

	if !selected {
		return false, "this process runs none of the services in PYCK_EVENT_ADAPTER_SERVICES", nil
	}

	port, local := localAddrPort(adapterAddr)
	if !local || slices.Contains(servedPorts, port) {
		return true, "", nil
	}

	explicit := slices.ContainsFunc(adapterServices, func(name string) bool {
		return strings.TrimSpace(name) != ""
	})
	if !explicit {
		return false, fmt.Sprintf("temporal address %q is local and no service of this process serves it", adapterAddr), nil
	}

	return false, "", fmt.Errorf(
		"%w: address %q (TEMPORAL_ADDRESS), running services [%s], "+
			"gRPC ports of running frontend/internal-frontend: %v; "+
			"run the listener on a role that serves that address "+
			"(e.g. PYCK_EVENT_ADAPTER_SERVICES=internal-frontend for :7236) "+
			"or set TEMPORAL_ADDRESS to a reachable frontend",
		ErrAdapterAddressNotServed, adapterAddr, strings.Join(runningServices, ", "), servedPorts)
}

// localAddrPort splits a host:port dial address. local is true when the host
// is empty, "localhost", a loopback IP or an unspecified IP (0.0.0.0, ::), so
// the dial stays on this pod. An address that is not host:port with a numeric
// port is reported as not local, and so is never checked.
func localAddrPort(addr string) (port int, local bool) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, false
	}

	port, err = strconv.Atoi(portStr)
	if err != nil {
		return 0, false
	}

	if host == "" || strings.EqualFold(host, "localhost") {
		return port, true
	}

	ip := net.ParseIP(host)

	return port, ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// workflowServicePorts returns the configured gRPC ports of the running
// services that serve the WorkflowService (frontend and internal-frontend).
func workflowServicePorts(cfg *temporalconfig.Config, runningServices []string) []int {
	var ports []int

	for _, name := range []string{string(primitives.FrontendService), string(primitives.InternalFrontendService)} {
		if !slices.Contains(runningServices, name) {
			continue
		}

		if port := cfg.Services[name].RPC.GRPCPort; port != 0 {
			ports = append(ports, port)
		}
	}

	return ports
}
