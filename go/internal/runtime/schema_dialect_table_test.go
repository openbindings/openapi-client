package openapiclient

// Executes the shared Schema Object dialect case table
// (testdata/schema-object-dialect-cases.json) through the shipped acceptance
// floor. The same file, at the same digest, embeds in the standalone Go and
// TypeScript engines. OpenBindings integration migrates after their public
// contract is frozen.
//
// Each cell places one Schema Object at a success response's only media
// alternative beside a clean sibling operation, so every cell asserts three
// things at once: which positions the governing dialect finds defective, which
// class owns each position, and that the confinement stays inside the unit that
// earned it.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

// The embedded table's own digest. A change here is a change to the shared
// answer and must land in every engine simultaneously.
const schemaObjectDialectTableSHA256 = "3f455cbd34904fa90a002b0276816c5ed0a9d527c8bbd05bb5e7e1d4e5479803"

type schemaDialectPosition struct {
	Position string `json:"position"`
	Class    string `json:"class"`
}

type schemaDialectCell struct {
	ID          string                  `json:"id"`
	Line        string                  `json:"line"`
	OpenAPI     string                  `json:"openapi"`
	Schema      map[string]any          `json:"schema"`
	Positions   []schemaDialectPosition `json:"positions"`
	Disposition string                  `json:"disposition"`
	Why         string                  `json:"why"`
}

type schemaDialectTable struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Cells         []schemaDialectCell `json:"cells"`
}

const schemaDialectSubjectRef = "#/paths/~1a/get"
const schemaDialectSchemaPtr = "#/paths/~1a/get/responses/200/content/application~1json/schema"
const schemaDialectCleanRef = "#/paths/~1b/get"

func loadSchemaDialectTable(t *testing.T) schemaDialectTable {
	t.Helper()
	data, err := os.ReadFile("testdata/schema-object-dialect-cases.json")
	if err != nil {
		t.Fatalf("read shared dialect case table: %v", err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != schemaObjectDialectTableSHA256 {
		t.Fatalf("shared dialect case table digest %s, pinned %s: the table changed without a simultaneous four-engine landing", got, schemaObjectDialectTableSHA256)
	}
	var table schemaDialectTable
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatalf("parse shared dialect case table: %v", err)
	}
	if len(table.Cells) == 0 {
		t.Fatal("shared dialect case table carries no cells")
	}
	return table
}

// schemaDialectDocument is the one document shape every engine builds for a
// cell: the cell's Schema Object as the sole media alternative of the subject
// operation's success response, beside a clean sibling.
func schemaDialectDocument(cell schemaDialectCell) map[string]any {
	response := func(schema any) map[string]any {
		return map[string]any{
			"200": map[string]any{
				"description": "ok",
				"content":     map[string]any{"application/json": map[string]any{"schema": schema}},
			},
		}
	}
	return map[string]any{
		"openapi": cell.OpenAPI,
		"info":    map[string]any{"title": "schema object dialect", "version": "1"},
		"paths": map[string]any{
			"/a": map[string]any{"get": map[string]any{"responses": response(cell.Schema)}},
			"/b": map[string]any{"get": map[string]any{"responses": response(map[string]any{"type": "object"})}},
		},
	}
}

func TestSchemaObjectDialectTable(t *testing.T) {
	table := loadSchemaDialectTable(t)
	for _, cell := range table.Cells {
		t.Run(cell.ID, func(t *testing.T) {
			floor := computeAcceptanceFloor(schemaDialectDocument(cell))
			if floor == nil {
				t.Fatalf("no floor for edition %s", cell.OpenAPI)
			}
			if floor.Line != cell.Line {
				t.Fatalf("edition %s read as line %s, want %s", cell.OpenAPI, floor.Line, cell.Line)
			}
			if floor.Refusal != "" {
				t.Fatalf("a confined defect refused the whole source: %s", floor.Refusal)
			}

			subject := floor.opVerdict(schemaDialectSubjectRef)
			if subject == nil {
				t.Fatalf("no verdict for %s", schemaDialectSubjectRef)
			}
			if subject.Disposition != cell.Disposition {
				t.Fatalf("%s is %s, want %s\n%s", schemaDialectSubjectRef, subject.Disposition, cell.Disposition, cell.Why)
			}

			// Defective response positions stay on the represented operation's
			// smallest-owner projections.
			want := make([]string, 0, len(cell.Positions))
			for _, position := range cell.Positions {
				want = append(want, position.Class+" "+schemaDialectSchemaPtr+position.Position)
			}
			got := make([]string, 0, len(cell.Positions))
			for _, defects := range subject.Projections {
				for _, d := range defects {
					got = append(got, d.Class+" "+d.Position)
				}
			}
			sort.Strings(want)
			sort.Strings(got)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("defective positions %v, want %v\n%s", got, want, cell.Why)
			}

			// Confinement: the clean sibling never pays for the cell's defect.
			clean := floor.opVerdict(schemaDialectCleanRef)
			if clean == nil || clean.Disposition != "represented" || len(clean.Defects) != 0 {
				t.Fatalf("the clean sibling operation did not survive intact: %+v", clean)
			}
		})
	}
}
