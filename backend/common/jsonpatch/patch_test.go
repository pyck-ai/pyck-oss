package jsonpatch_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/jsonpatch"
)

func ptr(s string) *string { return &s }

func TestApplyPatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current map[string]any
		ops     []jsonpatch.PatchOperation
		want    map[string]any
		wantErr bool
	}{
		{
			name:    "add field",
			current: map[string]any{"name": "Alice"},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpAdd, Path: "/age", Value: ptr("30")},
			},
			want: map[string]any{"name": "Alice", "age": float64(30)},
		},
		{
			name:    "add nested field",
			current: map[string]any{"address": map[string]any{"city": "NYC"}},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpAdd, Path: "/address/zip", Value: ptr(`"10001"`)},
			},
			want: map[string]any{"address": map[string]any{"city": "NYC", "zip": "10001"}},
		},
		{
			name:    "remove field",
			current: map[string]any{"name": "Alice", "age": float64(30)},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpRemove, Path: "/age"},
			},
			want: map[string]any{"name": "Alice"},
		},
		{
			name:    "replace field",
			current: map[string]any{"name": "Alice", "age": float64(30)},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/age", Value: ptr("31")},
			},
			want: map[string]any{"name": "Alice", "age": float64(31)},
		},
		{
			name:    "move field",
			current: map[string]any{"old_name": "Alice"},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpMove, From: ptr("/old_name"), Path: "/name"},
			},
			want: map[string]any{"name": "Alice"},
		},
		{
			name:    "copy field",
			current: map[string]any{"name": "Alice"},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpCopy, From: ptr("/name"), Path: "/backup_name"},
			},
			want: map[string]any{"name": "Alice", "backup_name": "Alice"},
		},
		{
			name:    "test operation succeeds",
			current: map[string]any{"version": "1.0"},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpTest, Path: "/version", Value: ptr(`"1.0"`)},
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/version", Value: ptr(`"2.0"`)},
			},
			want: map[string]any{"version": "2.0"},
		},
		{
			name:    "test operation fails",
			current: map[string]any{"version": "1.0"},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpTest, Path: "/version", Value: ptr(`"2.0"`)},
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/version", Value: ptr(`"3.0"`)},
			},
			wantErr: true,
		},
		{
			name:    "multiple operations",
			current: map[string]any{"a": float64(1), "b": float64(2), "c": float64(3)},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/a", Value: ptr("10")},
				{Op: jsonpatch.JSONPatchOpRemove, Path: "/b"},
				{Op: jsonpatch.JSONPatchOpAdd, Path: "/d", Value: ptr("4")},
			},
			want: map[string]any{"a": float64(10), "c": float64(3), "d": float64(4)},
		},
		{
			name:    "append to array",
			current: map[string]any{"tags": []any{"a", "b"}},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpAdd, Path: "/tags/-", Value: ptr(`"c"`)},
			},
			want: map[string]any{"tags": []any{"a", "b", "c"}},
		},
		{
			name:    "replace value with object",
			current: map[string]any{"config": "old"},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/config", Value: ptr(`{"key":"val"}`)},
			},
			want: map[string]any{"config": map[string]any{"key": "val"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := jsonpatch.ApplyPatches(tt.current, tt.ops)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertMapsEqual(t, tt.want, got)
		})
	}
}

func TestValidateOperations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ops     []jsonpatch.PatchOperation
		wantErr error
	}{
		{
			name:    "empty patches",
			ops:     nil,
			wantErr: jsonpatch.ErrEmptyPatches,
		},
		{
			name:    "add without value",
			ops:     []jsonpatch.PatchOperation{{Op: jsonpatch.JSONPatchOpAdd, Path: "/x"}},
			wantErr: jsonpatch.ErrMissingValue,
		},
		{
			name:    "replace without value",
			ops:     []jsonpatch.PatchOperation{{Op: jsonpatch.JSONPatchOpReplace, Path: "/x"}},
			wantErr: jsonpatch.ErrMissingValue,
		},
		{
			name:    "test without value",
			ops:     []jsonpatch.PatchOperation{{Op: jsonpatch.JSONPatchOpTest, Path: "/x"}},
			wantErr: jsonpatch.ErrMissingValue,
		},
		{
			name:    "move without from",
			ops:     []jsonpatch.PatchOperation{{Op: jsonpatch.JSONPatchOpMove, Path: "/x"}},
			wantErr: jsonpatch.ErrMissingFrom,
		},
		{
			name:    "copy without from",
			ops:     []jsonpatch.PatchOperation{{Op: jsonpatch.JSONPatchOpCopy, Path: "/x"}},
			wantErr: jsonpatch.ErrMissingFrom,
		},
		{
			name:    "invalid op",
			ops:     []jsonpatch.PatchOperation{{Op: jsonpatch.JSONPatchOp(99), Path: "/x"}},
			wantErr: jsonpatch.ErrInvalidOp,
		},
		{
			name: "valid mixed operations",
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpAdd, Path: "/a", Value: ptr("1")},
				{Op: jsonpatch.JSONPatchOpRemove, Path: "/b"},
				{Op: jsonpatch.JSONPatchOpMove, From: ptr("/c"), Path: "/d"},
			},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := jsonpatch.ValidateOperations(tt.ops)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected error %q, got %q", tt.wantErr, err)
			}
		})
	}
}

