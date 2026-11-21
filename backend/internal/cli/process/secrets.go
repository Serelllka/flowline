package process

import "sync"

// Configured API env names may not contain TOKEN or KEY. Keep their values in
// memory solely for redaction, including after a running profile is replaced.
var configuredSecrets sync.Map

func RegisterSecret(value string) {
	if value != "" {
		configuredSecrets.Store(value, struct{}{})
	}
}
