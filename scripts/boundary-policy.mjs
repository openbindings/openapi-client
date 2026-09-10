const neutralPackages = new Set(["@openbindings/json", "@openbindings/json-schema"]);
const neutralGoPackages = new Set([
  "github.com/openbindings/openbindings-go/jsonvalue",
  "github.com/openbindings/openbindings-go/internal/jstring",
  "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec",
]);

export const isForbiddenPackage = (name) =>
  name.startsWith("@openbindings/") && !neutralPackages.has(name);
export const isForbiddenGoPackage = (name) =>
  name.startsWith("github.com/openbindings/openbindings-go") && !neutralGoPackages.has(name);
