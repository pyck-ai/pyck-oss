package main

import (
	"strings"
	"testing"
)

// TestExtractComponentID tests the extractComponentID function with various inputs
func TestExtractComponentID(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple ID",
			input:    "ID: MyWidget",
			expected: "MyWidget",
		},
		{
			name:     "ID with spaces",
			input:    "ID:   MyWidget  ",
			expected: "MyWidget",
		},
		{
			name:     "ID with no space after colon",
			input:    "ID:MyWidget",
			expected: "MyWidget",
		},
		{
			name:     "ID with multiline text",
			input:    "ID: MyWidget\nSome other text\non multiple lines",
			expected: "MyWidget",
		},
		{
			name:     "ID with carriage return",
			input:    "ID: MyWidget\r\nMore text",
			expected: "MyWidget",
		},
		{
			name:     "ID in middle of text",
			input:    "Some prefix text\nID: MyWidget\nSome suffix",
			expected: "MyWidget",
		},
		{
			name:     "camelCase ID",
			input:    "ID: scanBarcodeWidget",
			expected: "scanBarcodeWidget",
		},
		{
			name:     "PascalCase ID",
			input:    "ID: ScanBarcodeWidget",
			expected: "ScanBarcodeWidget",
		},
		{
			name:     "ID with underscores",
			input:    "ID: scan_barcode_widget",
			expected: "scan_barcode_widget",
		},
		{
			name:     "ID with numbers",
			input:    "ID: Widget123",
			expected: "Widget123",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "no ID prefix",
			input:    "MyWidget",
			expected: "",
		},
		{
			name:     "ID: but no value",
			input:    "ID:",
			expected: "",
		},
		{
			name:     "ID: with only whitespace",
			input:    "ID:   \n  ",
			expected: "",
		},
		{
			name:     "lowercase id (should not match)",
			input:    "id: MyWidget",
			expected: "",
		},
		{
			name:     "ID with special characters",
			input:    "ID: My-Widget",
			expected: "My-Widget",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := extractComponentID(tc.input)
			if result != tc.expected {
				t.Errorf("Expected %q, got %q", tc.expected, result)
			}
		})
	}
}

// TestParseBPMN_ValidFile tests parsing a valid BPMN file with component IDs
func TestParseBPMN_ValidFile(t *testing.T) {
	t.Parallel()

	bpmnContent := `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_Scan" name="Scan Barcode"/>
    <bpmn:userTask id="Task_Confirm" name="Confirm Items"/>

    <bpmn:textAnnotation id="Annotation_1">
      <bpmn:text>ID: ScanBarcodeWidget</bpmn:text>
    </bpmn:textAnnotation>

    <bpmn:textAnnotation id="Annotation_2">
      <bpmn:text>ID: ConfirmItemsWidget</bpmn:text>
    </bpmn:textAnnotation>

    <bpmn:association sourceRef="Task_Scan" targetRef="Annotation_1"/>
    <bpmn:association sourceRef="Task_Confirm" targetRef="Annotation_2"/>
  </bpmn:process>
</bpmn:definitions>`

	fs := newMemFileSystem()
	fs.files["test.bpmn"] = []byte(bpmnContent)

	parser := NewBPMNParser(fs, false)
	tasks, err := parser.ParseBPMN("test.bpmn")
	if err != nil {
		t.Fatalf("ParseBPMN failed: %v", err)
	}

	if len(tasks) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(tasks))
	}

	// Check first task
	if tasks[0].ID != "ScanBarcodeWidget" {
		t.Errorf("Expected ID 'ScanBarcodeWidget', got %q", tasks[0].ID)
	}
	if tasks[0].Name != "Scan Barcode" {
		t.Errorf("Expected Name 'Scan Barcode', got %q", tasks[0].Name)
	}
	if tasks[0].BPMNFile != "test.bpmn" {
		t.Errorf("Expected BPMNFile 'test.bpmn', got %q", tasks[0].BPMNFile)
	}

	// Check second task
	if tasks[1].ID != "ConfirmItemsWidget" {
		t.Errorf("Expected ID 'ConfirmItemsWidget', got %q", tasks[1].ID)
	}
	if tasks[1].Name != "Confirm Items" {
		t.Errorf("Expected Name 'Confirm Items', got %q", tasks[1].Name)
	}
}

// TestParseBPMN_NoAnnotations tests BPMN file without any ID annotations
func TestParseBPMN_NoAnnotations(t *testing.T) {
	t.Parallel()

	bpmnContent := `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Some Task"/>
    <bpmn:userTask id="Task_2" name="Another Task"/>
  </bpmn:process>
</bpmn:definitions>`

	fs := newMemFileSystem()
	fs.files["test.bpmn"] = []byte(bpmnContent)

	parser := NewBPMNParser(fs, false)
	tasks, err := parser.ParseBPMN("test.bpmn")
	if err != nil {
		t.Fatalf("ParseBPMN failed: %v", err)
	}

	if len(tasks) != 0 {
		t.Errorf("Expected 0 tasks (no ID annotations), got %d", len(tasks))
	}
}

