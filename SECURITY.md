# Security Policy

serialize.go parses untrusted network data, so security reports are taken seriously.

## Reporting a vulnerability

Report vulnerabilities privately via
[GitHub security advisories](https://github.com/mas-bandwidth/serialize.go/security/advisories/new)
or by email to glenn@mas-bandwidth.com. Please do not open public issues for
security reports.

## What counts as a vulnerability

For maliciously crafted or truncated packet data, the library guarantees that it:

* never panics (panics are reserved for API misuse by the calling program),
* never reads or writes out of bounds,
* never does unbounded work or allocation when the documented loop rules
  ([docs/reading_untrusted_data.md](docs/reading_untrusted_data.md)) are followed.

Any violation of these guarantees is a security bug. So is any input that the Go
and C++ libraries decode differently, since wire compatibility between the two is
a core guarantee.

## Supported versions

The latest release. Fixes are not backported.
