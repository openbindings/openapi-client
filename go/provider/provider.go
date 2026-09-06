// Package provider exposes advanced OpenAPI-native analysis and execution
// facts for generators and protocol adapters. Application code should prefer
// the root openapi package's Client surface.
package provider

import (
	"context"
	"errors"

	runtime "github.com/openbindings/openapi-client/go/internal/runtime"
)

var errArtifactRequired = errors.New("OpenAPI provider artifact is required")

type ArtifactLoadOptions = runtime.ArtifactLoadOptions
type Analysis = runtime.Analysis
type ClientOptions = runtime.ClientOptions
type Edition = runtime.Edition
type ParameterConverter = runtime.ParameterConverter
type Source = runtime.Source
type InboundOperationDisposition = runtime.InboundOperationDisposition
type InboundOperationTarget = runtime.InboundOperationTarget
type OpenAPI32Overlay = runtime.OpenAPI32Overlay
type OpenAPI32ResponseSelection = runtime.OpenAPI32ResponseSelection
type OperationDisposition = runtime.OperationDisposition
type OperationAnalysis = runtime.OperationAnalysis
type OperationInfo = runtime.OperationInfo
type OperationReference = runtime.OperationReference
type OperationResolutionError = runtime.OperationResolutionError
type OperationTarget = runtime.OperationTarget
type Profile = runtime.Profile
type ProjectionAnalysis = runtime.ProjectionAnalysis
type ProjectionBinding = runtime.ProjectionBinding
type ProjectionCoverageEntry = runtime.ProjectionCoverageEntry
type ProjectionDependency = runtime.ProjectionDependency
type ProjectionDocument = runtime.ProjectionDocument
type ProjectionFailure = runtime.ProjectionFailure
type ProjectionInputCorrespondence = runtime.ProjectionInputCorrespondence
type ProjectionOperation = runtime.ProjectionOperation
type ProjectionParameterRoute = runtime.ProjectionParameterRoute
type ProjectionWarning = runtime.ProjectionWarning
type SchemaDeclaration = runtime.SchemaDeclaration
type SupportDisposition = runtime.SupportDisposition
type ServerResolutionRequiredError = runtime.ServerResolutionRequiredError
type ServerSelection = runtime.ServerSelection
type Swagger20Document = runtime.Swagger20Document
type Swagger20ParameterLocation = runtime.Swagger20ParameterLocation
type Swagger20Parameters = runtime.Swagger20Parameters
type Swagger20PrepareOptions = runtime.Swagger20PrepareOptions
type Swagger20Source = runtime.Swagger20Source
type Swagger20SynthesisDocument = runtime.Swagger20SynthesisDocument
type Swagger20SynthesisOperation = runtime.Swagger20SynthesisOperation

const (
	CodeRefused             = runtime.CodeRefused
	EditionSwagger20        = runtime.EditionSwagger20
	EditionOpenAPI300       = runtime.EditionOpenAPI300
	EditionOpenAPI301       = runtime.EditionOpenAPI301
	EditionOpenAPI302       = runtime.EditionOpenAPI302
	EditionOpenAPI303       = runtime.EditionOpenAPI303
	EditionOpenAPI304       = runtime.EditionOpenAPI304
	EditionOpenAPI310       = runtime.EditionOpenAPI310
	EditionOpenAPI311       = runtime.EditionOpenAPI311
	EditionOpenAPI312       = runtime.EditionOpenAPI312
	EditionOpenAPI320       = runtime.EditionOpenAPI320
	OperationTargetInvalid  = runtime.OperationTargetInvalid
	OperationTargetExcluded = runtime.OperationTargetExcluded
	ParameterInQueryString  = runtime.ParameterInQueryString
)

var (
	RecognizeRepresentation             = runtime.RecognizeRepresentation
	ClassifyOpenAPI32SequentialResponse = runtime.ClassifyOpenAPI32SequentialResponse
	DecodeResponseBody                  = runtime.DecodeResponseBody
	DocumentInboundOperationInventory   = runtime.DocumentInboundOperationInventory
	EffectiveServerSet                  = runtime.EffectiveServerSet
	FullProfile                         = runtime.FullProfile
	IsServerBaseURL                     = runtime.IsServerBaseURL
	LoadSwagger20                       = runtime.LoadSwagger20
	NewServerSet                        = runtime.NewServerSet
	ParseOperationReference             = runtime.ParseOperationReference
	ResolveSchemaDeclaration            = runtime.ResolveSchemaDeclaration
	ValidateSecurityRequirementCarriage = runtime.ValidateSecurityRequirementCarriage
	ValidateSwagger20Selector           = runtime.ValidateSwagger20Selector
)

