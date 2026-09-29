/** Remove URL authority userinfo without ever exposing it as a persisted value. */
export function stripUrlCredentials(raw: string): string {
  try {
    const url = new URL(raw);
    if (url.username || url.password) {
      url.username = "";
      url.password = "";
      return url.toString();
    }
    return raw;
  } catch {
    // URL rejects some incomplete/malformed authorities. Still remove the
    // unambiguous `userinfo@` segment after a scheme so a bad paste cannot put
    // a password in settings before request-time URL validation rejects it.
    const schemeEnd = raw.indexOf("://");
    if (schemeEnd < 0) return raw;
    const authorityStart = schemeEnd + 3;
    let authorityEnd = raw.length;
    for (const separator of ["/", "?", "#"]) {
      const index = raw.indexOf(separator, authorityStart);
      if (index >= 0 && index < authorityEnd) authorityEnd = index;
    }
    const at = raw.lastIndexOf("@", authorityEnd - 1);
    if (at < authorityStart) return raw;
    return raw.slice(0, authorityStart) + raw.slice(at + 1);
  }
}
