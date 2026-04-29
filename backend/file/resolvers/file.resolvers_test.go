package resolvers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var (
	createFile = resolver.ParseTemplate(`mutation {
		createFile(input: {
			{{- if .RefID }}
			refid: "{{ .RefID }}",
			{{- end }}
			{{- if .RefType }}
			reftype: {{ .RefType }},
			{{- end }}
			{{- if .Name }}
			name: "{{ .Name }}",
			{{- end }}
			{{- if .Size }}
			size: {{ .Size }},
			{{- end }}
			{{- if .ContentType }}
			contentType: "{{ .ContentType }}",
			{{- end }}
			{{- if .Description }}
			description: "{{ .Description }}",
			{{- end }}
			{{- if .DataTypeID }}
			dataTypeID: "{{ .DataTypeID }}",
			{{- end }}
			{{- if .Data }}
			data: {{ .Data }}
			{{- end }}
			{{- if .PublicAlias }}
			publicAlias: "{{ .PublicAlias }}",
			{{- end }}
		}) {
			id
			preSignedUploadUrl
			file {
				tenantID
				refid
				reftype
				name
				size
				contentType
				description
				dataTypeID
				data
				publicAlias
				url
				publicURL
			}
		}
	}`)

	updateFile = resolver.ParseTemplate(`mutation {
		updateFile(
			id: "{{ .ID }}",
			input: {
				{{- if .Description }}
				description: "{{ .Description }}",
				{{- end }}
				{{- if .DataTypeID }}
				dataTypeID: "{{ .DataTypeID }}",
				{{- end }}
				{{- if .Data }}
				data: {{ .Data }}
				{{- end }}
				{{- if .PublicAlias }}
				publicAlias: "{{ .PublicAlias }}",
				{{- end }}
				{{- if .ClearPublicAlias }}
				clearPublicAlias: true,
				{{- end }}
			}
		) {
			id
			tenantID
			name
			description
			publicAlias
			url
			publicURL
		}
	}`)

	deleteFile = resolver.ParseTemplate(`mutation {
		deleteFile(id: "{{ .ID }}") {
			deletedID
		}
	}`)

	queryFiles = resolver.ParseTemplate(`query {
		files(
			{{- if .First }}
			first: {{ .First }},
			{{- end }}
			{{- if .After }}
			after: {{ .After }},
			{{- end }}
			{{- if .OrderBy }}
			orderBy: {{ .OrderBy }},
			{{- end }}
			{{- if .Where }}
			where: {{ .Where }}
			{{- else }}
			where: null
			{{- end }}
		) {
			totalCount
			edges {
				node {
					id
					tenantID
					refid
					reftype
					name
					size
					contentType
					description
					url
					publicURL
					publicAlias
					dataTypeID
					data
				}
				cursor
			}
			pageInfo {
				hasNextPage
				hasPreviousPage
				startCursor
				endCursor
			}
		}
	}`)
)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type fileNode struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	RefID       uuid.UUID
	RefType     string
	Name        string
	Size        int64
	ContentType string
	Description string
	URL         string
	PublicURL   *string
	PublicAlias *string
	DataTypeID  uuid.UUID
	Data        map[string]any
}

type createFileData struct {
	CreateFile struct {
		ID                 uuid.UUID
		PreSignedUploadUrl string
		File               fileNode
	}
}

type updateFileData struct {
	UpdateFile struct {
		ID          uuid.UUID
		TenantID    uuid.UUID
		Name        string
		Description string
		PublicAlias *string
		URL         string
		PublicURL   *string
	}
}

type deleteFileData struct {
	DeleteFile struct{ DeletedID uuid.UUID }
}

type queryFilesData struct {
	Files struct {
		TotalCount int
		Edges      []struct {
			Node   fileNode
			Cursor string
		}
		PageInfo struct {
			HasNextPage     bool
			HasPreviousPage bool
			StartCursor     *string
			EndCursor       *string
		}
	}
}

// =============================================================================
// CREATE TESTS
// =============================================================================

