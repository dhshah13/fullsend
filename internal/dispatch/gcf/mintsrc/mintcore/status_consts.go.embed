package mintcore

// StatusGitHubGroup is stamped into the deployed binary at
// build/deploy time, the same mechanism used for Version and Commit.
// In development and tests it defaults to the empty string.
//
// StatusGitHubGroup is an ORG/TEAM slug. When the github build tag
// is active, the GitHub status validator checks that the caller is a
// member of this team. When the tag is absent (stub), the value is
// unused.
var StatusGitHubGroup string