// TestParseBPMN_MixedAnnotations tests BPMN with both ID and non-ID annotations
func TestParseBPMN_MixedAnnotations(t *testing.T) {
	t.Parallel()

	bpmnContent := `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="With ID"/>
    <bpmn:userTask id="Task_2" name="Without ID"/>
    <bpmn:userTask id="Task_3" name="With Different Annotation"/>

    <bpmn:textAnnotation id="Ann_1">
      <bpmn:text>ID: ValidWidget</bpmn:text>
    </bpmn:textAnnotation>

    <bpmn:textAnnotation id="Ann_2">
      <bpmn:text>This is just a comment</bpmn:text>
    </bpmn:textAnnotation>

    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
    <bpmn:association sourceRef="Task_3" targetRef="Ann_2"/>
  </bpmn:process>
</bpmn:definitions>`

	fs := newMemFileSystem()
	fs.files["test.bpmn"] = []byte(bpmnContent)

	parser := NewBPMNParser(fs, false)
	tasks, err := parser.ParseBPMN("test.bpmn")
	if err != nil {
		t.Fatalf("ParseBPMN failed: %v", err)
	}

	// Only Task_1 should be included (has ID: annotation)
	if len(tasks) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(tasks))
	}

	if tasks[0].ID != "ValidWidget" {
		t.Errorf("Expected ID 'ValidWidget', got %q", tasks[0].ID)
	}
}

// TestParseBPMN_NoAssociation tests annotation without association
func TestParseBPMN_NoAssociation(t *testing.T) {
	t.Parallel()

	bpmnContent := `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="My Task"/>

    <bpmn:textAnnotation id="Ann_1">
      <bpmn:text>ID: MyWidget</bpmn:text>
    </bpmn:textAnnotation>

    <!-- No association! -->
  </bpmn:process>
</bpmn:definitions>`

	fs := newMemFileSystem()
	fs.files["test.bpmn"] = []byte(bpmnContent)

	parser := NewBPMNParser(fs, false)
	tasks, err := parser.ParseBPMN("test.bpmn")
	if err != nil {
		t.Fatalf("ParseBPMN failed: %v", err)
	}

	// Should find 0 tasks because annotation is not associated with any task
	if len(tasks) != 0 {
		t.Errorf("Expected 0 tasks (no association), got %d", len(tasks))
	}
}

// TestParseBPMN_InvalidXML tests error handling for invalid XML
func TestParseBPMN_InvalidXML(t *testing.T) {
	t.Parallel()

	bpmnContent := `This is not valid XML`

	fs := newMemFileSystem()
	fs.files["test.bpmn"] = []byte(bpmnContent)

	parser := NewBPMNParser(fs, false)
	_, err := parser.ParseBPMN("test.bpmn")

	if err == nil {
		t.Error("Expected error for invalid XML, got nil")
	}
}

// TestParseBPMN_MissingFile tests error handling for missing file
func TestParseBPMN_MissingFile(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()
	parser := NewBPMNParser(fs, false)
	_, err := parser.ParseBPMN("nonexistent.bpmn")

	if err == nil {
		t.Error("Expected error for missing file, got nil")
	}
}

// TestParseBPMN_DefinitionLevelAnnotations tests annotations at definition level
func TestParseBPMN_DefinitionLevelAnnotations(t *testing.T) {
	t.Parallel()

	bpmnContent := `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:textAnnotation id="Ann_Global">
    <bpmn:text>ID: GlobalWidget</bpmn:text>
  </bpmn:textAnnotation>

  <bpmn:association sourceRef="Task_1" targetRef="Ann_Global"/>

  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Global Task"/>
  </bpmn:process>
</bpmn:definitions>`

	fs := newMemFileSystem()
	fs.files["test.bpmn"] = []byte(bpmnContent)

	parser := NewBPMNParser(fs, false)
	tasks, err := parser.ParseBPMN("test.bpmn")
	if err != nil {
		t.Fatalf("ParseBPMN failed: %v", err)
	}

	if len(tasks) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(tasks))
	}

	if tasks[0].ID != "GlobalWidget" {
		t.Errorf("Expected ID 'GlobalWidget', got %q", tasks[0].ID)
	}
}

// TestFindAllBPMNFiles_Recursive tests finding BPMN files recursively
func TestFindAllBPMNFiles_Recursive(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()
	fs.files["workflows/w1/workflow.bpmn"] = []byte("content")
	fs.files["workflows/w2/workflow.bpmn"] = []byte("content")
	fs.files["workflows/nested/deep/workflow.bpmn"] = []byte("content")
	fs.files["workflows/w3/not-bpmn.xml"] = []byte("content")

	parser := NewBPMNParser(fs, false)
	files, err := parser.FindAllBPMNFiles("workflows/...")
	if err != nil {
		t.Fatalf("FindAllBPMNFiles failed: %v", err)
	}

	if len(files) != 3 {
		t.Errorf("Expected 3 BPMN files, got %d", len(files))
	}
}

// TestFindAllBPMNFiles_NonRecursive tests finding BPMN files non-recursively
func TestFindAllBPMNFiles_NonRecursive(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()
	fs.files["workflows/workflow1.bpmn"] = []byte("content")
	fs.files["workflows/workflow2.bpmn"] = []byte("content")
	fs.files["workflows/subdir/workflow3.bpmn"] = []byte("content")

	parser := NewBPMNParser(fs, false)
	files, err := parser.FindAllBPMNFiles("workflows")
	if err != nil {
		t.Fatalf("FindAllBPMNFiles failed: %v", err)
	}

	// Non-recursive should only find files directly in workflows/
	if len(files) != 2 {
		t.Errorf("Expected 2 BPMN files, got %d", len(files))
	}
}

