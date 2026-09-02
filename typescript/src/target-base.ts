// This file answers one question for §10 (servers and target URL) of the
// OpenAPI binding-specification family: does a
// server URL, after Server Variable substitution, denote a target address?
//
// It exists because the answer used to come from the HOST LANGUAGE's URL
// parser — the WHATWG URL parser here, Go's net/url in the Go twins — and
// neither is an authority this specification incorporates. The two disagree,
// and the corpus caught both halves in opposite directions: net/url rejects a
// server URL carrying an unsubstituted template variable and accepts one whose
// http authority has no host, while the WHATWG parser does the reverse. Each
// engine was right about one shape by accident.
//
// The predicate below is derived from the two authorities instead. A server
// URL denotes a target address iff
//
//   1. it matches RFC 3986's URI production — `scheme ":" hier-part [ "?" query ]
//      [ "#" fragment ]`, Appendix A, Collected ABNF — so it carries a scheme and
//      every character is drawn from the productions that ABNF defines; and
//   2. when its scheme is http or https (compared ASCII case-insensitively), its
//      authority carries a NON-EMPTY host, per RFC 9110 §4.2.1 and §4.2.2: "A
//      sender MUST NOT generate an 'http' URI with an empty host identifier. A
//      recipient that processes such a URI reference MUST reject it as invalid."
//
// Both are incorporated: §2 of the binding specification incorporates RFC 9110
// "for plain HTTP semantics", and §9.3 and §11 name RFC 3986 for server-URL
// resolution. This is a restatement of an incorporated consequence, not a
// convention of this implementation's own.
//
// Deliberately RFC 3986's `URI` and not its `absolute-URI`: the two differ only
// in the optional fragment, and refusing a fragment-bearing server URL is a
// separate per-edition question that only OAS 3.1.2 speaks to ("Query and
// fragment MUST NOT be part of this URL", §4.8.5.1, absent from the other seven
// accepted editions). Whether a URL carries an address, and whether the artifact
// was allowed to write it, are different questions.
//
// Equally deliberately, nothing here repairs the artifact's value: a space is
// not percent-encoded, a non-ASCII host is not IDNA-mapped, and an unsubstituted
// `{name}` is not guessed at. A server URL that is not a URI leaves the target
// undetermined, and §9.3 sends that to the `server` configuration point.
//
// Twinned byte-for-byte with the Go implementations in
// openbindings-go/formats/openapi/target_base.go and
// openapi-client/go/target_base.go, and pinned by the shared case table
// src/testdata/server-target-base-cases.json.

/**
 * Reports whether `s` is a URI under RFC 3986 that also satisfies RFC 9110's
 * host requirement for the http and https schemes.
 */
export function denotesTargetBase(s: string): boolean {
  const colon = s.indexOf(":");
  if (colon <= 0) return false;
  if (!isURIScheme(s.slice(0, colon))) return false;
  const scheme = s.slice(0, colon).toLowerCase();
  let rest = s.slice(colon + 1);

  // URI = scheme ":" hier-part [ "?" query ] [ "#" fragment ]. A "#" cannot
  // appear inside hier-part or query, and a "?" cannot appear inside
  // hier-part, so the first occurrence of each delimits the component.
  const hash = rest.indexOf("#");
  if (hash >= 0) {
    // fragment = *( pchar / "/" / "?" )
    if (!isCharRun(rest.slice(hash + 1), pcharPlusSlashQuestion)) return false;
    rest = rest.slice(0, hash);
  }
  const question = rest.indexOf("?");
  if (question >= 0) {
    // query = *( pchar / "/" / "?" )
    if (!isCharRun(rest.slice(question + 1), pcharPlusSlashQuestion)) return false;
    rest = rest.slice(0, question);
  }

  let host = "";
  let hasAuthority = false;
  if (rest.startsWith("//")) {
    // hier-part = "//" authority path-abempty
    const after = rest.slice(2);
    const slash = after.indexOf("/");
    const authority = slash >= 0 ? after.slice(0, slash) : after;
    const path = slash >= 0 ? after.slice(slash) : "";
    // path-abempty = *( "/" segment ); empty, or begins with "/".
    if (path !== "" && !isCharRun(path, pcharPlusSlash)) return false;
    const parsedHost = uriAuthorityHost(authority);
    if (parsedHost === null) return false;
    host = parsedHost;
    hasAuthority = true;
  } else {
    // hier-part = path-absolute / path-rootless / path-empty. All three are
    // pchar-and-"/" runs; path-absolute additionally forbids a leading "//",
    // which is unreachable here because that prefix took the branch above.
    if (rest !== "" && !isCharRun(rest, pcharPlusSlash)) return false;
  }

  if (scheme === "http" || scheme === "https") {
    // RFC 9110 §4.2.1 / §4.2.2.
    if (!hasAuthority || host === "") return false;
  }
  return true;
}

