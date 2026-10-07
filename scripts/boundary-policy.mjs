const neutralPackages = new Set(["@openbindings/json", "@openbindings/json-schema"]);

export const isForbiddenPackage = (name) =>
  name.startsWith("@openbindings/") && !neutralPackages.has(name);
// The Go client's only authorities are OpenAPI and the RFCs it relies on, so
// it depends on no OpenBindings package but its own.
export const isForbiddenGoPackage = (name) =>
  name.startsWith("github.com/openbindings/") &&
  name !== "github.com/openbindings/openapi-client/go" &&
  !name.startsWith("github.com/openbindings/openapi-client/go/");

// Go names say what code does or what a test checks, never how it was
// developed. A development-process label is a stage, review or repair
// marker (Stage6, Review9, Repair), a record word (Rulings, Ledger, Pins,
// Regress), a milestone tag (Stream7, IP4F8, Schema9, Prepared8) or a
// finding ID (C1, C41 to C48, F1 to F41, G1 to G18, H1 to H10, K1 to K11,
// T1_15, VP7, VN8, VPP1, RQ3 to RQ6, IFP9, IFP10, A8, A10). Each matches only
// as a whole word of a name: Preview, Reviewer, Regression, UTF8, HTTP2 and
// K8s carry none. HTTP/2 is spelled HTTP2 in a name, since H2 is a finding ID.
const wordLabels = String.raw`Stage\d+|Review\d*|Repair|Rulings?|Ledgers?|Pins|Stream7|Schema9|Prepared8|Regress\d*`;
const findingIDs = String.raw`IP4F\d+|C(?:1|4[1-8])|F(?:[1-9]|[1-3]\d|4[01])|G(?:1[0-8]|[1-9])|H(?:10|[1-9])|K(?:1[01]|[1-9])|T[12]_\d+|VPP?\d+|VN\d+|RQ[3-6]|IFP(?:9|10)|A(?:8|10)`;
// In lowercase, VP, VPP and VN also appear without their number (vppPaths).
const lowerFindingIDs = String.raw`ip4f\d+|c(?:1|4[1-8])|f(?:[1-9]|[1-3]\d|4[01])|g(?:1[0-8]|[1-9])|h(?:10|[1-9])|k(?:1[01]|[1-9])|t[12]_\d+|vpp?\d*|vn\d*|rq[3-6]|ifp(?:9|10)|a(?:8|10)`;
// A word of a mixed-case name starts with a capital letter; a finding ID is
// a word only where no capital precedes it.
const camelLabel = new RegExp(String.raw`(?:${wordLabels})(?![a-z])|(?<![A-Z])(?:${findingIDs})(?![a-z0-9])`);
// A name's first word is lowercase. A lowercase finding ID is a word only
// when another word follows it, so short locals such as c1 or k2 are names.
const leadingLabel = new RegExp(String.raw`^(?:(?:${wordLabels.toLowerCase()})(?![a-z])|(?:${lowerFindingIDs})(?=[A-Z_]))`);
// File names are lowercase words joined by underscores, and some join a
// word to a prefix (docreview). Finding IDs and Pins count only as whole
// words there.
const fileLabel = new RegExp(String.raw`(?<=^|_)(?:pins|${lowerFindingIDs})(?=[_.]|$)|stage\d|(?<!p)review|repair|ruling|ledger|stream7|schema9|prepared8|regress\d*(?!ion)`);

// processLabelInGoName returns the development-process label a Go
// identifier carries, or undefined.
export const processLabelInGoName = (name) =>
  (leadingLabel.exec(name) ?? camelLabel.exec(name))?.[0];

// processLabelInGoFileName returns the development-process label a Go file
// name carries, or undefined.
export const processLabelInGoFileName = (name) => fileLabel.exec(name)?.[0];

// blankGoText returns source with every comment and string, rune and raw
// string literal replaced by spaces, newlines kept, so that what remains is
// code at the same lines.
const blankGoText = (source) => {
  let out = "";
  for (let i = 0; i < source.length;) {
    const c = source[i];
    let end;
    if (c === "/" && source[i + 1] === "/") {
      end = source.indexOf("\n", i);
      if (end < 0) end = source.length;
    } else if (c === "/" && source[i + 1] === "*") {
      end = source.indexOf("*/", i + 2);
      end = end < 0 ? source.length : end + 2;
    } else if (c === "`") {
      end = source.indexOf("`", i + 1);
      end = end < 0 ? source.length : end + 1;
    } else if (c === '"' || c === "'") {
      end = i + 1;
      while (end < source.length && source[end] !== c && source[end] !== "\n") {
        end += source[end] === "\\" ? 2 : 1;
      }
      end += 1;
    } else {
      out += c;
      i++;
      continue;
    }
    out += source.slice(i, end).replace(/[^\n]/g, " ");
    i = end;
  }
  return out;
};

// goDeclaredNames returns the names Go source declares with func (methods
// included), type, var and const, each with its line.
export const goDeclaredNames = (source) => {
  const code = blankGoText(source);
  const lineAt = (index) => code.slice(0, index).split("\n").length;
  const names = [];
  const add = (list, index) => {
    for (const name of list.split(",")) names.push({ name: name.trim(), line: lineAt(index) });
  };
  const nameList = /[A-Za-z_]\w*(?:\s*,\s*[A-Za-z_]\w*)*/y;
  for (const m of code.matchAll(/^func\s*(?:\([^)]*\)\s*)?([A-Za-z_]\w*)/gm)) {
    add(m[1], m.index);
  }
  for (const m of code.matchAll(/\b(?:var|const|type)\s+([A-Za-z_]\w*(?:\s*,\s*[A-Za-z_]\w*)*)/g)) {
    add(m[0].startsWith("type") ? m[1].split(",")[0] : m[1], m.index);
  }
  // In a grouped declaration, each spec starts a line or follows a
  // semicolon at the group's own depth.
  for (const m of code.matchAll(/\b(?:var|const|type)\s*\(/g)) {
    let depth = 0;
    let start = true;
    for (let i = m.index + m[0].length; i < code.length; i++) {
      const c = code[i];
      if (depth === 0 && start && /[A-Za-z_]/.test(c)) {
        nameList.lastIndex = i;
        const list = nameList.exec(code)[0];
        add(m[0].startsWith("type") ? list.split(",")[0] : list, i);
        i += list.length - 1;
        start = false;
      } else if ("([{".includes(c)) {
        depth++;
        start = false;
      } else if (")]}".includes(c)) {
        if (depth === 0) break;
        depth--;
      } else if (c === "\n" || c === ";") {
        start = depth === 0;
      } else if (!/\s/.test(c)) {
        start = false;
      }
    }
  }
  return names;
};
