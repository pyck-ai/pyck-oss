package zitadel //nolint:testpackage // tests the unexported Seeder.ensureLoginEventAction

import (
	"context"
	"testing"

	action_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/action/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/pyck-ai/pyck/backend/bootstrap/internal/exporters"
)

const loginEndpoint = "http://management:8082/webhook/zitadel/login"

// fakeActionClient stubs the ActionServiceClient methods ensureLoginEventAction
// uses; the embedded interface satisfies the rest (never called) so the struct
// still implements the client.
type fakeActionClient struct {
	action_pb.ActionServiceClient

	existingTarget *action_pb.Target      // returned by ListTargets when non-nil
	executions     []*action_pb.Execution // returned by ListExecutions
	newTargetID    string                 // Id returned by CreateTarget

	createCalls        int
	deleteCalls        int
	updateCalls        int
	lastUpdateEndpoint string
	setEvents          []string // events bound to a target (non-empty targets)
	removedEvents      []string // events whose Execution was removed (no targets)
	lastTargets        []string // targets of the last binding SetExecution
}

// ListTargets models real Zitadel: a missing Target yields an empty list (not
// a NotFound error), exercising findTargetByName's "iterate, none match" path.
func (f *fakeActionClient) ListTargets(_ context.Context, _ *action_pb.ListTargetsRequest, _ ...grpc.CallOption) (*action_pb.ListTargetsResponse, error) {
	if f.existingTarget == nil {
		return &action_pb.ListTargetsResponse{}, nil
	}
	return &action_pb.ListTargetsResponse{Targets: []*action_pb.Target{f.existingTarget}}, nil
}

func (f *fakeActionClient) CreateTarget(_ context.Context, _ *action_pb.CreateTargetRequest, _ ...grpc.CallOption) (*action_pb.CreateTargetResponse, error) {
	f.createCalls++
	return &action_pb.CreateTargetResponse{Id: f.newTargetID, SigningKey: "minted-key"}, nil
}

func (f *fakeActionClient) DeleteTarget(_ context.Context, _ *action_pb.DeleteTargetRequest, _ ...grpc.CallOption) (*action_pb.DeleteTargetResponse, error) {
	f.deleteCalls++
	return &action_pb.DeleteTargetResponse{}, nil
}

func (f *fakeActionClient) UpdateTarget(_ context.Context, in *action_pb.UpdateTargetRequest, _ ...grpc.CallOption) (*action_pb.UpdateTargetResponse, error) {
	f.updateCalls++
	f.lastUpdateEndpoint = in.GetEndpoint()
	return &action_pb.UpdateTargetResponse{}, nil
}

func (f *fakeActionClient) ListExecutions(_ context.Context, _ *action_pb.ListExecutionsRequest, _ ...grpc.CallOption) (*action_pb.ListExecutionsResponse, error) {
	return &action_pb.ListExecutionsResponse{Executions: f.executions}, nil
}

func (f *fakeActionClient) SetExecution(_ context.Context, in *action_pb.SetExecutionRequest, _ ...grpc.CallOption) (*action_pb.SetExecutionResponse, error) {
	event := in.GetCondition().GetEvent().GetEvent()
	if len(in.GetTargets()) == 0 {
		f.removedEvents = append(f.removedEvents, event)
	} else {
		f.setEvents = append(f.setEvents, event)
		f.lastTargets = in.GetTargets()
	}
	return &action_pb.SetExecutionResponse{}, nil
}

// fakeExporter lets a test control whether the signing key is reported present
// at its destination (drives the self-healing guard) and records exports.
type fakeExporter struct {
	exists      bool
	exportCalls int
}

func (f *fakeExporter) Exists(_ context.Context, _ exporters.Export) (bool, error) {
	return f.exists, nil
}

func (f *fakeExporter) Export(_ context.Context, _ string, _ exporters.Export) error {
	f.exportCalls++
	return nil
}