// Artifact is an immutable loaded analysis workspace. The parser graph is not
// part of the provider contract; consumers use detached inventory and analysis
// values instead.
type Artifact struct {
	inner *runtime.Artifact
}

// LoadArtifact loads one provider workspace using the same native loader and
// edition policy as the application client.
func LoadArtifact(ctx context.Context, source Source, options ArtifactLoadOptions) (*Artifact, error) {
	artifact, err := runtime.LoadArtifact(ctx, source, options)
	if err != nil {
		return nil, err
	}
	return &Artifact{inner: artifact}, nil
}

// Analyze loads one Swagger 2.0 or OpenAPI 3.x artifact and returns the
// detached generator view produced from the same compiled model used by the
// native client's preflight and invocation paths.
func Analyze(ctx context.Context, source Source, options ClientOptions) (Analysis, error) {
	client, err := runtime.Load(ctx, source, options)
	if err != nil {
		return Analysis{}, err
	}
	return client.Analysis(), nil
}

// AnalyzeProjection returns the complete detached generator contract. It is
// built by the native planner over the same loaded artifact used by calls and
// contains no OpenBindings SDK or OBI types.
func AnalyzeProjection(ctx context.Context, source Source, options ClientOptions) (ProjectionAnalysis, error) {
	client, err := runtime.Load(ctx, source, options)
	if err != nil {
		return ProjectionAnalysis{}, err
	}
	return client.Projection()
}

func (a *Artifact) Edition() Edition {
	if a == nil || a.inner == nil {
		return ""
	}
	return a.inner.Edition
}

func (a *Artifact) EntryBytes() []byte {
	if a == nil || a.inner == nil {
		return nil
	}
	return a.inner.EntryBytes()
}

func (a *Artifact) OpenAPI32() *OpenAPI32Overlay {
	if a == nil || a.inner == nil {
		return nil
	}
	return a.inner.OpenAPI32()
}

func (a *Artifact) Refusal() error {
	if a == nil || a.inner == nil {
		return nil
	}
	return a.inner.Refusal()
}

func (a *Artifact) SourceExclusion() error {
	if a == nil || a.inner == nil {
		return nil
	}
	return a.inner.SourceExclusion()
}

func (a *Artifact) Operations() []OperationInfo {
	if a == nil || a.inner == nil {
		return nil
	}
	return a.inner.Operations()
}

func (a *Artifact) OperationInventory() []OperationDisposition {
	if a == nil || a.inner == nil {
		return nil
	}
	return a.inner.OperationInventory()
}

func (a *Artifact) ResolveOperation(ref string) (*OperationTarget, error) {
	if a == nil || a.inner == nil {
		return nil, errArtifactRequired
	}
	return a.inner.ResolveOperation(ref)
}

func (a *Artifact) InboundOperationInventory() []InboundOperationDisposition {
	if a == nil || a.inner == nil {
		return nil
	}
	return a.inner.InboundOperationInventory()
}

func (a *Artifact) SelectOpenAPI32Response(target *OperationTarget, statusCode int) (OpenAPI32ResponseSelection, error) {
	if a == nil || a.inner == nil {
		return OpenAPI32ResponseSelection{}, errArtifactRequired
	}
	return a.inner.SelectOpenAPI32Response(target, statusCode)
}

func (a *Artifact) WithOperationTarget(target *OperationTarget) (*Artifact, error) {
	if a == nil || a.inner == nil {
		return nil, errArtifactRequired
	}
	artifact, err := a.inner.WithOperationTarget(target)
	if err != nil {
		return nil, err
	}
	return &Artifact{inner: artifact}, nil
}

// ValidateSwagger20Operation resolves and prepares one Swagger 2.0 operation
// without exposing the private execution engine itself.
func ValidateSwagger20Operation(ctx context.Context, options Swagger20PrepareOptions) error {
	_, err := runtime.NewEngine(nil).PrepareSwagger20(ctx, options)
	return err
}
