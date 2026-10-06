# jsonschematest

Tests that check the client's JSON Schema output with an independent JSON
Schema 2020-12 evaluator. They live in their own module so that the client
module keeps no dependencies; nothing here is imported by the client.

Run them with `GOWORK=off go test ./...` from this directory.
