package common

import (
	"os"
	"strings"
)

// IsProdRuntime reports whether the QSPILL_CONTROLLER_ENV environment
// variable is set to "prod" (case-insensitive). Default behaviour (env unset)
// is development.
func IsProdRuntime() bool {
	runEvnVal, hasEnv := os.LookupEnv(QSpillControllerRuntimeEnv)
	if hasEnv {
		return strings.Compare(strings.ToLower(runEvnVal), "prod") == 0
	}

	return false
}
