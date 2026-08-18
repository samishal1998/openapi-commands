package oascmd

import (
	"fmt"
	"slices"
	"strings"
)

// ValidateEnum checks that every value is one of allowed. It backs enum
// validation in generated commands and is shared with the runtime builder.
func ValidateEnum(flag string, allowed []string, values ...string) error {
	for _, v := range values {
		if !slices.Contains(allowed, v) {
			return fmt.Errorf("invalid value %q for --%s (one of: %s)",
				v, flag, strings.Join(allowed, ", "))
		}
	}
	return nil
}
