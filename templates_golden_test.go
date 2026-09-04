package main

import (
	"flag"
	"go/format"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden template renders")

// goldenSpec exercises the parts of the command templates that drifted when the
// same blocks were copy-pasted per file: a grouped and an ungrouped operation,
// every optional parameter type, enum/format/x-cli-no-validate parameters,
// required and optional date-time parameters, a body with an enum and a
// date-time field, and a destructive operation.
const goldenSpec = `
openapi: 3.0.3
info:
  title: Golden API
  version: "1"
servers:
  - url: https://api.example.com
paths:
  /{id}:
    get:
      operationId: GetThing
      summary: Get a thing
      parameters:
        - {name: id, in: path, required: true, schema: {type: string, format: uuid}}
        - {name: detailed, in: query, schema: {type: boolean}}
        - {name: limit, in: query, schema: {type: integer}}
        - {name: ratio, in: query, schema: {type: number}}
        - {name: kind, in: query, schema: {type: string, enum: [internal, a2a]}}
        # Two in one operation, so each normalization needs its own scope.
        - {name: from, in: query, required: true, schema: {type: string, format: date-time}}
        - {name: to, in: query, schema: {type: string, format: date-time}}
        # Shares the Go name a from-derived temporary would take, which is why
        # the normalization is scoped to a block rather than named after a param.
        - {name: from-time, in: query, required: true, schema: {type: string}}
        - name: cursor
          in: query
          x-cli-no-validate: true
          schema: {type: string, format: uuid}
        # in: cookie does not set Imports.Fmt, so this pins that the validation
        # block emits no fmt.Sprintf.
        - {name: sess, in: cookie, schema: {type: string, enum: [a, b]}}
      responses:
        "200": {description: ok}
  /widgets:
    post:
      operationId: CreateWidget
      summary: Create a widget
      x-cli-help-section: Writes
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                status: {type: string, enum: [active, archived]}
                created_after: {type: string, format: date-time}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  warnings:
                    type: array
                    items: {type: string}
  /widgets/search:
    post:
      operationId: SearchWidgets
      summary: Search widgets
      x-cli-list-fields: [name, id]
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  matches:
                    type: array
                    items:
                      type: object
                      properties:
                        id: {type: string}
                        name: {type: string}
  /widgets/{id}:
    delete:
      operationId: DeleteWidget
      summary: Delete a widget
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      responses:
        "204": {description: gone}
`

// TestGeneratedOutputMatchesGolden pins the full rendered output of every
// command template. The substring assertions elsewhere only catch drift someone
// thought to assert; this catches the rest. A dropped enum or a missing shell
// completion still compiles, and smoke only runs the few paths it asserts on.
//
// Run `go test . -update` after an intentional template change, and read the
// diff rather than trusting it.
func TestGeneratedOutputMatchesGolden(t *testing.T) {
	api := ProcessAPI("example", loadTestSpec(t, goldenSpec))

	renders := map[string]string{
		"root_commands.go": renderCommandTemplate("templates/generated_root_commands.tmpl", &CommandsTemplateData{
			API:        api,
			Operations: api.Operations,
			Waiters:    api.Waiters,
			NeedsFmt:   commandFileNeedsFmt(api.Operations),
		}),
		"client.go": renderCommandTemplate("templates/generated_client.tmpl", api),
	}

	for _, group := range api.Groups {
		renders["group_"+group.CLIName+"_commands.go"] = renderCommandTemplate("templates/generated_group_commands.tmpl", &CommandsTemplateData{
			API:        api,
			Group:      group,
			Operations: group.Operations,
			NeedsFmt:   commandFileNeedsFmt(group.Operations),
		})
	}

	for name, rendered := range renders {
		// Golden files hold what the generator actually writes, which is the
		// gofmt'd render — otherwise `gofmt -l .` flags testdata forever.
		formatted, err := format.Source([]byte(rendered))
		if err != nil {
			t.Fatalf("%s does not parse as Go: %v", name, err)
		}

		got := string(formatted)
		path := filepath.Join("testdata", "golden", name)

		if *updateGolden {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}

			continue
		}

		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s (run `go test . -update` to create it): %v", path, err)
		}

		if string(want) != got {
			t.Errorf("%s differs from its golden file. If the change is intended, run `go test . -update` and review the diff.", name)
		}
	}
}