// loginSeeder returns a Seeder whose key-guard reports keyPresent, plus the
// guard/export lists and the exporter (to assert re-export).
func loginSeeder(keyPresent bool) (*Seeder, *fakeExporter, []*exporters.Export, []*exporters.Export) {
	fe := &fakeExporter{exists: keyPresent}
	s := &Seeder{exporters: exporters.NewExporterRegistry(map[exporters.ExportType]exporters.Exporter{
		exporters.ExportTypeEnv: fe,
	})}
	guard := []*exporters.Export{{Type: exporters.ExportTypeEnv, Name: "PYCK_ZITADEL_LOGIN_ACTION_SIGNING_KEY", File: "bootstrap.env"}}
	export := []*exporters.Export{{Type: exporters.ExportTypeEnv, Name: "PYCK_ZITADEL_LOGIN_ACTION_SIGNING_KEY", File: "bootstrap.env", Field: "signing_key"}}
	return s, fe, guard, export
}

func eventCondition(event string) *action_pb.Condition {
	return &action_pb.Condition{
		ConditionType: &action_pb.Condition_Event{
			Event: &action_pb.EventExecution{
				Condition: &action_pb.EventExecution_Event{Event: event},
			},
		},
	}
}

// presentTarget returns an existing Target whose Endpoint/Timeout already match
// the desired values, so the reconcile path performs no UpdateTarget.
func presentTarget() *action_pb.Target {
	return &action_pb.Target{
		Id:       "existing-target",
		Name:     PyckLoginEventTargetName,
		Endpoint: loginEndpoint,
		Timeout:  durationpb.New(pyckActionTimeoutV2),
	}
}

// assertExecutionsSet verifies SetExecution ran once per loginEventTypes, for
// exactly those event types, all bound to wantTarget.
func assertExecutionsSet(t *testing.T, fc *fakeActionClient, wantTarget string) {
	t.Helper()
	if len(fc.setEvents) != len(loginEventTypes) {
		t.Fatalf("SetExecution (bind) calls = %d, want %d", len(fc.setEvents), len(loginEventTypes))
	}
	got := make(map[string]bool, len(fc.setEvents))
	for _, e := range fc.setEvents {
		got[e] = true
	}
	for _, want := range loginEventTypes {
		if !got[want] {
			t.Errorf("missing SetExecution for event %q", want)
		}
	}
	if len(fc.lastTargets) != 1 || fc.lastTargets[0] != wantTarget {
		t.Errorf("SetExecution targets = %v, want [%s]", fc.lastTargets, wantTarget)
	}
}

func TestEnsureLoginEventAction_CreatesTargetWhenAbsent(t *testing.T) {
	t.Parallel()
	s, fe, guard, export := loginSeeder(false) // no key yet
	fc := &fakeActionClient{newTargetID: "new-target"}

	if err := s.ensureLoginEventAction(t.Context(), fc, "http://management:8082", guard, export); err != nil {
		t.Fatalf("ensureLoginEventAction: %v", err)
	}
	if fc.createCalls != 1 {
		t.Errorf("CreateTarget calls = %d, want 1", fc.createCalls)
	}
	if fc.deleteCalls != 0 || fc.updateCalls != 0 {
		t.Errorf("delete=%d update=%d, want 0/0 (nothing to delete/update on first create)", fc.deleteCalls, fc.updateCalls)
	}
	if fe.exportCalls == 0 {
		t.Error("expected the minted signing key to be exported")
	}
	assertExecutionsSet(t, fc, "new-target")
}

func TestEnsureLoginEventAction_ReconcilesWhenTargetPresent(t *testing.T) {
	t.Parallel()
	s, fe, guard, export := loginSeeder(true) // key present → no rotation
	fc := &fakeActionClient{existingTarget: presentTarget()}

	if err := s.ensureLoginEventAction(t.Context(), fc, "http://management:8082", guard, export); err != nil {
		t.Fatalf("ensureLoginEventAction: %v", err)
	}
	if fc.createCalls != 0 || fc.deleteCalls != 0 || fc.updateCalls != 0 {
		t.Errorf("create=%d delete=%d update=%d, want 0/0/0 (steady state)", fc.createCalls, fc.deleteCalls, fc.updateCalls)
	}
	if fe.exportCalls != 0 {
		t.Errorf("export calls = %d, want 0 (key already present)", fe.exportCalls)
	}
	assertExecutionsSet(t, fc, "existing-target")
}

