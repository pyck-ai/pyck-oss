package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

const stateFileName = ".stockfix-workers.json"

// workerBackend holds configuration for fly and/or k8s worker management.
// Both fields are optional; only the non-empty ones are acted on.
type workerBackend struct {
	flyApp    string // Fly.io application name
	k8sDeploy string // "namespace/deploy-name"
}

func (wb workerBackend) active() bool { return wb.flyApp != "" || wb.k8sDeploy != "" }

// printStopCmds writes the commands that would be run on stop (dry-run helper).
func (wb workerBackend) printStopCmds(w io.Writer) {
	if wb.flyApp != "" {
		fmt.Fprintf(w, "  flyctl machine list --app %s --json   # get started machine IDs\n", wb.flyApp)
		fmt.Fprintf(w, "  flyctl machine stop <id> --app %s    # for each started machine\n", wb.flyApp)
	}
	if wb.k8sDeploy != "" {
		ns, deploy := splitK8sDeploy(wb.k8sDeploy)
		fmt.Fprintf(w, "  kubectl get deploy/%s -n %s -o jsonpath='{.spec.replicas}'  # save count\n", deploy, ns)
		fmt.Fprintf(w, "  kubectl scale deploy/%s -n %s --replicas=0\n", deploy, ns)
	}
}

// printStartCmds writes the commands that would be run on start (dry-run helper).
func (wb workerBackend) printStartCmds(w io.Writer) {
	if wb.flyApp != "" {
		fmt.Fprintf(w, "  flyctl machine start <id> --app %s  # for each previously stopped machine\n", wb.flyApp)
	}
	if wb.k8sDeploy != "" {
		ns, deploy := splitK8sDeploy(wb.k8sDeploy)
		fmt.Fprintf(w, "  kubectl scale deploy/%s -n %s --replicas=<saved>\n", deploy, ns)
	}
}

// stop stops all running workers, persisting state for later start.
func (wb workerBackend) stop(ctx context.Context, dryRun bool, w io.Writer) error {
	state := loadState()
	if wb.flyApp != "" {
		if err := wb.flyStop(ctx, dryRun, w, state); err != nil {
			return err
		}
	}
	if wb.k8sDeploy != "" {
		if err := wb.k8sStop(ctx, dryRun, w, state); err != nil {
			return err
		}
	}
	return saveState(state)
}

// start restores workers from persisted state.
func (wb workerBackend) start(ctx context.Context, dryRun bool, w io.Writer) error {
	state := loadState()
	if wb.flyApp != "" {
		if err := wb.flyStart(ctx, dryRun, w, state); err != nil {
			return err
		}
	}
	if wb.k8sDeploy != "" {
		if err := wb.k8sStart(ctx, dryRun, w, state); err != nil {
			return err
		}
	}
	return saveState(state)
}

// status prints current worker status (always read-only).
func (wb workerBackend) status(ctx context.Context, w io.Writer) error {
	if wb.flyApp != "" {
		fmt.Fprintf(w, "=== fly app: %s ===\n", wb.flyApp)
		out, err := runCmdOutput(ctx, "flyctl", "machine", "list", "--app", wb.flyApp)
		if err != nil {
			return fmt.Errorf("flyctl machine list: %w", err)
		}
		fmt.Fprintln(w, out)
	}
	if wb.k8sDeploy != "" {
		ns, deploy := splitK8sDeploy(wb.k8sDeploy)
		fmt.Fprintf(w, "=== k8s: %s / %s ===\n", ns, deploy)
		out, err := runCmdOutput(ctx, "kubectl", "get", "deploy/"+deploy, "-n", ns)
		if err != nil {
			return fmt.Errorf("kubectl get deploy: %w", err)
		}
		fmt.Fprintln(w, out)
	}
	state := loadState()
	if len(state.FlyMachines) > 0 || len(state.K8sReplicas) > 0 {
		fmt.Fprintln(w, "\n--- state file (.stockfix-workers.json) ---")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(state)
	}
	return nil
}

