package schema

import (
	"encoding/json"
	"fmt"
)

// MapAuthWhoAmI converts Jenkins's whoAmI response into the public output
// shape. Authorities is always non-nil so empty authorities render as [].
func MapAuthWhoAmI(raw []byte) (AuthWhoAmI, error) {
	var out AuthWhoAmI
	if err := json.Unmarshal(raw, &out); err != nil {
		return AuthWhoAmI{}, fmt.Errorf("MapAuthWhoAmI: %w", err)
	}
	if out.Authorities == nil {
		out.Authorities = []string{}
	}
	return out, nil
}