// TestSyncWidgets_AddNew tests adding new widgets
func TestSyncWidgets_AddNew(t *testing.T) {
	t.Parallel()

	bpmnContent := `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="New Task"/>
    <bpmn:textAnnotation id="Ann_1">
      <bpmn:text>ID: NewWidget</bpmn:text>
    </bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`

	fs := newMemFileSystem()
	fs.files["workflow.bpmn"] = []byte(bpmnContent)
	fs.files["flutter/index.rfwtxt"] = []byte(`import core.widgets;
import local;

// Existing content
`)

	parser := NewBPMNParser(fs, false)
	gen := NewWidgetGenerator(fs, parser, rfwTemplate, false)

	added, removed, err := gen.SyncWidgets("workflow.bpmn", "flutter/index.rfwtxt")
	if err != nil {
		t.Fatalf("SyncWidgets failed: %v", err)
	}

	if added != 1 {
		t.Errorf("Expected 1 widget added, got %d", added)
	}

	if removed != 0 {
		t.Errorf("Expected 0 widgets removed, got %d", removed)
	}

	content := string(fs.files["flutter/index.rfwtxt"])
	if !strings.Contains(content, "widget NewWidget") {
		t.Error("Expected NewWidget to be added")
	}
}

// TestSyncWidgets_KeepOrphaned tests that orphaned widgets are kept in the file (not removed)
func TestSyncWidgets_KeepOrphaned(t *testing.T) {
	t.Parallel()

	bpmnContent := `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
  </bpmn:process>
</bpmn:definitions>`

	dartContent := `import core.widgets;

// User Task: Old Task
// Source: workflow.bpmn
widget OldWidget = ActivityContainer(
    child: ListView(
        children: [
          ActivityHeader(title: "Old Task"),
        ]
    )
);
`

	fs := newMemFileSystem()
	fs.files["workflow.bpmn"] = []byte(bpmnContent)
	fs.files["flutter/index.rfwtxt"] = []byte(dartContent)

	parser := NewBPMNParser(fs, false)
	gen := NewWidgetGenerator(fs, parser, rfwTemplate, false)

	added, orphaned, err := gen.SyncWidgets("workflow.bpmn", "flutter/index.rfwtxt")
	if err != nil {
		t.Fatalf("SyncWidgets failed: %v", err)
	}

	if added != 0 {
		t.Errorf("Expected 0 widgets added, got %d", added)
	}

	if orphaned != 1 {
		t.Errorf("Expected 1 orphaned widget, got %d", orphaned)
	}

	content := string(fs.files["flutter/index.rfwtxt"])

	// Orphaned widget should still be in the file (preserved for developer)
	if !strings.Contains(content, "OldWidget") {
		t.Error("OldWidget should still be in the file (orphaned widgets are kept)")
	}
}

// TestSyncWidgets_PreserveExisting tests preserving existing widget implementations
func TestSyncWidgets_PreserveExisting(t *testing.T) {
	t.Parallel()

	bpmnContent := `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="My Task"/>
    <bpmn:textAnnotation id="Ann_1">
      <bpmn:text>ID: MyWidget</bpmn:text>
    </bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`

	dartContent := `import core.widgets;

// User Task: My Task
// Source: workflow.bpmn
widget MyWidget = ActivityContainer(
    child: ListView(
        children: [
          ActivityHeader(title: "My Task"),
          CustomImplementation(data: "important"),
          ComplexLogic(),
        ]
    )
);
`

	fs := newMemFileSystem()
	fs.files["workflow.bpmn"] = []byte(bpmnContent)
	fs.files["flutter/index.rfwtxt"] = []byte(dartContent)

	parser := NewBPMNParser(fs, false)
	gen := NewWidgetGenerator(fs, parser, rfwTemplate, false)

	added, removed, err := gen.SyncWidgets("workflow.bpmn", "flutter/index.rfwtxt")
	if err != nil {
		t.Fatalf("SyncWidgets failed: %v", err)
	}

	if added != 0 {
		t.Errorf("Expected 0 widgets added, got %d", added)
	}

	if removed != 0 {
		t.Errorf("Expected 0 widgets removed, got %d", removed)
	}

	content := string(fs.files["flutter/index.rfwtxt"])
	// Custom implementation should be preserved
	if !strings.Contains(content, "CustomImplementation") {
		t.Error("CustomImplementation should be preserved")
	}
	if !strings.Contains(content, "ComplexLogic") {
		t.Error("ComplexLogic should be preserved")
	}
}

// TestSyncWidgets_CreateNewFile tests creating a new Dart file
func TestSyncWidgets_CreateNewFile(t *testing.T) {
	t.Parallel()

	bpmnContent := `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="First Task"/>
    <bpmn:textAnnotation id="Ann_1">
      <bpmn:text>ID: FirstWidget</bpmn:text>
    </bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`

	fs := newMemFileSystem()
	fs.files["workflow.bpmn"] = []byte(bpmnContent)

	parser := NewBPMNParser(fs, false)
	gen := NewWidgetGenerator(fs, parser, rfwTemplate, false)

	added, _, err := gen.SyncWidgets("workflow.bpmn", "flutter/index.rfwtxt")
	if err != nil {
		t.Fatalf("SyncWidgets failed: %v", err)
	}

	if added != 1 {
		t.Errorf("Expected 1 widget added, got %d", added)
	}

	// File should be created
	content, exists := fs.files["flutter/index.rfwtxt"]
	if !exists {
		t.Fatal("Dart file should have been created")
	}

	if !strings.Contains(string(content), "widget FirstWidget") {
		t.Error("FirstWidget should be in the new file")
	}
}

