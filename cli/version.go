// Package cli holds the library's release number.
//
//	Version  this library's release, kept equal to the repository's VERSION file by
//	         scripts/version.sh, so a product can report which connector it is built on. It is not
//	         protocol.Version, the wire version, which changes only when an older peer could not
//	         survive the message set.
//
// Example: fmt.Println("connector", cli.Version)
package cli

const Version = "0.2.0"