func TestEnsureLoginEventAction_ReconcilesEndpointWhenChanged(t *testing.T) {
	t.Parallel()
	s, _, guard, export := loginSeeder(true)
	stale := presentTarget()
	stale.Endpoint = "http://old-host:9999/webhook/zitadel/login" // base URL moved
	fc := &fakeActionClient{existingTarget: stale}

	if err := s.ensureLoginEventAction(t.Context(), fc, "http://management:8082", guard, export); err != nil {
		t.Fatalf("ensureLoginEventAction: %v", err)
	}
	if fc.updateCalls != 1 {
		t.Fatalf("UpdateTarget calls = %d, want 1 (endpoint changed)", fc.updateCalls)
	}
	if fc.lastUpdateEndpoint != loginEndpoint {
		t.Errorf("updated endpoint = %q, want %q", fc.lastUpdateEndpoint, loginEndpoint)
	}
	if fc.createCalls != 0 {
		t.Errorf("CreateTarget calls = %d, want 0 (no key rotation on endpoint change)", fc.createCalls)
	}
}

func TestEnsureLoginEventAction_SelfHealsWhenKeyMissing(t *testing.T) {
	t.Parallel()
	// Target survives but the exported key is gone → rotate to restore it.
	s, fe, guard, export := loginSeeder(false)
	fc := &fakeActionClient{existingTarget: presentTarget(), newTargetID: "rotated-target"}

	if err := s.ensureLoginEventAction(t.Context(), fc, "http://management:8082", guard, export); err != nil {
		t.Fatalf("ensureLoginEventAction: %v", err)
	}
	if fc.deleteCalls != 1 || fc.createCalls != 1 {
		t.Errorf("delete=%d create=%d, want 1/1 (rotate the Target)", fc.deleteCalls, fc.createCalls)
	}
	if fe.exportCalls == 0 {
		t.Error("expected the rotated signing key to be re-exported")
	}
	assertExecutionsSet(t, fc, "rotated-target")
}

func TestEnsureLoginEventAction_RemovesStaleExecution(t *testing.T) {
	t.Parallel()
	s, _, guard, export := loginSeeder(true)
	fc := &fakeActionClient{
		existingTarget: presentTarget(),
		executions: []*action_pb.Execution{
			// Stale: an event no longer in loginEventTypes, still on our Target.
			{Condition: eventCondition("user.human.otp.sms.check.succeeded"), Targets: []string{"existing-target"}},
			// Still desired → must NOT be removed.
			{Condition: eventCondition("user.human.password.check.succeeded"), Targets: []string{"existing-target"}},
			// Same stale-looking event but on a different Target → not ours.
			{Condition: eventCondition("user.human.otp.sms.check.succeeded"), Targets: []string{"other-target"}},
		},
	}

	if err := s.ensureLoginEventAction(t.Context(), fc, "http://management:8082", guard, export); err != nil {
		t.Fatalf("ensureLoginEventAction: %v", err)
	}
	if len(fc.removedEvents) != 1 || fc.removedEvents[0] != "user.human.otp.sms.check.succeeded" {
		t.Fatalf("removedEvents = %v, want [user.human.otp.sms.check.succeeded]", fc.removedEvents)
	}
	assertExecutionsSet(t, fc, "existing-target")
}

func TestEnsureLoginEventAction_RequiresWebhookURL(t *testing.T) {
	t.Parallel()
	s, _, guard, export := loginSeeder(true)
	if err := s.ensureLoginEventAction(t.Context(), &fakeActionClient{}, "", guard, export); err == nil {
		t.Fatal("expected error for empty webhookBaseURL")
	}
}
