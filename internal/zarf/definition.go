package zarf

import (
	"context"
	"fmt"

	zarfload "github.com/zarf-dev/zarf/src/pkg/packager/load"
)

const SchemaURL = "https://raw.githubusercontent.com/zarf-dev/zarf/main/zarf.schema.json"

// ValidateDefinition parses and validates a package definition using Zarf's Go APIs.
func ValidateDefinition(ctx context.Context, path string) error {
	if _, err := zarfload.PackageDefinition(ctx, path, zarfload.DefinitionOptions{SkipVersionCheck: true}); err != nil {
		return fmt.Errorf("zarf package definition validation failed: %w", err)
	}
	return nil
}