func TestFile_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates file with valid data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createFileData](te, ctx, createFile, map[string]any{
			"RefID":       testRefID.String(),
			"RefType":     "supplier",
			"Name":        "test.txt",
			"Size":        100,
			"ContentType": "text/plain",
			"Description": "test file",
			"DataTypeID":  fileDataTypeID.String(),
			"Data": `{
				type: "file",
				meta: {
					name: "test-file"
				}
			}`,
		})

		created := data.CreateFile
		assert.NotEqual(t, uuid.Nil, created.ID)
		assert.NotEmpty(t, created.PreSignedUploadUrl)
		assert.Equal(t, "test.txt", created.File.Name)
		assert.Equal(t, tenantA, created.File.TenantID)

		// Verify persisted
		stored, err := te.Ent.File.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, "test.txt", stored.Name)

		// Verify event
		te.assertEvents(ctx, Create("file", created.ID))
	})

	t.Run("rejects missing required fields", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createFile, map[string]any{
			"RefID": testRefID.String(),
		}, "")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects invalid data field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createFile, map[string]any{
			"RefID":       testRefID.String(),
			"RefType":     "supplier",
			"Name":        "test.txt",
			"Size":        100,
			"ContentType": "text/plain",
			"Description": "test file",
			"DataTypeID":  fileDataTypeID.String(),
			"Data": `{
				type: "file",
				meta: {}
			}`,
		}, "")

		te.assertNoEvents(ctx)
	})
}

// =============================================================================
// UPDATE TESTS
// =============================================================================

func TestFile_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates file description", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f := te.newFile(ctx, userA).Description("old-desc").Create()
		te.clearEvents(ctx)

		data := execOK[updateFileData](te, ctx, updateFile, map[string]any{
			"ID":          f.ID.String(),
			"Description": "new-desc",
			"DataTypeID":  fileDataTypeID.String(),
			"Data": `{
				type: "file",
				meta: {
					name: "test-file"
				}
			}`,
		})

		assert.Equal(t, "new-desc", data.UpdateFile.Description)
		assert.Equal(t, tenantA, data.UpdateFile.TenantID)
		te.assertEvents(ctx, Update("file", f.ID))
	})

	t.Run("rejects update of other tenant's file", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create file as tenant B
		ctxB := te.ctx(userB)
		f := te.newFile(ctxB, userB).Description("old-desc").Create()
		te.clearEvents(ctxB)

		// Try to update as tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, updateFile, map[string]any{
			"ID":          f.ID.String(),
			"Description": "new-desc",
			"DataTypeID":  fileDataTypeID.String(),
		}, "not found")

		te.assertNoEvents(ctxA)
	})
}

// =============================================================================
// DELETE TESTS
// =============================================================================

func TestFile_Delete(t *testing.T) {
	t.Parallel()

	t.Run("soft deletes file", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f := te.newFile(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[deleteFileData](te, ctx, deleteFile, map[string]any{
			"ID": f.ID.String(),
		})

		assert.Equal(t, f.ID, data.DeleteFile.DeletedID)

		// Verify soft-deleted (need showDeleted context)
		deleted, err := te.Ent.File.Get(te.ctxWithDeleted(userA), f.ID)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt)
		assert.Equal(t, time.UTC, deleted.DeletedAt.Location(), "deleted_at should be in UTC")

		te.assertEvents(ctx, Delete("file", f.ID))
	})

	t.Run("rejects delete of other tenant's file", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		// Create file as tenant B
		ctxB := te.ctx(userB)
		f := te.newFile(ctxB, userB).Create()
		te.clearEvents(ctxB)

		// Try to delete as tenant A
		ctxA := te.ctx(userA)
		execErr(te, ctxA, deleteFile, map[string]any{
			"ID": f.ID.String(),
		}, "")

		te.assertNoEvents(ctxA)
	})
}

// =============================================================================
// QUERY TESTS
// =============================================================================

