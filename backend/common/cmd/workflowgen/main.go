package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "embed"
)

var ErrModuleNotFound = errors.New("module declaration not found in go.mod")

//go:embed main.go.tmpl
var mainTemplate string

//go:embed rfw.rfwtxt.tmpl
var rfwTemplate string

//go:embed rfws.rfwtxt.tmpl
var rfwsTemplate string

var (
	verbose  bool
	dryRun   bool
	scaffold bool
)

func main() {
	flag.BoolVar(&verbose, "v", false, "verbose output")
	flag.BoolVar(&dryRun, "dry-run", false, "print generated output without modifying files")
	flag.BoolVar(&scaffold, "scaffold", false, "generate scaffold flutter/index.rfwtxt files from BPMN (off by default)")
	flag.Parse()
	args := flag.Args()

	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: workflowgen [-v] [--dry-run] <path>...\n")
		fmt.Fprintf(os.Stderr, "  -v        enable verbose output\n")
		fmt.Fprintf(os.Stderr, "  --dry-run  print generated output without modifying files\n")
		fmt.Fprintf(os.Stderr, "  --scaffold generate flutter/index.rfwtxt scaffolds from BPMN\n")
		fmt.Fprintf(os.Stderr, "  <path>... one or more directories or patterns\n")
		fmt.Fprintf(os.Stderr, "            use ./path/... for recursive search\n")
		fmt.Fprintf(os.Stderr, "            use ./path for non-recursive search\n")
		os.Exit(1)
	}

	// Run Go workflow import generation
	if err := runDefaultMode(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error generating Go workflows: %v\n", err)
		os.Exit(1)
	}

	// Run Flutter widget generation
	if err := runFlutterMode(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error generating Flutter widgets: %v\n", err)
		os.Exit(1)
	}
}

// runDefaultMode runs the original workflow import generation.
func runDefaultMode(args []string) error {
	// Get module path from go.mod
	modulePath, err := getModulePath()
	if err != nil {
		return fmt.Errorf("reading module path: %w", err)
	}

	// Create generator with OS file system
	gen := NewGenerator(osFileSystem{}, modulePath, mainTemplate)
	gen.dryRun = dryRun

	// Run the generator
	_, _, err = gen.Run(args)
	return err
}

// runFlutterMode stitches all .rfwtxt files into dist/widgets.rfwtxt.
// When --scaffold is set, it also generates flutter/index.rfwtxt scaffolds from BPMN.
func runFlutterMode(args []string) error {
	// Only generate scaffolds when explicitly requested
	if scaffold {
		if err := runScaffoldMode(args); err != nil {
			return err
		}
	}

	// Always stitch all .rfwtxt files together
	return runStitchOnlyMode(args)
}

// runScaffoldMode generates flutter/index.rfwtxt scaffold files from BPMN user tasks.
func runScaffoldMode(args []string) error {
	fs := osFileSystem{}
	bpmnParser := NewBPMNParser(fs, verbose)
	rfwGen := NewWidgetGenerator(fs, bpmnParser, rfwTemplate, verbose)
	rfwGen.dryRun = dryRun

	for _, searchPath := range args {
		if verbose {
			fmt.Printf("Processing %s for RFW scaffold generation...\n", searchPath)
		}

		bpmnFiles, err := bpmnParser.FindAllBPMNFiles(searchPath)
		if err != nil {
			return fmt.Errorf("finding BPMN files: %w", err)
		}

		totalAdded := 0
		totalRemoved := 0

		for _, bpmnFile := range bpmnFiles {
			rfwFile := getRfwPathFromBPMN(bpmnFile)

			if verbose {
				fmt.Printf("\nSyncing %s with %s...\n", rfwFile, bpmnFile)
			}

			added, removed, err := rfwGen.SyncWidgets(bpmnFile, rfwFile)
			if err != nil {
				return fmt.Errorf("syncing RFW components for %s: %w", bpmnFile, err)
			}

			totalAdded += added
			totalRemoved += removed
		}

		if verbose {
			fmt.Printf("\nScaffold summary: added %d, removed %d RFW component(s)\n", totalAdded, totalRemoved)
		}
	}

	return nil
}

// runStitchOnlyMode stitches existing RFW components into dist/widgets.rfwtxt.
func runStitchOnlyMode(args []string) error {
	fs := osFileSystem{}
	stitcher := NewStitcher(fs, rfwsTemplate, verbose)
	stitcher.dryRun = dryRun

	// Stitch from each search path
	for _, searchPath := range args {
		if verbose {
			fmt.Printf("\nStitching RFW components from %s...\n", searchPath)
		}

		outputPath := "dist/widgets.rfwtxt"
		if err := stitcher.StitchWidgets(searchPath, outputPath); err != nil {
			return fmt.Errorf("stitching RFW components: %w", err)
		}
	}

	return nil
}

// getRfwPathFromBPMN converts a BPMN file path to its corresponding RFW component file path.
// Example: workflows/101-unloading/101-unloading.bpmn -> workflows/101-unloading/flutter/index.rfwtxt
func getRfwPathFromBPMN(bpmnPath string) string {
	dir := filepath.Dir(bpmnPath)
	return filepath.Join(dir, "flutter", "index.rfwtxt")
}

// getModulePath reads the module path from go.mod
func getModulePath() (string, error) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module")), nil
		}
	}

	return "", ErrModuleNotFound
}