/**
 * Reports whether `s` begins with an RFC 3986 scheme followed by ":". A string
 * that does is never a relative reference — path-noscheme forbids a colon in a
 * relative reference's first segment — so resolving it against the artifact's
 * base URI cannot rescue it. It names an address, and the address does not
 * exist.
 */
export function hasURIScheme(s: string): boolean {
  const colon = s.indexOf(":");
  return colon > 0 && isURIScheme(s.slice(0, colon));
}

/**
 * Splits an RFC 3986 authority into its host and validates every component:
 * authority = [ userinfo "@" ] host [ ":" port ]. Returns null when the
 * authority is not well formed.
 */
function uriAuthorityHost(authority: string): string | null {
  // userinfo admits ":" but not "@", so the LAST "@" ends it.
  const at = authority.lastIndexOf("@");
  if (at >= 0) {
    // userinfo = *( unreserved / pct-encoded / sub-delims / ":" )
    if (!isCharRun(authority.slice(0, at), userinfoChar)) return null;
    authority = authority.slice(at + 1);
  }
  if (authority.startsWith("[")) {
    // IP-literal = "[" ( IPv6address / IPvFuture ) "]"
    const closing = authority.indexOf("]");
    if (closing < 0 || !isIPLiteralInner(authority.slice(1, closing))) return null;
    if (!isURIPort(authority.slice(closing + 1))) return null;
    return authority.slice(0, closing + 1);
  }
  // Neither reg-name nor IPv4address admits ":", so the LAST ":" starts the
  // port. reg-name subsumes IPv4address character-wise, so one check covers
  // both alternatives of `host`.
  const colon = authority.lastIndexOf(":");
  const host = colon >= 0 ? authority.slice(0, colon) : authority;
  const remainder = colon >= 0 ? authority.slice(colon) : "";
  if (!isCharRun(host, regNameChar) || !isURIPort(remainder)) return null;
  return host;
}

/** Accepts an empty remainder or ":" *DIGIT. */
function isURIPort(remainder: string): boolean {
  if (remainder === "") return true;
  if (remainder.charAt(0) !== ":") return false;
  for (let i = 1; i < remainder.length; i += 1) {
    if (!isDigit(remainder.charAt(i))) return false;
  }
  return true;
}

/**
 * Accepts IPv6address or IPvFuture, the two alternatives RFC 3986 admits
 * between the square brackets.
 */
function isIPLiteralInner(inner: string): boolean {
  if (inner.startsWith("v") || inner.startsWith("V")) {
    // IPvFuture = "v" 1*HEXDIG "." 1*( unreserved / sub-delims / ":" )
    const dot = inner.indexOf(".");
    if (dot < 2 || dot + 1 >= inner.length) return false;
    for (let i = 1; i < dot; i += 1) {
      if (!isHexDigit(inner.charAt(i))) return false;
    }
    return isCharRun(inner.slice(dot + 1), ipvFutureTailChar);
  }
  return isIPv6Address(inner);
}

/**
 * Checks RFC 3986's IPv6address alternatives: eight h16 groups, with at most
 * one "::" eliding one or more of them, and an optional trailing IPv4address
 * occupying the last two.
 */