func TestFile_Query(t *testing.T) {
	t.Parallel()

	t.Run("returns empty result for no data", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[queryFilesData](te, ctx, queryFiles, nil)

		assert.Equal(t, 0, data.Files.TotalCount)
		assert.Empty(t, data.Files.Edges)
		assert.False(t, data.Files.PageInfo.HasNextPage)
		assert.False(t, data.Files.PageInfo.HasPreviousPage)
		assert.Nil(t, data.Files.PageInfo.StartCursor)
		assert.Nil(t, data.Files.PageInfo.EndCursor)
	})

	t.Run("returns only own tenant's files", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		fileA := te.newFile(ctxA, userA).Name("tenant-a.txt").Create()
		te.newFile(ctxB, userB).Name("tenant-b.txt").Create()

		data := execOK[queryFilesData](te, ctxA, queryFiles, nil)

		require.Equal(t, 1, data.Files.TotalCount)
		assert.Len(t, data.Files.Edges, 1)
		assert.Equal(t, fileA.ID, data.Files.Edges[0].Node.ID)
		assert.Equal(t, fileA.TenantID, data.Files.Edges[0].Node.TenantID)
		assert.Equal(t, fileA.Name, data.Files.Edges[0].Node.Name)
	})

	t.Run("filters by name prefix", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		file1 := te.newFile(ctx, userA).Name("1234567890.jpg").
			DataTypeID(fileDataTypeID).Data(validFileData).Create()
		te.newFile(ctx, userA).Name("abcdefghijk.jpg").
			DataTypeID(fileDataTypeID).Data(map[string]any{
			"type": "item",
			"meta": map[string]any{
				"name": "Testfile2",
				"tags": []any{"foo", "bla"},
			},
		}).Create()

		data := execOK[queryFilesData](te, ctx, queryFiles, map[string]any{
			"First":   20,
			"OrderBy": "{ direction: ASC, field: CREATED_AT }",
			"Where":   `{nameHasPrefix: "123"}`,
		})

		require.Equal(t, 1, data.Files.TotalCount)
		assert.Len(t, data.Files.Edges, 1)
		assert.Equal(t, file1.ID, data.Files.Edges[0].Node.ID)
	})

	t.Run("filters by name suffix", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		file1 := te.newFile(ctx, userA).Name("1234567890.jpg").
			DataTypeID(fileDataTypeID).Data(validFileData).Create()
		file2 := te.newFile(ctx, userA).Name("abcdefghijk.jpg").
			DataTypeID(fileDataTypeID).Data(map[string]any{
			"type": "item",
			"meta": map[string]any{
				"name": "Testfile2",
				"tags": []any{"foo", "bla"},
			},
		}).Create()

		data := execOK[queryFilesData](te, ctx, queryFiles, map[string]any{
			"First":   20,
			"OrderBy": "{ direction: ASC, field: CREATED_AT }",
			"Where":   `{nameHasSuffix: ".jpg"}`,
		})

		require.Equal(t, 2, data.Files.TotalCount)
		assert.Len(t, data.Files.Edges, 2)
		assert.Equal(t, file1.ID, data.Files.Edges[0].Node.ID)
		assert.Equal(t, file2.ID, data.Files.Edges[1].Node.ID)
	})
}

// =============================================================================
// JSONB FILTERING TESTS
// =============================================================================

func TestFile_FilterByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("filters by data field", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		target := te.newFile(ctx, userA).Data(map[string]any{
			"type": "custom",
			"meta": map[string]any{
				"name": "TestItem",
				"tags": []any{"foo", "bar"},
			},
		}).Create()
		te.newFile(ctx, userA).Create() // no data

		cases := []struct {
			desc   string
			filter string
			count  int
		}{
			{
				desc:   "Data filter",
				filter: `{ Data: ["type", "custom"] }`,
				count:  1,
			},
			{
				desc:   "DataHasKey filter",
				filter: `{ DataHasKey: "meta.name" }`,
				count:  1,
			},
			{
				desc:   "DataIn filter",
				filter: `{ DataIn: ["meta.name", "TestItem", "foo"] }`,
				count:  1,
			},
			{
				desc:   "DataContains filter",
				filter: `{ DataContains: ["meta.tags", "foo"] }`,
				count:  1,
			},
			{
				desc:   "Data null filter",
				filter: `{ Data: null }`,
				count:  2,
			},
			{
				desc:   "DataHasKey null filter",
				filter: `{ DataHasKey: null }`,
				count:  2,
			},
			{
				desc:   "DataIn null filter",
				filter: `{ DataIn: null }`,
				count:  2,
			},
			{
				desc:   "DataContains null filter",
				filter: `{ DataContains: null }`,
				count:  2,
			},
		}

		for _, tc := range cases {
			t.Run(tc.desc, func(t *testing.T) { //nolint:paralleltest // Subtests share test environment
				data := execOK[queryFilesData](te, ctx, queryFiles, map[string]any{
					"First":   100,
					"OrderBy": "{ direction: ASC, field: CREATED_AT }",
					"Where":   tc.filter,
				})

				assert.Equal(t, tc.count, data.Files.TotalCount)
				require.Len(t, data.Files.Edges, tc.count)

				if tc.count == 1 {
					assert.Equal(t, target.ID, data.Files.Edges[0].Node.ID)
				}
			})
		}
	})
}

// =============================================================================
// JSONB ORDERING TESTS
// =============================================================================