// ── fly helpers ──────────────────────────────────────────────────────────────

func (wb workerBackend) flyStop(ctx context.Context, dryRun bool, w io.Writer, state *WorkerState) error {
	machines, err := flyListMachines(ctx, wb.flyApp)
	if err != nil {
		return err
	}
	if state.FlyMachines == nil {
		state.FlyMachines = make(map[string][]string)
	}
	for _, m := range machines {
		if m.State != "started" && m.State != "running" {
			continue
		}
		args := []string{"machine", "stop", m.ID, "--app", wb.flyApp}
		if dryRun {
			fmt.Fprintf(w, "  [DRY RUN] flyctl %s\n", strings.Join(args, " "))
		} else {
			fmt.Fprintf(w, "  stopping machine %s…\n", m.ID)
			if err := runCmd(ctx, w, "flyctl", args...); err != nil {
				return err
			}
		}
		state.FlyMachines[wb.flyApp] = append(state.FlyMachines[wb.flyApp], m.ID)
	}
	return nil
}

func (wb workerBackend) flyStart(ctx context.Context, dryRun bool, w io.Writer, state *WorkerState) error {
	ids := state.FlyMachines[wb.flyApp]
	if len(ids) == 0 {
		fmt.Fprintf(w, "no stopped fly machines recorded for app %q; nothing to start\n", wb.flyApp)
		return nil
	}
	for _, id := range ids {
		args := []string{"machine", "start", id, "--app", wb.flyApp}
		if dryRun {
			fmt.Fprintf(w, "  [DRY RUN] flyctl %s\n", strings.Join(args, " "))
		} else {
			fmt.Fprintf(w, "  starting machine %s…\n", id)
			if err := runCmd(ctx, w, "flyctl", args...); err != nil {
				return err
			}
		}
	}
	delete(state.FlyMachines, wb.flyApp)
	return nil
}

func flyListMachines(ctx context.Context, app string) ([]flyMachineInfo, error) {
	out, err := runCmdOutput(ctx, "flyctl", "machine", "list", "--app", app, "--json")
	if err != nil {
		return nil, fmt.Errorf("flyctl machine list: %w", err)
	}
	var machines []flyMachineInfo
	if err := json.Unmarshal([]byte(out), &machines); err != nil {
		return nil, fmt.Errorf("parse flyctl output: %w", err)
	}
	return machines, nil
}

// ── k8s helpers ──────────────────────────────────────────────────────────────

func (wb workerBackend) k8sStop(ctx context.Context, dryRun bool, w io.Writer, state *WorkerState) error {
	ns, deploy := splitK8sDeploy(wb.k8sDeploy)
	// Save current replica count before scaling to zero.
	replicaStr, err := runCmdOutput(ctx, "kubectl", "get",
		"deploy/"+deploy, "-n", ns, "-o", "jsonpath={.spec.replicas}")
	if err != nil {
		return fmt.Errorf("kubectl get replicas: %w", err)
	}
	var replicas int
	fmt.Sscanf(strings.TrimSpace(replicaStr), "%d", &replicas)
	if state.K8sReplicas == nil {
		state.K8sReplicas = make(map[string]int)
	}
	state.K8sReplicas[wb.k8sDeploy] = replicas

	scaleArgs := []string{"scale", "deploy/" + deploy, "-n", ns, "--replicas=0"}
	if dryRun {
		fmt.Fprintf(w, "  [DRY RUN] kubectl %s  # was %d replica(s)\n", strings.Join(scaleArgs, " "), replicas)
	} else {
		fmt.Fprintf(w, "  scaling deploy/%s to 0 (was %d)…\n", deploy, replicas)
		if err := runCmd(ctx, w, "kubectl", scaleArgs...); err != nil {
			return err
		}
	}
	return nil
}

