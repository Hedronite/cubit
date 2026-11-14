package config

import "regexp"

// The scan schema's workspace-name pattern.
func regexpWorkspace() *regexp.Regexp {
	return regexp.MustCompile(`^[a-z0-9._-]+(/[a-z0-9._-]+)*$`)
}
