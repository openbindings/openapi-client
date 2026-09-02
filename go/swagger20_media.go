package openapiclient

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type swagger20MediaEntry struct {
	raw       string
	parsed    parsedMediaType
	parseErr  error
	colliding bool
}

type swagger20MediaSet struct {
	entries []swagger20MediaEntry
}

type swagger20PayloadKind string

const (
	swagger20PayloadBody swagger20PayloadKind = "body"
	swagger20PayloadForm swagger20PayloadKind = "formData"
)

type swagger20PayloadModel struct {
	kind        swagger20PayloadKind
	body        *swagger20Parameter
	form        []*swagger20Parameter
	declaration swagger20SchemaDeclaration
}

type swagger20MediaLane string

const (
	swagger20LaneJSON       swagger20MediaLane = "json"
	swagger20LaneText       swagger20MediaLane = "text"
	swagger20LaneByteString swagger20MediaLane = "byte"
	swagger20LaneRawOctets  swagger20MediaLane = "octets"
	swagger20LaneURLEncoded swagger20MediaLane = "urlencoded"
	swagger20LaneMultipart  swagger20MediaLane = "multipart"
)

type swagger20RequestMediaSelection struct {
	media       parsedMediaType
	declaration parsedMediaType
	lane        swagger20MediaLane
}

func effectiveSwagger20MediaSet(document *Swagger20Document, operation swagger20Operation, field string) (swagger20MediaSet, error) {
	member := operation.raw.array(field)
	if !member.present {
		member = document.root.array(field)
	}
	if !member.present {
		return swagger20MediaSet{}, nil
	}
	if !member.valid {
		return swagger20MediaSet{}, fmt.Errorf("effective %s is not an array", field)
	}
	entries := make([]swagger20MediaEntry, len(member.value))
	identities := map[string]int{}
	for index, value := range member.value {
		raw, ok := value.(string)
		if !ok || raw == "" {
			return swagger20MediaSet{}, fmt.Errorf("effective %s member %d is not a nonempty string", field, index)
		}
		parsed, err := parseMediaDeclaration(raw)
		entries[index] = swagger20MediaEntry{raw: raw, parsed: parsed, parseErr: err}
		if err == nil {
			identities[parsed.semanticIdentity]++
		}
	}
	for index := range entries {
		if entries[index].parseErr == nil && identities[entries[index].parsed.semanticIdentity] > 1 {
			entries[index].colliding = true
		}
	}
	return swagger20MediaSet{entries: entries}, nil
}

func swagger20PayloadFor(parameters *swagger20ParameterSet, document *Swagger20Document) (swagger20PayloadModel, error) {
	if parameters.body != nil {
		schema, _ := parameters.body.raw.member("schema")
		declaration, err := resolveSwagger20SchemaDeclaration(document.graph, schema, parameters.body.resource, false)
		if err != nil {
			return swagger20PayloadModel{}, fmt.Errorf("body Parameter schema: %w", err)
		}
		return swagger20PayloadModel{kind: swagger20PayloadBody, body: parameters.body, declaration: declaration}, nil
	}
	form := make([]*swagger20Parameter, 0)
	for _, parameter := range parameters.nonBody {
		if parameter.in == Swagger20ParameterFormData {
			form = append(form, parameter)
		}
	}
	swagger20ParameterSort(form)
	if len(form) > 0 {
		return swagger20PayloadModel{kind: swagger20PayloadForm, form: form}, nil
	}
	return swagger20PayloadModel{}, nil
}

func selectSwagger20RequestMedia(set swagger20MediaSet, model swagger20PayloadModel, configured string) (*swagger20RequestMediaSelection, error) {
	if model.kind == "" {
		return nil, fmt.Errorf("payload was supplied but the operation declares no request payload model")
	}
	if configured != "" {
		wanted, err := parseRevision3MediaType(configured)
		if err != nil {
			return nil, fmt.Errorf("configuration.requestMedia: %w", err)
		}
		return selectConfiguredSwagger20RequestMedia(set, model, wanted)
	}
	type candidate struct {
		entry swagger20MediaEntry
		lane  swagger20MediaLane
	}
	var candidates []candidate
	for _, entry := range set.entries {
		if entry.parseErr != nil || entry.colliding {
			continue
		}
		if entry.parsed.rangeSpecificity < 2 {
			if swagger20RangeHasUsableLane(entry.parsed, model) {
				candidates = append(candidates, candidate{entry: entry})
			}
			continue
		}
		lane, err := swagger20LaneForConcrete(entry.parsed, model)
		if err == nil {
			candidates = append(candidates, candidate{entry: entry, lane: lane})
		}
	}
	if len(candidates) == 1 && candidates[0].entry.parsed.rangeSpecificity == 2 {
		return &swagger20RequestMediaSelection{
			media: candidates[0].entry.parsed, declaration: candidates[0].entry.parsed, lane: candidates[0].lane,
		}, nil
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("effective consumes has no usable request-media candidate")
	}
	return nil, swagger20ConfigRequired("requestMedia", "")
}

