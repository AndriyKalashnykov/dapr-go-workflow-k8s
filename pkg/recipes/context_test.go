package recipes

import "testing"

func newResourceWithProps() Resource {
	return Resource{
		Properties: map[string]any{
			"status": map[string]any{
				"binding": map[string]any{
					"database": "app_db",
					"username": "app_user",
					"port":     5432, // non-string on purpose
				},
			},
		},
	}
}

func TestResourceGetStringValue(t *testing.T) {
	r := newResourceWithProps()

	tests := []struct {
		name      string
		key       string
		wantValue string
		wantOK    bool
	}{
		{"found string", "/status/binding/database", "app_db", true},
		{"found second string", "/status/binding/username", "app_user", true},
		{"missing key", "/status/binding/nope", "", false},
		{"missing parent", "/does/not/exist", "", false},
		{"non-string value does not panic", "/status/binding/port", "", false},
		{"invalid pointer", "status/binding/database", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := r.GetStringValue(tt.key)
			if got != tt.wantValue || ok != tt.wantOK {
				t.Fatalf("GetStringValue(%q) = (%q, %v), want (%q, %v)",
					tt.key, got, ok, tt.wantValue, tt.wantOK)
			}
		})
	}
}

func TestResourceGetStringValueEmptyProperties(t *testing.T) {
	r := Resource{}
	if got, ok := r.GetStringValue("/anything"); ok || got != "" {
		t.Fatalf("expected empty miss on nil Properties, got (%q, %v)", got, ok)
	}
}

func TestContextLogAttrs(t *testing.T) {
	c := Context{
		Resource:    Resource{ResourceInfo: ResourceInfo{ID: "rid"}},
		Application: ResourceInfo{ID: "aid", Name: "aname"},
	}
	attrs := c.LogAttrs()
	if len(attrs) != 3 {
		t.Fatalf("expected 3 attrs, got %d", len(attrs))
	}
	if attrs[0].Key != "resource.id" || attrs[0].Value.String() != "rid" {
		t.Errorf("unexpected resource.id attr: %+v", attrs[0])
	}
	if attrs[1].Key != "application.id" || attrs[1].Value.String() != "aid" {
		t.Errorf("unexpected application.id attr: %+v", attrs[1])
	}
}