// TestStitchWidgets_CombineMultiple tests stitching multiple widget files
func TestStitchWidgets_CombineMultiple(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()

	// Create multiple widget files with BPMN for metadata
	fs.files["workflows/101-unloading/101-unloading.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Widget 1"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: Widget1</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`)
	fs.files["workflows/101-unloading/mobile/widget1.rfwtxt"] = []byte(`import core.widgets;

widget Widget1 = ActivityContainer(
    child: ListView(children: [])
);
`)

	fs.files["workflows/102-receiving/102-receiving.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Widget 2"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: Widget2</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`)
	fs.files["workflows/102-receiving/mobile/widget1.rfwtxt"] = []byte(`import core.widgets;

widget Widget2 = ActivityContainer(
    child: ListView(children: [])
);
`)

	stitcher := NewStitcher(fs, rfwsTemplate, false)
	err := stitcher.StitchWidgets("workflows/...", "dist/widgets.rfwtxt")
	if err != nil {
		t.Fatalf("StitchWidgets failed: %v", err)
	}

	content, exists := fs.files["dist/widgets.rfwtxt"]
	if !exists {
		t.Fatal("Stitched file should have been created")
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "Widget1") {
		t.Error("Widget1 should be in stitched file")
	}
	if !strings.Contains(contentStr, "Widget2") {
		t.Error("Widget2 should be in stitched file")
	}
}

