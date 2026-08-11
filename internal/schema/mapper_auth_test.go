package schema_test

import (
	"testing"

	"github.com/addozhang/jk/internal/schema"
)

func Test_MapAuthWhoAmI(t *testing.T) {
	got, err := schema.MapAuthWhoAmI([]byte(`{"authenticated":true,"name":"alice","authorities":["authenticated","read"]}`))
	if err != nil {
		t.Fatalf("MapAuthWhoAmI: %v", err)
	}
	if !got.Authenticated || got.Name != "alice" || len(got.Authorities) != 2 {
		t.Fatalf("got %+v", got)
	}
}