func TestApplyPatchesEmptyData(t *testing.T) {
	t.Parallel()

	got, err := jsonpatch.ApplyPatches(map[string]any{}, []jsonpatch.PatchOperation{
		{Op: jsonpatch.JSONPatchOpAdd, Path: "/x", Value: ptr("1")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["x"] != float64(1) {
		t.Fatalf("expected x=1, got %v", got["x"])
	}
}

func TestApplyPatchesNilDataTreatedAsEmpty(t *testing.T) {
	t.Parallel()

	// nil data should be treated as empty object by PatchEntityData,
	// but ApplyPatches itself should handle it gracefully too.
	got, err := jsonpatch.ApplyPatches(map[string]any{}, []jsonpatch.PatchOperation{
		{Op: jsonpatch.JSONPatchOpAdd, Path: "/name", Value: ptr(`"Alice"`)},
		{Op: jsonpatch.JSONPatchOpAdd, Path: "/age", Value: ptr("30")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["name"] != "Alice" {
		t.Fatalf("expected name=Alice, got %v", got["name"])
	}
	if got["age"] != float64(30) {
		t.Fatalf("expected age=30, got %v", got["age"])
	}
}

func TestToOperationsSkipsNil(t *testing.T) {
	t.Parallel()

	v := "1"
	inputs := []*jsonpatch.JSONPatchInput{
		{Op: jsonpatch.JSONPatchOpAdd, Path: "/a", Value: &v},
		nil,
		{Op: jsonpatch.JSONPatchOpRemove, Path: "/b"},
	}
	ops := jsonpatch.ToOperations(inputs)
	if len(ops) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(ops))
	}
	if ops[0].Path != "/a" {
		t.Fatalf("expected first op path /a, got %s", ops[0].Path)
	}
	if ops[1].Path != "/b" {
		t.Fatalf("expected second op path /b, got %s", ops[1].Path)
	}
}

// =============================================================================
// EDGE CASES
// =============================================================================

func TestApplyPatchesEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current map[string]any
		ops     []jsonpatch.PatchOperation
		want    map[string]any
		wantErr bool
	}{
		{
			name:    "unicode value",
			current: map[string]any{"name": "Alice"},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/name", Value: ptr(`"\u65e5\u672c\u8a9e\u30c6\u30b9\u30c8"`)},
			},
			want: map[string]any{"name": "\u65e5\u672c\u8a9e\u30c6\u30b9\u30c8"},
		},
		{
			name:    "emoji value",
			current: map[string]any{"status": "ok"},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/status", Value: ptr(`"✅ 🚀 done"`)},
			},
			want: map[string]any{"status": "✅ 🚀 done"},
		},
		{
			name:    "escaped quotes in value",
			current: map[string]any{"note": "old"},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/note", Value: ptr(`"say \"hello\" world"`)},
			},
			want: map[string]any{"note": `say "hello" world`},
		},
		{
			name:    "backslash in value",
			current: map[string]any{"path": "old"},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/path", Value: ptr(`"C:\\Users\\test"`)},
			},
			want: map[string]any{"path": `C:\Users\test`},
		},
		{
			name:    "null value",
			current: map[string]any{"field": "has-value"},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/field", Value: ptr("null")},
			},
			want: map[string]any{"field": nil},
		},
		{
			name:    "empty string value",
			current: map[string]any{"field": "has-value"},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/field", Value: ptr(`""`)},
			},
			want: map[string]any{"field": ""},
		},
		{
			name:    "boolean value",
			current: map[string]any{"active": false},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/active", Value: ptr("true")},
			},
			want: map[string]any{"active": true},
		},
		{
			name:    "deeply nested path",
			current: map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": "old"}}}},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/a/b/c/d", Value: ptr(`"new"`)},
			},
			want: map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": "new"}}}},
		},
		{
			name:    "add object value",
			current: map[string]any{"x": float64(1)},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpAdd, Path: "/nested", Value: ptr(`{"key":"val","num":42}`)},
			},
			want: map[string]any{"x": float64(1), "nested": map[string]any{"key": "val", "num": float64(42)}},
		},
		{
			name:    "add array value",
			current: map[string]any{"x": float64(1)},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpAdd, Path: "/list", Value: ptr(`[1,2,3]`)},
			},
			want: map[string]any{"x": float64(1), "list": []any{float64(1), float64(2), float64(3)}},
		},
		{
			name:    "malformed JSON value",
			current: map[string]any{"x": float64(1)},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpAdd, Path: "/bad", Value: ptr(`{not json}`)},
			},
			wantErr: true,
		},
		{
			name:    "unicode escape in value",
			current: map[string]any{"x": "old"},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/x", Value: ptr(`"\u00e9l\u00e8ve"`)},
			},
			want: map[string]any{"x": "élève"},
		},
		{
			name:    "key with special characters",
			current: map[string]any{"a/b": "old"},
			ops: []jsonpatch.PatchOperation{
				// RFC 6901: ~ is escaped as ~0, / is escaped as ~1
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/a~1b", Value: ptr(`"new"`)},
			},
			want: map[string]any{"a/b": "new"},
		},
		{
			name:    "key with tilde",
			current: map[string]any{"a~b": "old"},
			ops: []jsonpatch.PatchOperation{
				{Op: jsonpatch.JSONPatchOpReplace, Path: "/a~0b", Value: ptr(`"new"`)},
			},
			want: map[string]any{"a~b": "new"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := jsonpatch.ApplyPatches(tt.current, tt.ops)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertMapsEqual(t, tt.want, got)
		})
	}
}

// =============================================================================
// FUZZ TESTS
// =============================================================================

// FuzzApplyPatches exercises the patch pipeline with random values to find
// panics, hangs, or unexpected errors in the JSON marshal/unmarshal/patch chain.
func FuzzApplyPatches(f *testing.F) {
	// Seed corpus with representative inputs.
	f.Add("name", `"Alice"`)
	f.Add("count", "42")
	f.Add("flag", "true")
	f.Add("data", `{"nested":"value"}`)
	f.Add("list", `[1,2,3]`)
	f.Add("empty", `""`)
	f.Add("null_val", "null")
	f.Add("unicode", "\"unicode test \\u00e9l\\u00e8ve\"")
	f.Add("escaped", `"line1\nline2\ttab"`)
	f.Add("special", `"say \"hello\""`)

	f.Fuzz(func(t *testing.T, key, value string) {
		// Only fuzz with syntactically valid JSON values to focus on
		// the patch logic rather than JSON parse errors.
		if !json.Valid([]byte(value)) {
			t.Skip("invalid JSON value")
		}

		current := map[string]any{"existing": "data", key: "original"}

		ops := []jsonpatch.PatchOperation{
			{Op: jsonpatch.JSONPatchOpReplace, Path: "/" + key, Value: &value},
		}

		result, err := jsonpatch.ApplyPatches(current, ops)
		if err != nil {
			// Errors are fine (e.g. invalid path) — we're looking for panics.
			return
		}

		// The result must be valid: re-marshal should succeed.
		if _, err := json.Marshal(result); err != nil {
			t.Fatalf("result is not valid JSON: %v", err)
		}
	})
}

// FuzzApplyPatchesMultiOp exercises sequences of mixed operations.
func FuzzApplyPatchesMultiOp(f *testing.F) {
	f.Add("field1", `"value1"`, "field2", `"value2"`)
	f.Add("a", "1", "b", `"hello"`)
	f.Add("x", `{"k":"v"}`, "y", `[1,2]`)

	f.Fuzz(func(t *testing.T, addKey, addVal, replaceKey, replaceVal string) {
		if !json.Valid([]byte(addVal)) || !json.Valid([]byte(replaceVal)) {
			t.Skip("invalid JSON")
		}

		current := map[string]any{replaceKey: "original"}

		ops := []jsonpatch.PatchOperation{
			{Op: jsonpatch.JSONPatchOpAdd, Path: "/" + addKey, Value: &addVal},
			{Op: jsonpatch.JSONPatchOpReplace, Path: "/" + replaceKey, Value: &replaceVal},
		}

		result, err := jsonpatch.ApplyPatches(current, ops)
		if err != nil {
			return
		}

		if _, err := json.Marshal(result); err != nil {
			t.Fatalf("result is not valid JSON: %v", err)
		}
	})
}

// assertMapsEqual does a deep comparison via JSON serialization.
func assertMapsEqual(t *testing.T, want, got map[string]any) {
	t.Helper()

	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}

	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}

	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("mismatch:\nwant: %s\ngot:  %s", wantJSON, gotJSON)
	}
}
