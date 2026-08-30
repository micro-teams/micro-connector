package cli

// Version is this library's release, kept in step with the repository's VERSION file by
// scripts/version.sh. It is here so a product can report which connector it is built on when
// something on a machine behaves strangely — which, on a machine you cannot log in to, is often the
// only thing you have.
//
// Not to be confused with protocol.Version, which is the WIRE version: that one changes when the
// message set changes in a way an older peer cannot survive, which is a rarer event than a release
// and deliberately tracked separately.
const Version = "0.1.6"