func TestFile_QueryOrderByJSONData(t *testing.T) {
	t.Parallel()

	t.Run("orders by top-level JSON key ascending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f1 := te.newFile(ctx, userA).Data(map[string]any{"sum": float64(30)}).Create()
		f2 := te.newFile(ctx, userA).Data(map[string]any{"sum": float64(10)}).Create()
		f3 := te.newFile(ctx, userA).Data(map[string]any{"sum": float64(20)}).Create()

		data := execOK[queryFilesData](te, ctx, queryFiles, map[string]any{
			"OrderBy": `{ direction: ASC, jsonPath: "sum" }`,
			"First":   100,
		})

		require.Equal(t, 3, data.Files.TotalCount)
		assert.Equal(t, f2.ID, data.Files.Edges[0].Node.ID)
		assert.Equal(t, f3.ID, data.Files.Edges[1].Node.ID)
		assert.Equal(t, f1.ID, data.Files.Edges[2].Node.ID)
	})

	t.Run("orders by nested JSON key descending", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f1 := te.newFile(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(10)},
		}).Create()
		f2 := te.newFile(ctx, userA).Data(map[string]any{
			"meta": map[string]any{"weight": float64(30)},
		}).Create()

		data := execOK[queryFilesData](te, ctx, queryFiles, map[string]any{
			"OrderBy": `{ direction: DESC, jsonPath: "meta.weight" }`,
			"First":   100,
		})

		require.Equal(t, 2, data.Files.TotalCount)
		assert.Equal(t, f2.ID, data.Files.Edges[0].Node.ID)
		assert.Equal(t, f1.ID, data.Files.Edges[1].Node.ID)
	})

	t.Run("standard field ordering still works", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f1 := te.newFile(ctx, userA).Create()
		f2 := te.newFile(ctx, userA).Create()

		data := execOK[queryFilesData](te, ctx, queryFiles, map[string]any{
			"OrderBy": `{ direction: DESC, field: CREATED_AT }`,
			"First":   100,
		})

		require.Equal(t, 2, data.Files.TotalCount)
		assert.Equal(t, f2.ID, data.Files.Edges[0].Node.ID)
		assert.Equal(t, f1.ID, data.Files.Edges[1].Node.ID)
	})
}

// =============================================================================
// ALIAS TESTS
// =============================================================================

func TestFile_Alias_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates file with alias", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createFileData](te, ctx, createFile, map[string]any{
			"RefID":       testRefID.String(),
			"RefType":     "supplier",
			"Name":        "logo.png",
			"Size":        2048,
			"ContentType": "image/png",
			"PublicAlias": "company-logo",
		})

		created := data.CreateFile
		require.NotNil(t, created.File.PublicAlias)
		assert.Equal(t, "company-logo", *created.File.PublicAlias)
		require.NotNil(t, created.File.PublicURL)
		assert.Contains(t, *created.File.PublicURL, "/api/v1/files/")
		assert.Contains(t, *created.File.PublicURL, "company-logo")

		te.assertEvents(ctx, Create("file", created.ID))
	})

	t.Run("creates file without alias returns pre-signed URL", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		data := execOK[createFileData](te, ctx, createFile, map[string]any{
			"RefID":       testRefID.String(),
			"RefType":     "supplier",
			"Name":        "doc.pdf",
			"Size":        1024,
			"ContentType": "application/pdf",
		})

		created := data.CreateFile
		assert.Nil(t, created.File.PublicAlias)
		assert.Nil(t, created.File.PublicURL)

		te.assertEvents(ctx, Create("file", created.ID))
	})

	t.Run("rejects invalid alias", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execErr(te, ctx, createFile, map[string]any{
			"RefID":       testRefID.String(),
			"RefType":     "supplier",
			"Name":        "logo.png",
			"Size":        2048,
			"ContentType": "image/png",
			"PublicAlias": "INVALID ALIAS!",
		}, "")

		te.assertNoEvents(ctx)
	})

	t.Run("rejects duplicate alias within same tenant", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		execOK[createFileData](te, ctx, createFile, map[string]any{
			"RefID":       uuidgql.GenerateV7UUID().String(),
			"RefType":     "supplier",
			"Name":        "logo1.png",
			"Size":        1024,
			"ContentType": "image/png",
			"PublicAlias": "shared-alias",
		})
		te.clearEvents(ctx)

		execErr(te, ctx, createFile, map[string]any{
			"RefID":       uuidgql.GenerateV7UUID().String(),
			"RefType":     "supplier",
			"Name":        "logo2.png",
			"Size":        1024,
			"ContentType": "image/png",
			"PublicAlias": "shared-alias",
		}, "")

		te.assertNoEvents(ctx)
	})

	t.Run("allows same alias across different tenants", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)

		ctxA := te.ctx(userA)
		ctxB := te.ctx(userB)

		dataA := execOK[createFileData](te, ctxA, createFile, map[string]any{
			"RefID":       uuidgql.GenerateV7UUID().String(),
			"RefType":     "supplier",
			"Name":        "logo-a.png",
			"Size":        1024,
			"ContentType": "image/png",
			"PublicAlias": "company-logo",
		})

		dataB := execOK[createFileData](te, ctxB, createFile, map[string]any{
			"RefID":       uuidgql.GenerateV7UUID().String(),
			"RefType":     "supplier",
			"Name":        "logo-b.png",
			"Size":        1024,
			"ContentType": "image/png",
			"PublicAlias": "company-logo",
		})

		require.NotNil(t, dataA.CreateFile.File.PublicAlias)
		require.NotNil(t, dataB.CreateFile.File.PublicAlias)
		assert.Equal(t, "company-logo", *dataA.CreateFile.File.PublicAlias)
		assert.Equal(t, "company-logo", *dataB.CreateFile.File.PublicAlias)
	})
}

