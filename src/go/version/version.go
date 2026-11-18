// Package version holds the release number of weftsync itself.
//
// This is the number a user reads off the console, not a build stamp. The git
// revision in InfoView.Version answers "which commit is this binary"; this answers
// "which release", and the two are different questions: a local build has a
// revision but no release, and a release tag can be rebuilt without changing.
//
// Bumping rules, so the number means something later:
//
//	0.1.0 -> 0.1.1   a fix; a config file written by the old build still loads
//	0.1.0 -> 0.2.0   a new setting or command; old config files still load
//	0.1.0 -> 1.0.0   the job-file format changes incompatibly
//
// While the major version is 0 the middle digit carries new features instead of
// the major one, which is the usual reading of 0.x.
package version

// Version is the product version, without a leading "v".
//
// One place, because it is displayed in the console header and in 关于, and two
// literals would disagree the first time one of them was forgotten.
const Version = "0.1.0"