func selectConfiguredSwagger20RequestMedia(set swagger20MediaSet, model swagger20PayloadModel, wanted parsedMediaType) (*swagger20RequestMediaSelection, error) {
	type match struct {
		entry swagger20MediaEntry
	}
	bestSpecificity, bestParameters := -1, -1
	var best []match
	for _, entry := range set.entries {
		if entry.parseErr != nil || entry.colliding || !requestMediaDeclarationMatches(entry.parsed, wanted) {
			continue
		}
		specificity, parameters := entry.parsed.rangeSpecificity, len(entry.parsed.params)
		switch {
		case specificity > bestSpecificity || specificity == bestSpecificity && parameters > bestParameters:
			bestSpecificity, bestParameters = specificity, parameters
			best = []match{{entry: entry}}
		case specificity == bestSpecificity && parameters == bestParameters:
			best = append(best, match{entry: entry})
		}
	}
	if len(best) == 0 {
		return nil, fmt.Errorf("configuration.requestMedia %q matches no non-colliding effective consumes declaration", wanted.canonical)
	}
	if len(best) > 1 {
		labels := make([]string, len(best))
		for index := range best {
			labels[index] = best[index].entry.parsed.canonical
		}
		sort.Strings(labels)
		return nil, fmt.Errorf("configuration.requestMedia %q ambiguously matches %s", wanted.canonical, strings.Join(labels, ", "))
	}
	lane, err := swagger20LaneForConcrete(wanted, model)
	if err != nil {
		return nil, fmt.Errorf("configuration.requestMedia %q selects an unusable request lane: %w", wanted.canonical, err)
	}
	return &swagger20RequestMediaSelection{media: wanted, declaration: best[0].entry.parsed, lane: lane}, nil
}

func swagger20LaneForConcrete(media parsedMediaType, model swagger20PayloadModel) (swagger20MediaLane, error) {
	if media.rangeSpecificity != 2 {
		return "", fmt.Errorf("media type is not concrete")
	}
	if model.kind == swagger20PayloadForm {
		switch media.base {
		case "application/x-www-form-urlencoded":
			for _, parameter := range model.form {
				if parameter.typeName == "file" {
					return "", fmt.Errorf("urlencoded form cannot carry file parameter %q", parameter.name)
				}
			}
			return swagger20LaneURLEncoded, nil
		case "multipart/form-data":
			for _, parameter := range model.form {
				if !swagger20SafeMultipartName(parameter.name) {
					return "", fmt.Errorf("multipart cannot safely represent form parameter name %q", parameter.name)
				}
			}
			return swagger20LaneMultipart, nil
		default:
			return "", fmt.Errorf("formData requires application/x-www-form-urlencoded or multipart/form-data")
		}
	}
	if isSwagger20JSONMedia(media.base) {
		return swagger20LaneJSON, nil
	}
	if model.declaration.artifactByteString() {
		return swagger20LaneByteString, nil
	}
	if model.declaration.rawOctets() {
		return swagger20LaneRawOctets, nil
	}
	if isSwagger20CharacterMedia(media.base) && model.declaration.admitsStringAsSoleNonNullType() {
		if err := supportedTextCharset(media); err != nil {
			return "", err
		}
		return swagger20LaneText, nil
	}
	return "", fmt.Errorf("selected media and resolved declaration define no request byte carriage")
}

func swagger20RangeHasUsableLane(declaration parsedMediaType, model swagger20PayloadModel) bool {
	if declaration.rangeSpecificity == 2 {
		_, err := swagger20LaneForConcrete(declaration, model)
		return err == nil
	}
	candidates := []string{}
	if model.kind == swagger20PayloadForm {
		candidates = []string{"application/x-www-form-urlencoded", "multipart/form-data"}
	} else {
		candidates = []string{"application/json", "text/plain", "application/octet-stream", "image/png"}
	}
	for _, candidate := range candidates {
		parsed, _ := parseRevision3MediaType(candidate)
		if requestMediaDeclarationMatches(declaration, parsed) {
			if _, err := swagger20LaneForConcrete(parsed, model); err == nil {
				return true
			}
		}
	}
	return false
}

func encodeSwagger20RequestPayload(selection *swagger20RequestMediaSelection, model swagger20PayloadModel, routed swagger20RoutedInput, options Swagger20PrepareOptions) ([]byte, string, error) {
	if selection == nil {
		return nil, "", nil
	}
	switch selection.lane {
	case swagger20LaneJSON:
		body, err := json.Marshal(routed.body)
		if err != nil {
			return nil, "", fmt.Errorf("strict JSON request encoding: %w", err)
		}
		return body, selection.media.canonical, nil
	case swagger20LaneText:
		text, ok := routed.body.(string)
		if !ok {
			return nil, "", fmt.Errorf("character-data request lane requires a string, got %T", routed.body)
		}
		body, err := encodeTextString(text, selection.media)
		if err != nil {
			return nil, "", err
		}
		return body, selection.media.canonical, nil
	case swagger20LaneByteString:
		text, ok := routed.body.(string)
		if !ok {
			return nil, "", fmt.Errorf("format byte request lane requires a Base64-character string, got %T", routed.body)
		}
		if _, err := base64.StdEncoding.DecodeString(text); err != nil {
			return nil, "", fmt.Errorf("format byte request value is not standard padded Base64: %w", err)
		}
		return []byte(text), selection.media.canonical, nil
	case swagger20LaneRawOctets:
		text, ok := routed.body.(string)
		if !ok {
			return nil, "", fmt.Errorf("raw-octet request lane requires a canonical Base64 string, got %T", routed.body)
		}
		body, err := canonicalBase64BoundaryBytes("body", text)
		if err != nil {
			return nil, "", err
		}
		return body, selection.media.canonical, nil
	case swagger20LaneURLEncoded:
		return encodeSwagger20URLEncoded(routed.formData), selection.media.canonical, nil
	case swagger20LaneMultipart:
		return encodeSwagger20Multipart(routed.formData, selection.media, options.PropertyMedia)
	default:
		return nil, "", fmt.Errorf("unknown Swagger 2.0 request-media lane %q", selection.lane)
	}
}