// TestStitchWidgets_DeduplicateImports tests import deduplication
func TestStitchWidgets_DeduplicateImports(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()

	fs.files["workflows/w1/w1.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="W1"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: W1</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`)
	fs.files["workflows/w1/mobile/widget1.rfwtxt"] = []byte(`import core.widgets;
import local;

widget W1 = ActivityContainer(child: ListView(children: []));
`)

	fs.files["workflows/w2/w2.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="W2"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: W2</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`)
	fs.files["workflows/w2/mobile/widget1.rfwtxt"] = []byte(`import core.widgets;
import local;

widget W2 = ActivityContainer(child: ListView(children: []));
`)

	stitcher := NewStitcher(fs, rfwsTemplate, false)
	err := stitcher.StitchWidgets("workflows/...", "dist/widgets.rfwtxt")
	if err != nil {
		t.Fatalf("StitchWidgets failed: %v", err)
	}

	content := string(fs.files["dist/widgets.rfwtxt"])

	// Count occurrences of imports (should only appear once in the output)
	coreCount := strings.Count(content, "import core.widgets")
	localCount := strings.Count(content, "import local")

	if coreCount != 1 {
		t.Errorf("Expected 'import core.widgets' to appear once, found %d times", coreCount)
	}
	if localCount != 1 {
		t.Errorf("Expected 'import local' to appear once, found %d times", localCount)
	}
}

// TestStitchWidgets_SortedOutput tests that output is sorted
func TestStitchWidgets_SortedOutput(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()

	// Create files in non-alphabetical order with BPMN for metadata
	fs.files["workflows/z-last/z.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Z"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: ZWidget</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`)
	fs.files["workflows/z-last/mobile/widget1.rfwtxt"] = []byte(`widget ZWidget = ActivityContainer(child: ListView(children: []));`)

	fs.files["workflows/a-first/a.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="A"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: AWidget</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`)
	fs.files["workflows/a-first/mobile/widget1.rfwtxt"] = []byte(`widget AWidget = ActivityContainer(child: ListView(children: []));`)

	fs.files["workflows/m-middle/m.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="M"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: MWidget</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`)
	fs.files["workflows/m-middle/mobile/widget1.rfwtxt"] = []byte(`widget MWidget = ActivityContainer(child: ListView(children: []));`)

	stitcher := NewStitcher(fs, rfwsTemplate, false)
	err := stitcher.StitchWidgets("workflows/...", "dist/widgets.rfwtxt")
	if err != nil {
		t.Fatalf("StitchWidgets failed: %v", err)
	}

	content := string(fs.files["dist/widgets.rfwtxt"])

	// Check order: a-first should come before m-middle, which should come before z-last
	aPos := strings.Index(content, "a-first")
	mPos := strings.Index(content, "m-middle")
	zPos := strings.Index(content, "z-last")

	if aPos == -1 || mPos == -1 || zPos == -1 {
		t.Fatal("All workflow names should be in output")
	}

	if aPos >= mPos || mPos >= zPos {
		t.Error("Workflows should be sorted alphabetically")
	}
}

// TestStitchWidgets_EmptyDirectory tests stitching with no widget files
func TestStitchWidgets_EmptyDirectory(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()
	stitcher := NewStitcher(fs, rfwsTemplate, false)

	err := stitcher.StitchWidgets("workflows/...", "dist/widgets.rfwtxt")
	// Should not error, just not create output file
	if err != nil {
		t.Fatalf("StitchWidgets should handle empty directory: %v", err)
	}
}

// TestStitchWidgets_GeneratedHeader tests that generated header is correct
func TestStitchWidgets_GeneratedHeader(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()
	fs.files["workflows/w1/w1.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="W1"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: W1</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`)
	fs.files["workflows/w1/mobile/widget1.rfwtxt"] = []byte(`widget W1 = ActivityContainer(child: ListView(children: []));`)

	stitcher := NewStitcher(fs, rfwsTemplate, false)
	err := stitcher.StitchWidgets("workflows/...", "dist/widgets.rfwtxt")
	if err != nil {
		t.Fatalf("StitchWidgets failed: %v", err)
	}

	content := string(fs.files["dist/widgets.rfwtxt"])

	// Check for the generated header
	if !strings.Contains(content, "Code generated by pyck-ai/backend/workflowgen, DO NOT EDIT") {
		t.Error("Expected generated header not found")
	}

	// Ensure no timestamp is present
	if strings.Contains(content, "Timestamp:") {
		t.Error("Timestamp should not be in generated file")
	}

	// Check new Source format (just "Source:" with rfwFile path)
	if !strings.Contains(content, "// Source:") {
		t.Error("Expected 'Source:' comment not found")
	}
	if strings.Contains(content, "Source BPMN:") {
		t.Error("Old 'Source BPMN:' format should not be present")
	}
	if strings.Contains(content, "Source RFW:") {
		t.Error("Old 'Source RFW:' format should not be present")
	}
}

// TestWidgetGenerator_DryRun tests dry-run mode
func TestWidgetGenerator_DryRun(t *testing.T) {
	t.Parallel()

	bpmnContent := `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Test Task"/>
    <bpmn:textAnnotation id="Ann_1">
      <bpmn:text>ID: TestWidget</bpmn:text>
    </bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`

	fs := newMemFileSystem()
	fs.files["workflow.bpmn"] = []byte(bpmnContent)

	parser := NewBPMNParser(fs, false)
	gen := NewWidgetGenerator(fs, parser, rfwTemplate, false)
	gen.dryRun = true

	added, _, err := gen.SyncWidgets("workflow.bpmn", "flutter/index.rfwtxt")
	if err != nil {
		t.Fatalf("SyncWidgets failed: %v", err)
	}

	if added != 1 {
		t.Errorf("Expected 1 widget to be detected as added, got %d", added)
	}

	// In dry-run mode, file should NOT be created
	if _, exists := fs.files["flutter/index.rfwtxt"]; exists {
		t.Error("File should not be created in dry-run mode")
	}
}

// TestFullWorkflow_Integration tests the complete workflow end-to-end
func TestFullWorkflow_Integration(t *testing.T) {
	t.Parallel()

	// Setup: Create BPMN files with component IDs
	bpmn1 := `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Scan Barcode"/>
    <bpmn:textAnnotation id="Ann_1">
      <bpmn:text>ID: ScanWidget</bpmn:text>
    </bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`

	bpmn2 := `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_2">
    <bpmn:userTask id="Task_2" name="Confirm Items"/>
    <bpmn:textAnnotation id="Ann_2">
      <bpmn:text>ID: ConfirmWidget</bpmn:text>
    </bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_2" targetRef="Ann_2"/>
  </bpmn:process>
</bpmn:definitions>`

	fs := newMemFileSystem()
	fs.files["workflows/101-unloading/101-unloading.bpmn"] = []byte(bpmn1)
	fs.files["workflows/102-receiving/102-receiving.bpmn"] = []byte(bpmn2)

	// Step 1: Parse BPMN and generate widgets (scaffold mode generates flutter/index.rfwtxt)
	parser := NewBPMNParser(fs, false)
	gen := NewWidgetGenerator(fs, parser, rfwTemplate, false)

	added1, _, err := gen.SyncWidgets(
		"workflows/101-unloading/101-unloading.bpmn",
		"workflows/101-unloading/flutter/index.rfwtxt",
	)
	if err != nil {
		t.Fatalf("SyncWidgets failed for workflow 1: %v", err)
	}
	if added1 != 1 {
		t.Errorf("Expected 1 widget added for workflow 1, got %d", added1)
	}

	added2, _, err := gen.SyncWidgets(
		"workflows/102-receiving/102-receiving.bpmn",
		"workflows/102-receiving/flutter/index.rfwtxt",
	)
	if err != nil {
		t.Fatalf("SyncWidgets failed for workflow 2: %v", err)
	}
	if added2 != 1 {
		t.Errorf("Expected 1 widget added for workflow 2, got %d", added2)
	}

	// Step 2: Stitch widgets together (finds all .rfwtxt files)
	stitcher := NewStitcher(fs, rfwsTemplate, false)
	err = stitcher.StitchWidgets("workflows/...", "dist/widgets.rfwtxt")
	if err != nil {
		t.Fatalf("StitchWidgets failed: %v", err)
	}

	// Verify: Check that stitched file contains both widgets
	stitched := string(fs.files["dist/widgets.rfwtxt"])
	if !strings.Contains(stitched, "ScanWidget") {
		t.Error("Stitched file should contain ScanWidget")
	}
	if !strings.Contains(stitched, "ConfirmWidget") {
		t.Error("Stitched file should contain ConfirmWidget")
	}
	if !strings.Contains(stitched, "101-unloading") {
		t.Error("Stitched file should reference workflow 101-unloading")
	}
	if !strings.Contains(stitched, "102-receiving") {
		t.Error("Stitched file should reference workflow 102-receiving")
	}

	// Step 3: Modify BPMN (remove one widget's user task)
	bpmn1Modified := `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
  </bpmn:process>
</bpmn:definitions>`
	fs.files["workflows/101-unloading/101-unloading.bpmn"] = []byte(bpmn1Modified)

	// Sync again (ScanWidget becomes orphaned but is kept in the file)
	_, orphaned, err := gen.SyncWidgets(
		"workflows/101-unloading/101-unloading.bpmn",
		"workflows/101-unloading/flutter/index.rfwtxt",
	)
	if err != nil {
		t.Fatalf("SyncWidgets failed after modification: %v", err)
	}
	if orphaned != 1 {
		t.Errorf("Expected 1 orphaned widget, got %d", orphaned)
	}

	// Stitch again
	err = stitcher.StitchWidgets("workflows/...", "dist/widgets.rfwtxt")
	if err != nil {
		t.Fatalf("StitchWidgets failed after modification: %v", err)
	}

	// Verify: ScanWidget should still be present (orphan filtering removed),
	// ConfirmWidget should also remain
	stitchedAfter := string(fs.files["dist/widgets.rfwtxt"])
	if !strings.Contains(stitchedAfter, "ScanWidget") {
		t.Error("ScanWidget should still be in stitched file (orphan filtering removed)")
	}
	if !strings.Contains(stitchedAfter, "ConfirmWidget") {
		t.Error("ConfirmWidget should still be in stitched file")
	}
}

// TestStitchWidgets_DeduplicateIdenticalWidgets tests that identical widgets across workflows are deduplicated silently
func TestStitchWidgets_DeduplicateIdenticalWidgets(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()

	// Two workflows with identical widget (same ID and same content)
	identicalWidget := `widget SharedWidget = ActivityContainer(
    child: ListView(children: [])
);`

	// Both BPMNs reference the same widget ID via annotations
	sharedBPMN := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Shared"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: SharedWidget</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`)

	fs.files["workflows/w1/w1.bpmn"] = sharedBPMN
	fs.files["workflows/w1/mobile/widget1.rfwtxt"] = []byte(identicalWidget)

	fs.files["workflows/w2/w2.bpmn"] = sharedBPMN
	fs.files["workflows/w2/mobile/widget1.rfwtxt"] = []byte(identicalWidget)

	stitcher := NewStitcher(fs, rfwsTemplate, false)
	err := stitcher.StitchWidgets("workflows/...", "dist/widgets.rfwtxt")
	if err != nil {
		t.Fatalf("StitchWidgets failed: %v", err)
	}

	content := string(fs.files["dist/widgets.rfwtxt"])

	// Widget should appear exactly once
	count := strings.Count(content, "widget SharedWidget")
	if count != 1 {
		t.Errorf("Expected SharedWidget to appear once, found %d times", count)
	}
}

// TestStitchWidgets_WarnOnConflictingWidgets tests that conflicting widgets emit a warning and first one wins
func TestStitchWidgets_WarnOnConflictingWidgets(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()

	// Both BPMNs reference the same widget ID via annotations
	conflictBPMN := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Conflict"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: ConflictWidget</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`)

	// Two workflows with same widget ID but different content
	fs.files["workflows/a-first/a.bpmn"] = conflictBPMN
	fs.files["workflows/a-first/mobile/widget1.rfwtxt"] = []byte(`widget ConflictWidget = ActivityContainer(
    child: ListView(children: [Text(text: "First version")])
);`)

	fs.files["workflows/b-second/b.bpmn"] = conflictBPMN
	fs.files["workflows/b-second/mobile/widget1.rfwtxt"] = []byte(`widget ConflictWidget = ActivityContainer(
    child: ListView(children: [Text(text: "Second version")])
);`)

	stitcher := NewStitcher(fs, rfwsTemplate, false)
	err := stitcher.StitchWidgets("workflows/...", "dist/widgets.rfwtxt")
	if err != nil {
		t.Fatalf("StitchWidgets failed: %v", err)
	}

	content := string(fs.files["dist/widgets.rfwtxt"])

	// Widget should appear exactly once
	count := strings.Count(content, "widget ConflictWidget")
	if count != 1 {
		t.Errorf("Expected ConflictWidget to appear once, found %d times", count)
	}

	// First version (from a-first, alphabetically first) should win
	if !strings.Contains(content, "First version") {
		t.Error("Expected first version to be kept (alphabetically first workflow wins)")
	}
	if strings.Contains(content, "Second version") {
		t.Error("Second version should have been skipped")
	}
}

// TestStitchWidgets_MixedDuplicates tests a mix of identical and conflicting duplicates
func TestStitchWidgets_MixedDuplicates(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()

	// Workflow A: has IdenticalWidget, ConflictWidget, and UniqueA (with proper annotations)
	fs.files["workflows/a-workflow/a.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Identical"/>
    <bpmn:userTask id="Task_2" name="Conflict"/>
    <bpmn:userTask id="Task_3" name="UniqueA"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: IdenticalWidget</bpmn:text></bpmn:textAnnotation>
    <bpmn:textAnnotation id="Ann_2"><bpmn:text>ID: ConflictWidget</bpmn:text></bpmn:textAnnotation>
    <bpmn:textAnnotation id="Ann_3"><bpmn:text>ID: UniqueA</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
    <bpmn:association sourceRef="Task_2" targetRef="Ann_2"/>
    <bpmn:association sourceRef="Task_3" targetRef="Ann_3"/>
  </bpmn:process>
</bpmn:definitions>`)
	fs.files["workflows/a-workflow/mobile/widget1.rfwtxt"] = []byte(`widget IdenticalWidget = ActivityContainer(
    child: ListView(children: [])
);

widget ConflictWidget = ActivityContainer(
    child: Text(text: "A version")
);

widget UniqueA = ActivityContainer(
    child: Text(text: "Only in A")
);`)

	// Workflow B: has same IdenticalWidget, different ConflictWidget, unique widget (with proper annotations)
	fs.files["workflows/b-workflow/b.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Identical"/>
    <bpmn:userTask id="Task_2" name="Conflict"/>
    <bpmn:userTask id="Task_3" name="UniqueB"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: IdenticalWidget</bpmn:text></bpmn:textAnnotation>
    <bpmn:textAnnotation id="Ann_2"><bpmn:text>ID: ConflictWidget</bpmn:text></bpmn:textAnnotation>
    <bpmn:textAnnotation id="Ann_3"><bpmn:text>ID: UniqueB</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
    <bpmn:association sourceRef="Task_2" targetRef="Ann_2"/>
    <bpmn:association sourceRef="Task_3" targetRef="Ann_3"/>
  </bpmn:process>
</bpmn:definitions>`)
	fs.files["workflows/b-workflow/mobile/widget1.rfwtxt"] = []byte(`widget IdenticalWidget = ActivityContainer(
    child: ListView(children: [])
);

widget ConflictWidget = ActivityContainer(
    child: Text(text: "B version")
);

widget UniqueB = ActivityContainer(
    child: Text(text: "Only in B")
);`)

	stitcher := NewStitcher(fs, rfwsTemplate, false)
	err := stitcher.StitchWidgets("workflows/...", "dist/widgets.rfwtxt")
	if err != nil {
		t.Fatalf("StitchWidgets failed: %v", err)
	}

	content := string(fs.files["dist/widgets.rfwtxt"])

	// IdenticalWidget should appear once (silently deduplicated)
	if strings.Count(content, "widget IdenticalWidget") != 1 {
		t.Error("IdenticalWidget should appear exactly once")
	}

	// ConflictWidget should appear once with A's version (first wins)
	if strings.Count(content, "widget ConflictWidget") != 1 {
		t.Error("ConflictWidget should appear exactly once")
	}
	if !strings.Contains(content, "A version") {
		t.Error("ConflictWidget should have A's version (alphabetically first)")
	}

	// Unique widgets should both be present
	if !strings.Contains(content, "widget UniqueA") {
		t.Error("UniqueA should be present")
	}
	if !strings.Contains(content, "widget UniqueB") {
		t.Error("UniqueB should be present")
	}
}

// TestStitchWidgets_IncludeAllWidgets tests that all widgets are included in stitched output regardless of BPMN references
func TestStitchWidgets_IncludeAllWidgets(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()

	// BPMN only references LinkedWidget via annotation - other widgets in RFW have no BPMN task
	fs.files["workflows/w1/w1.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Linked Widget"/>
    <bpmn:textAnnotation id="Ann_1">
      <bpmn:text>ID: LinkedWidget</bpmn:text>
    </bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`)

	// RFW file has LinkedWidget (in BPMN) plus other widgets (not in BPMN)
	fs.files["workflows/w1/mobile/widget1.rfwtxt"] = []byte(`import core.widgets;

widget LinkedWidget = ActivityContainer(
    child: ListView(children: [])
);

widget OrphanedWidget = ActivityContainer(
    child: Text(text: "I am orphaned")
);

widget AnotherOrphan = ActivityContainer(
    child: Text(text: "Me too")
);
`)

	stitcher := NewStitcher(fs, rfwsTemplate, false)
	err := stitcher.StitchWidgets("workflows/...", "dist/widgets.rfwtxt")
	if err != nil {
		t.Fatalf("StitchWidgets failed: %v", err)
	}

	content := string(fs.files["dist/widgets.rfwtxt"])

	// ALL widgets should be in stitched output (no orphan filtering)
	if !strings.Contains(content, "widget LinkedWidget") {
		t.Error("LinkedWidget should be in stitched output")
	}
	if !strings.Contains(content, "OrphanedWidget") {
		t.Error("OrphanedWidget should be in stitched output (no orphan filtering)")
	}
	if !strings.Contains(content, "AnotherOrphan") {
		t.Error("AnotherOrphan should be in stitched output (no orphan filtering)")
	}
}

// TestStitchWidgets_IndividualFiles tests stitching when each widget is in its own .rfwtxt file
func TestStitchWidgets_IndividualFiles(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()

	fs.files["workflows/201-picking/workflow.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Scan Tray"/>
    <bpmn:userTask id="Task_2" name="Confirm Pick"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: ScanTray</bpmn:text></bpmn:textAnnotation>
    <bpmn:textAnnotation id="Ann_2"><bpmn:text>ID: ConfirmPick</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
    <bpmn:association sourceRef="Task_2" targetRef="Ann_2"/>
  </bpmn:process>
</bpmn:definitions>`)

	// Each widget in its own file
	fs.files["workflows/201-picking/mobile/scan-tray.rfwtxt"] = []byte(`widget ScanTray = ActivityContainer(
    child: ListView(children: [ActivityHeader(title: "Scan Tray")])
);`)

	fs.files["workflows/201-picking/mobile/confirm-pick.rfwtxt"] = []byte(`widget ConfirmPick = ActivityContainer(
    child: ListView(children: [ActivityHeader(title: "Confirm Pick")])
);`)

	stitcher := NewStitcher(fs, rfwsTemplate, false)
	err := stitcher.StitchWidgets("workflows/...", "dist/widgets.rfwtxt")
	if err != nil {
		t.Fatalf("StitchWidgets failed: %v", err)
	}

	content := string(fs.files["dist/widgets.rfwtxt"])

	if !strings.Contains(content, "widget ScanTray") {
		t.Error("ScanTray should be in stitched output")
	}
	if !strings.Contains(content, "widget ConfirmPick") {
		t.Error("ConfirmPick should be in stitched output")
	}

	// Both should reference the 201-picking workflow
	if !strings.Contains(content, "201-picking") {
		t.Error("Stitched output should reference workflow 201-picking")
	}
}

// TestStitchWidgets_NoOrphanFiltering tests that widgets without matching BPMN tasks are still included
func TestStitchWidgets_NoOrphanFiltering(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()

	// BPMN with only one user task annotation for LinkedWidget
	fs.files["workflows/w1/workflow.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Linked"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: LinkedWidget</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`)

	fs.files["workflows/w1/mobile/linked.rfwtxt"] = []byte(`widget LinkedWidget = ActivityContainer(
    child: ListView(children: [])
);`)

	// Helper widget with no corresponding BPMN task (like centered-loader)
	fs.files["workflows/w1/mobile/helper.rfwtxt"] = []byte(`widget CenteredLoader = Center(
    child: CircularProgressIndicator()
);`)

	stitcher := NewStitcher(fs, rfwsTemplate, false)
	err := stitcher.StitchWidgets("workflows/...", "dist/widgets.rfwtxt")
	if err != nil {
		t.Fatalf("StitchWidgets failed: %v", err)
	}

	content := string(fs.files["dist/widgets.rfwtxt"])

	// Both LinkedWidget AND CenteredLoader should be in stitched output
	if !strings.Contains(content, "widget LinkedWidget") {
		t.Error("LinkedWidget should be in stitched output")
	}
	if !strings.Contains(content, "widget CenteredLoader") {
		t.Error("CenteredLoader should be in stitched output (no orphan filtering)")
	}
}

// TestStitchWidgets_MixedDirectories tests finding .rfwtxt in both mobile/ and flutter/ directories
func TestStitchWidgets_MixedDirectories(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()

	// Workflow 1 uses mobile/ directory
	fs.files["workflows/w1/w1.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Mobile Widget"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: MobileWidget</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`)
	fs.files["workflows/w1/mobile/widget1.rfwtxt"] = []byte(`widget MobileWidget = ActivityContainer(
    child: Text(text: "From mobile dir")
);`)

	// Workflow 2 uses flutter/ directory (legacy pattern)
	fs.files["workflows/w2/w2.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Flutter Widget"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: FlutterWidget</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`)
	fs.files["workflows/w2/flutter/index.rfwtxt"] = []byte(`widget FlutterWidget = ActivityContainer(
    child: Text(text: "From flutter dir")
);`)

	stitcher := NewStitcher(fs, rfwsTemplate, false)
	err := stitcher.StitchWidgets("workflows/...", "dist/widgets.rfwtxt")
	if err != nil {
		t.Fatalf("StitchWidgets failed: %v", err)
	}

	content := string(fs.files["dist/widgets.rfwtxt"])

	// Both widgets should be found and stitched
	if !strings.Contains(content, "widget MobileWidget") {
		t.Error("MobileWidget from mobile/ should be in stitched output")
	}
	if !strings.Contains(content, "widget FlutterWidget") {
		t.Error("FlutterWidget from flutter/ should be in stitched output")
	}
}

// TestStitchWidgets_SkipsDistDirectory tests that .rfwtxt files under dist/ are not picked up
func TestStitchWidgets_SkipsDistDirectory(t *testing.T) {
	t.Parallel()

	fs := newMemFileSystem()

	// Widget in a normal workflow directory
	fs.files["workflows/w1/w1.bpmn"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL">
  <bpmn:process id="Process_1">
    <bpmn:userTask id="Task_1" name="Real Widget"/>
    <bpmn:textAnnotation id="Ann_1"><bpmn:text>ID: RealWidget</bpmn:text></bpmn:textAnnotation>
    <bpmn:association sourceRef="Task_1" targetRef="Ann_1"/>
  </bpmn:process>
</bpmn:definitions>`)
	fs.files["workflows/w1/mobile/widget1.rfwtxt"] = []byte(`widget RealWidget = ActivityContainer(
    child: Text(text: "I am real")
);`)

	// A previous stitched output in dist/ that should NOT be picked up
	fs.files["dist/widgets.rfwtxt"] = []byte(`widget OldStitchedWidget = ActivityContainer(
    child: Text(text: "I am old stitched output")
);`)

	stitcher := NewStitcher(fs, rfwsTemplate, false)
	err := stitcher.StitchWidgets("workflows/...", "dist/widgets.rfwtxt")
	if err != nil {
		t.Fatalf("StitchWidgets failed: %v", err)
	}

	content := string(fs.files["dist/widgets.rfwtxt"])

	// RealWidget should be in stitched output
	if !strings.Contains(content, "widget RealWidget") {
		t.Error("RealWidget should be in stitched output")
	}

	// OldStitchedWidget should NOT be in stitched output (dist/ is skipped)
	if strings.Contains(content, "OldStitchedWidget") {
		t.Error("OldStitchedWidget from dist/ should NOT be in stitched output")
	}
}

// TestExtractWorkflowNameFromRfwPath tests the extractWorkflowNameFromRfwPath method
func TestExtractWorkflowNameFromRfwPath(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		rfwPath  string
		bpmnDir  string // directory containing a .bpmn file
		bpmnFile string // name of the .bpmn file
		expected string
	}{
		{
			name:     "mobile directory with BPMN in parent",
			rfwPath:  "workflows/201-picking/mobile/scan-tray.rfwtxt",
			bpmnDir:  "workflows/201-picking",
			bpmnFile: "workflows/201-picking/workflow.bpmn",
			expected: "201-picking",
		},
		{
			name:     "flutter directory with BPMN in parent",
			rfwPath:  "workflows/200-picklist/flutter/index.rfwtxt",
			bpmnDir:  "workflows/200-picklist",
			bpmnFile: "workflows/200-picklist/200-picklist.bpmn",
			expected: "200-picklist",
		},
		{
			name:     "deeply nested rfwtxt",
			rfwPath:  "workflows/300-shipping/mobile/screens/scan.rfwtxt",
			bpmnDir:  "workflows/300-shipping",
			bpmnFile: "workflows/300-shipping/shipping.bpmn",
			expected: "300-shipping",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fs := newMemFileSystem()
			fs.files[tc.bpmnFile] = []byte("bpmn content")

			stitcher := NewStitcher(fs, rfwsTemplate, false)
			result := stitcher.extractWorkflowNameFromRfwPath(tc.rfwPath)

			if result != tc.expected {
				t.Errorf("Expected %q, got %q", tc.expected, result)
			}
		})
	}
}
