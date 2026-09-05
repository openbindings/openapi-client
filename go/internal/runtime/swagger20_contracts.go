package openapiclient

// Swagger20ParameterLocation is one Swagger 2.0 Parameter Object wire
// location. Body is represented separately in Swagger20Input because its
// declared name has no wire role.
type Swagger20ParameterLocation string

const (
	Swagger20ParameterPath     Swagger20ParameterLocation = "path"
	Swagger20ParameterQuery    Swagger20ParameterLocation = "query"
	Swagger20ParameterHeader   Swagger20ParameterLocation = "header"
	Swagger20ParameterFormData Swagger20ParameterLocation = "formData"
	Swagger20ParameterBody     Swagger20ParameterLocation = "body"
)

// Swagger20ParameterInfo exposes the effective parameter identities needed
// by an adapter to map its caller vocabulary into the client's native,
// location-separated input. It intentionally carries no OpenBindings keys.
type Swagger20ParameterInfo struct {
	Name     string
	In       Swagger20ParameterLocation
	Type     string
	Required bool
}

// Swagger20Parameters keeps same-named declarations in different locations
// distinct without introducing an adapter-specific qualified-key convention.
type Swagger20Parameters struct {
	Path     map[string]any
	Query    map[string]any
	Header   map[string]any
	FormData map[string]any
}

// Swagger20Input is the OpenAPI-native input for one Swagger 2.0 operation.
// BodyPresent distinguishes an authored JSON null body from omission.
type Swagger20Input struct {
	Parameters  Swagger20Parameters
	Body        any
	BodyPresent bool
}

// Swagger20EmptyValueForm selects one of the two wire spellings admitted by
// allowEmptyValue for query and formData parameters.
type Swagger20EmptyValueForm string

const (
	Swagger20EmptyValueNameOnly Swagger20EmptyValueForm = "name-only"
	Swagger20EmptyValueEmpty    Swagger20EmptyValueForm = "empty"
)