function isIPv6Address(s: string): boolean {
  if (s === "") return false;
  let head = s;
  let tail = "";
  let elided = false;
  const i = s.indexOf("::");
  if (i >= 0) {
    if (s.slice(i + 2).includes("::")) return false;
    head = s.slice(0, i);
    tail = s.slice(i + 2);
    elided = true;
  }
  const headGroups = ipv6Groups(head);
  if (headGroups === null) return false;
  const tailGroups = ipv6Groups(tail);
  if (tailGroups === null) return false;
  if (elided) return headGroups + tailGroups <= 7;
  return headGroups === 8;
}

/**
 * Counts the h16 groups in one colon-separated run, allowing the last one to be
 * an IPv4address (which occupies two groups). An empty run is zero groups.
 * Returns null when the run is not well formed.
 */
function ipv6Groups(run: string): number | null {
  if (run === "") return 0;
  const parts = run.split(":");
  let count = 0;
  for (let i = 0; i < parts.length; i += 1) {
    const part = parts[i] ?? "";
    if (i === parts.length - 1 && part.includes(".")) {
      if (!isIPv4Address(part)) return null;
      count += 2;
      continue;
    }
    if (part.length < 1 || part.length > 4) return null;
    for (let j = 0; j < part.length; j += 1) {
      if (!isHexDigit(part.charAt(j))) return null;
    }
    count += 1;
  }
  return count;
}

/**
 * Checks RFC 3986's dec-octet "." dec-octet "." dec-octet "." dec-octet, which
 * forbids a leading zero on a multi-digit octet.
 */
function isIPv4Address(s: string): boolean {
  const parts = s.split(".");
  if (parts.length !== 4) return false;
  for (const part of parts) {
    if (part.length < 1 || part.length > 3) return false;
    if (part.length > 1 && part.charAt(0) === "0") return false;
    let value = 0;
    for (let i = 0; i < part.length; i += 1) {
      if (!isDigit(part.charAt(i))) return false;
      value = value * 10 + (part.charCodeAt(i) - 0x30);
    }
    if (value > 255) return false;
  }
  return true;
}

/** Checks scheme = ALPHA *( ALPHA / DIGIT / "+" / "-" / "." ). */
function isURIScheme(s: string): boolean {
  if (s === "" || !isAlpha(s.charAt(0))) return false;
  for (let i = 1; i < s.length; i += 1) {
    const c = s.charAt(i);
    if (!isAlpha(c) && !isDigit(c) && c !== "+" && c !== "-" && c !== ".") return false;
  }
  return true;
}

// The character classes below are RFC 3986 Appendix A's, named for the
// production each one serves.
function unreservedChar(c: string): boolean {
  return isAlpha(c) || isDigit(c) || c === "-" || c === "." || c === "_" || c === "~";
}

function subDelimChar(c: string): boolean {
  return "!$&'()*+,;=".includes(c);
}

function regNameChar(c: string): boolean {
  return unreservedChar(c) || subDelimChar(c);
}

function userinfoChar(c: string): boolean {
  return regNameChar(c) || c === ":";
}

function ipvFutureTailChar(c: string): boolean {
  return unreservedChar(c) || subDelimChar(c) || c === ":";
}

function pcharPlusSlash(c: string): boolean {
  return userinfoChar(c) || c === "@" || c === "/";
}

function pcharPlusSlashQuestion(c: string): boolean {
  return pcharPlusSlash(c) || c === "?";
}

/**
 * Reports whether every character of `s` is admitted by `allow`, treating a "%"
 * as the start of a pct-encoded triplet. Percent is in no character class, so
 * this is the one place the grammar is not a pure per-character test.
 */
function isCharRun(s: string, allow: (c: string) => boolean): boolean {
  for (let i = 0; i < s.length; i += 1) {
    if (s.charAt(i) === "%") {
      if (i + 2 >= s.length || !isHexDigit(s.charAt(i + 1)) || !isHexDigit(s.charAt(i + 2))) return false;
      i += 2;
      continue;
    }
    if (!allow(s.charAt(i))) return false;
  }
  return true;
}

function isAlpha(c: string): boolean {
  return (c >= "A" && c <= "Z") || (c >= "a" && c <= "z");
}

function isDigit(c: string): boolean {
  return c >= "0" && c <= "9";
}

function isHexDigit(c: string): boolean {
  return isDigit(c) || (c >= "a" && c <= "f") || (c >= "A" && c <= "F");
}
