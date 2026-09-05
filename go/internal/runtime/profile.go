package openapiclient

// Profile selects artifact-execution capabilities. The engine intentionally
// knows capabilities, not OpenBindings binding identifiers. Older internal
// filenames and helper names containing numbered "revision" labels record the
// order in which capabilities were developed; they were never published
// binding-specification identifiers or revisions.
type Profile struct {
	Name                           string
	RoutedInputs                   bool
	MediaFidelity                  bool
	ResponseFidelity               bool
	DynamicObjectCarriage          bool
	WholeJSONCarriage              bool
	SchemaOmittedOAS30ByteCarriage bool
	InputRouteKey                  string
	InputRouteMarker               string
}

var (
	profileBase          = Profile{Name: "base", InputRouteKey: "$openapi", InputRouteMarker: "openapi-client.routed@1"}
	profileRouted        = Profile{Name: "routed", RoutedInputs: true, InputRouteKey: "$openapi", InputRouteMarker: "openapi-client.routed@1"}
	profileMedia         = Profile{Name: "media", RoutedInputs: true, MediaFidelity: true, InputRouteKey: "$openapi", InputRouteMarker: "openapi-client.routed@1"}
	profileResponse      = Profile{Name: "response", RoutedInputs: true, MediaFidelity: true, ResponseFidelity: true, InputRouteKey: "$openapi", InputRouteMarker: "openapi-client.routed@1"}
	profileDynamicObject = Profile{Name: "dynamic-object", RoutedInputs: true, MediaFidelity: true, ResponseFidelity: true, DynamicObjectCarriage: true, InputRouteKey: "$openapi", InputRouteMarker: "openapi-client.routed@1"}
	profileWholeJSON     = Profile{Name: "whole-json", RoutedInputs: true, MediaFidelity: true, ResponseFidelity: true, DynamicObjectCarriage: true, WholeJSONCarriage: true, InputRouteKey: "$openapi", InputRouteMarker: "openapi-client.routed@1"}
	profileFull          = Profile{Name: "full", RoutedInputs: true, MediaFidelity: true, ResponseFidelity: true, DynamicObjectCarriage: true, WholeJSONCarriage: true, SchemaOmittedOAS30ByteCarriage: true, InputRouteKey: "$openapi", InputRouteMarker: "openapi-client.routed@1"}
)

func BaseProfile() Profile          { return profileBase }
func RoutedProfile() Profile        { return profileRouted }
func MediaProfile() Profile         { return profileMedia }
func ResponseProfile() Profile      { return profileResponse }
func DynamicObjectProfile() Profile { return profileDynamicObject }
func WholeJSONProfile() Profile     { return profileWholeJSON }
func FullProfile() Profile          { return profileFull }

// WithInputRouteMarker returns a profile using an adapter-owned private marker.
func WithInputRouteMarker(profile Profile, marker string) Profile {
	if marker == "" {
		panic("openapi client: input route marker must be non-empty")
	}
	profile.InputRouteMarker = marker
	return profile
}

// WithInputRouteEnvelope returns a profile using an adapter-owned private
// routed-envelope discriminator and marker.
func WithInputRouteEnvelope(profile Profile, key, marker string) Profile {
	if key == "" || marker == "" {
		panic("openapi client: input route key and marker must be non-empty")
	}
	profile.InputRouteKey = key
	profile.InputRouteMarker = marker
	return profile
}

func normalizedProfile(profile Profile) Profile {
	if profile.Name == "" {
		return FullProfile()
	}
	if profile.InputRouteMarker == "" {
		profile.InputRouteMarker = "openapi-client.routed@1"
	}
	if profile.InputRouteKey == "" {
		profile.InputRouteKey = "$openapi"
	}
	return profile
}
