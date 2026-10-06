package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporalconfig "go.temporal.io/server/common/config"
)

func TestRunsListenAdapter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		wanted  []string
		running []string
		want    bool
		wantErr bool
	}{
		{name: "empty list runs everywhere", wanted: nil, running: []string{"matching"}, want: true},
		{name: "blank entries count as empty", wanted: []string{"", " "}, running: []string{"matching"}, want: true},
		{name: "history only, history process", wanted: []string{"history"}, running: []string{"history"}, want: true},
		{name: "history only, matching process", wanted: []string{"history"}, running: []string{"matching"}, want: false},
		{name: "history only, frontend process", wanted: []string{"history"}, running: []string{"frontend"}, want: false},
		{
			name:    "process running several services including history",
			wanted:  []string{"history"},
			running: []string{"frontend", "history", "matching", "worker"},
			want:    true,
		},
		{
			name:    "process running several services without a listed one",
			wanted:  []string{"history"},
			running: []string{"frontend", "matching", "worker"},
			want:    false,
		},
		{name: "several listed, one matches", wanted: []string{"history", "worker"}, running: []string{"worker"}, want: true},
		{name: "entries are trimmed", wanted: []string{" history "}, running: []string{"history"}, want: true},
		{name: "internal-frontend is a service", wanted: []string{"internal-frontend"}, running: []string{"internal-frontend"}, want: true},
		{name: "unknown name", wanted: []string{"histroy"}, running: []string{"history"}, wantErr: true},
		{name: "unknown name next to a valid one", wanted: []string{"history", "nope"}, running: []string{"history"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := runsListenAdapter(tt.wanted, tt.running)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrUnknownAdapterService)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecideListenAdapter(t *testing.T) {
	t.Parallel()

	internal := []string{"internal-frontend"}
	history := []string{"history"}
	both := []string{"frontend", "internal-frontend"}

	tests := []struct {
		name    string
		wanted  []string
		running []string
		addr    string
		served  []int
		wantRun bool
		wantErr error
		skipped bool // run=false without error: a skip reason is returned
	}{
		{name: "selected, local, served", wanted: internal, running: internal, addr: ":7236", served: []int{7236}, wantRun: true},
		{name: "selected, local, not served", wanted: history, running: history, addr: ":7236", wantErr: ErrAdapterAddressNotServed},
		{name: "selected, local, other port served", wanted: internal, running: internal, addr: "localhost:7233", served: []int{7236}, wantErr: ErrAdapterAddressNotServed},
		{name: "empty, local, not served: skip", running: history, addr: ":7236", skipped: true},
		{name: "empty, local, served", running: internal, addr: ":7236", served: []int{7236}, wantRun: true},
		{name: "empty, all services in one process", running: []string{"frontend", "history", "matching", "worker"}, addr: "localhost:7233", served: []int{7233}, wantRun: true},
		{name: "selected, one of two ports served", wanted: both, running: both, addr: "127.0.0.1:7236", served: []int{7233, 7236}, wantRun: true},
		{name: "non-local address, selected, not served", wanted: history, running: history, addr: "pyck-temporal-internal-frontend:7236", wantRun: true},
		{name: "non-local address, empty, not served", running: history, addr: "host:7236", wantRun: true},
		{name: "non-local IP", wanted: history, running: history, addr: "10.0.0.5:7236", wantRun: true},
		{name: "localhost:7236", wanted: history, running: history, addr: "localhost:7236", wantErr: ErrAdapterAddressNotServed},
		{name: "LOCALHOST:7236", wanted: history, running: history, addr: "LOCALHOST:7236", wantErr: ErrAdapterAddressNotServed},
		{name: "127.0.0.1:7236", wanted: history, running: history, addr: "127.0.0.1:7236", wantErr: ErrAdapterAddressNotServed},
		{name: "127.1.2.3:7236", wanted: history, running: history, addr: "127.1.2.3:7236", wantErr: ErrAdapterAddressNotServed},
		{name: "[::1]:7236", wanted: history, running: history, addr: "[::1]:7236", wantErr: ErrAdapterAddressNotServed},
		{name: "0.0.0.0:7236", wanted: history, running: history, addr: "0.0.0.0:7236", wantErr: ErrAdapterAddressNotServed},
		{name: "[::]:7236", wanted: history, running: history, addr: "[::]:7236", wantErr: ErrAdapterAddressNotServed},
		{name: "host without port is not checked", wanted: history, running: history, addr: "localhost", wantRun: true},
		{name: "not selected: skip", wanted: internal, running: history, addr: ":7236", skipped: true},
		{name: "unknown name", wanted: []string{"histroy"}, running: history, addr: ":7236", wantErr: ErrUnknownAdapterService},
		{name: "blank entries count as empty", wanted: []string{" "}, running: history, addr: ":7236", skipped: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			run, reason, err := decideListenAdapter(tt.wanted, tt.running, tt.addr, tt.served)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.False(t, run)

				if errors.Is(tt.wantErr, ErrAdapterAddressNotServed) {
					assert.Contains(t, err.Error(), tt.addr)
					assert.Contains(t, err.Error(), strings.Join(tt.running, ", "))
					assert.Contains(t, err.Error(), "internal-frontend")
				}

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantRun, run)

			if tt.skipped {
				assert.NotEmpty(t, reason)
			} else {
				assert.Empty(t, reason)
			}
		})
	}
}

func TestWorkflowServicePorts(t *testing.T) {
	t.Parallel()

	cfg := &temporalconfig.Config{Services: map[string]temporalconfig.Service{
		"frontend":          {RPC: temporalconfig.RPC{GRPCPort: 7233}},
		"internal-frontend": {RPC: temporalconfig.RPC{GRPCPort: 7236}},
		"history":           {RPC: temporalconfig.RPC{GRPCPort: 7234}},
	}}

	assert.Equal(t, []int{7233, 7236}, workflowServicePorts(cfg, []string{"history", "frontend", "internal-frontend"}))
	assert.Equal(t, []int{7236}, workflowServicePorts(cfg, []string{"internal-frontend"}))
	assert.Empty(t, workflowServicePorts(cfg, []string{"history", "matching"}))
	assert.Empty(t, workflowServicePorts(cfg, []string{"worker"}))
}