func (wb workerBackend) k8sStart(ctx context.Context, dryRun bool, w io.Writer, state *WorkerState) error {
	ns, deploy := splitK8sDeploy(wb.k8sDeploy)
	replicas, ok := state.K8sReplicas[wb.k8sDeploy]
	if !ok {
		fmt.Fprintf(w, "no saved replica count for %q; defaulting to 1\n", wb.k8sDeploy)
		replicas = 1
	}
	scaleArgs := []string{"scale", "deploy/" + deploy, "-n", ns,
		fmt.Sprintf("--replicas=%d", replicas)}
	if dryRun {
		fmt.Fprintf(w, "  [DRY RUN] kubectl %s\n", strings.Join(scaleArgs, " "))
	} else {
		fmt.Fprintf(w, "  scaling deploy/%s to %d…\n", deploy, replicas)
		if err := runCmd(ctx, w, "kubectl", scaleArgs...); err != nil {
			return err
		}
	}
	delete(state.K8sReplicas, wb.k8sDeploy)
	return nil
}

// ── state file ───────────────────────────────────────────────────────────────

func loadState() *WorkerState {
	data, err := os.ReadFile(stateFileName)
	if err != nil {
		return &WorkerState{}
	}
	var s WorkerState
	if err := json.Unmarshal(data, &s); err != nil {
		return &WorkerState{}
	}
	return &s
}

func saveState(s *WorkerState) error {
	if len(s.FlyMachines) == 0 && len(s.K8sReplicas) == 0 {
		_ = os.Remove(stateFileName)
		return nil
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(stateFileName, data, 0o600)
}

// ── exec helpers ─────────────────────────────────────────────────────────────

// runCmd runs a command, streaming stderr+stdout to w.
func runCmd(ctx context.Context, w io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

// runCmdOutput runs a command and returns its combined stdout as a string.
func runCmdOutput(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// splitK8sDeploy splits "namespace/deploy-name" into (ns, deploy).
func splitK8sDeploy(s string) (ns, deploy string) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "default", s
}

// flyStopCmd returns the flyctl args to stop one machine (used in tests).
func flyStopCmd(app, machineID string) []string {
	return []string{"flyctl", "machine", "stop", machineID, "--app", app}
}

// flyStartCmd returns the flyctl args to start one machine (used in tests).
func flyStartCmd(app, machineID string) []string {
	return []string{"flyctl", "machine", "start", machineID, "--app", app}
}

// k8sScaleCmd returns the kubectl args to scale a deployment (used in tests).
func k8sScaleCmd(ns, deploy string, replicas int) []string {
	return []string{"kubectl", "scale", "deploy/" + deploy, "-n", ns,
		fmt.Sprintf("--replicas=%d", replicas)}
}

// ── subcommand ───────────────────────────────────────────────────────────────

func cmdWorkers(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: stockfix workers stop|start|status [--fly-app <name>] [--k8s-deploy <ns/deploy>]")
		return 1
	}
	action, rest := args[0], args[1:]

	fs := flag.NewFlagSet("workers "+action, flag.ExitOnError)
	var (
		flyApp    string
		k8sDeploy string
		dryRun    bool
	)
	fs.StringVar(&flyApp, "fly-app", "", "Fly.io app name")
	fs.StringVar(&k8sDeploy, "k8s-deploy", "", "K8s namespace/deploy (e.g. prod/inventory-worker)")
	fs.BoolVar(&dryRun, "dry-run", false, "Print commands without executing")
	fs.Parse(rest)

	wb := workerBackend{flyApp: flyApp, k8sDeploy: k8sDeploy}
	if !wb.active() {
		fmt.Fprintln(os.Stderr, "error: at least one of --fly-app or --k8s-deploy is required")
		return 1
	}

	ctx := context.Background()
	switch action {
	case "stop":
		if err := wb.stop(ctx, dryRun, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	case "start":
		if err := wb.start(ctx, dryRun, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	case "status":
		if err := wb.status(ctx, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown workers action %q (want stop|start|status)\n", action)
		return 1
	}
	return 0
}