func TestFile_Alias_Update(t *testing.T) {
	t.Parallel()

	t.Run("adds alias to existing file", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f := te.newFile(ctx, userA).Create()
		te.clearEvents(ctx)

		data := execOK[updateFileData](te, ctx, updateFile, map[string]any{
			"ID":          f.ID.String(),
			"PublicAlias": "new-alias",
		})

		require.NotNil(t, data.UpdateFile.PublicAlias)
		assert.Equal(t, "new-alias", *data.UpdateFile.PublicAlias)
		require.NotNil(t, data.UpdateFile.PublicURL)
		assert.Contains(t, *data.UpdateFile.PublicURL, "/api/v1/files/")
		assert.Contains(t, *data.UpdateFile.PublicURL, "new-alias")

		te.assertEvents(ctx, Update("file", f.ID))
	})

	t.Run("clears alias from file", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		f := te.newFile(ctx, userA).Alias("to-remove").Create()
		te.clearEvents(ctx)

		data := execOK[updateFileData](te, ctx, updateFile, map[string]any{
			"ID":               f.ID.String(),
			"ClearPublicAlias": true,
		})

		assert.Nil(t, data.UpdateFile.PublicAlias)
		assert.Nil(t, data.UpdateFile.PublicURL)

		te.assertEvents(ctx, Update("file", f.ID))
	})
}

func TestFile_Alias_Query(t *testing.T) {
	t.Parallel()

	t.Run("filters files by alias", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newFile(ctx, userA).Alias("app-config").Create()
		te.newFile(ctx, userA).Create() // no alias

		data := execOK[queryFilesData](te, ctx, queryFiles, map[string]any{
			"First": 100,
			"Where": `{publicAlias: "app-config"}`,
		})

		require.Equal(t, 1, data.Files.TotalCount)
		require.NotNil(t, data.Files.Edges[0].Node.PublicAlias)
		assert.Equal(t, "app-config", *data.Files.Edges[0].Node.PublicAlias)
	})

	t.Run("filters files with alias set", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		te.newFile(ctx, userA).Alias("has-alias").Create()
		te.newFile(ctx, userA).Create() // no alias

		data := execOK[queryFilesData](te, ctx, queryFiles, map[string]any{
			"First": 100,
			"Where": `{publicAliasNotNil: true}`,
		})

		require.Equal(t, 1, data.Files.TotalCount)
		require.NotNil(t, data.Files.Edges[0].Node.PublicAlias)
		assert.Equal(t, "has-alias", *data.Files.Edges[0].Node.PublicAlias)
	})

	t.Run("URL returns stable path for aliased files", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		aliased := te.newFile(ctx, userA).Alias("stable-file").Create()
		noAlias := te.newFile(ctx, userA).Create()

		data := execOK[queryFilesData](te, ctx, queryFiles, map[string]any{
			"First":   100,
			"OrderBy": "{ direction: ASC, field: CREATED_AT }",
		})

		require.Equal(t, 2, data.Files.TotalCount)

		// Aliased file: publicURL contains stable path
		aliasedNode := data.Files.Edges[0].Node
		assert.Equal(t, aliased.ID, aliasedNode.ID)
		require.NotNil(t, aliasedNode.PublicURL)
		assert.Contains(t, *aliasedNode.PublicURL, "/api/v1/files/")
		assert.Contains(t, *aliasedNode.PublicURL, "stable-file")

		// Non-aliased file: no publicURL
		noAliasNode := data.Files.Edges[1].Node
		assert.Equal(t, noAlias.ID, noAliasNode.ID)
		assert.Nil(t, noAliasNode.PublicURL)
	})
}
