const neutralPackages = new Set(["@openbindings/json", "@openbindings/json-schema"]);

export const isForbiddenPackage = (name) =>
  name.startsWith("@openbindings/") && !neutralPackages.has(name);
// The Go client's only authorities are OpenAPI and the RFCs it relies on, so
// it depends on no OpenBindings package but its own.
export const isForbiddenGoPackage = (name) =>
  name.startsWith("github.com/openbindings/") &&
  name !== "github.com/openbindings/openapi-client/go" &&
  !name.startsWith("github.com/openbindings/openapi-client/go/");
