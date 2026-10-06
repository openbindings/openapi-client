module github.com/openbindings/openapi-client/go/jsonschematest

go 1.25.0

require (
	github.com/openbindings/openapi-client/go v0.0.0-00010101000000-000000000000
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
)

require golang.org/x/text v0.14.0 // indirect

replace github.com/openbindings/openapi-client/go => ../
