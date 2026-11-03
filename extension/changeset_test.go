package assist

import "testing"

func TestOverwritesElement(t *testing.T) {
	text := []byte(`{"scopes":[{"id":"assist","routes":[{"name":"threads","handler":"assist"}],"secret_refs":{"a":"env:A"},"tags":["x"]}]}`)
	for pointer, want := range map[string]bool{
		"/scopes/assist/routes/0":       true,
		"/scopes/0/routes/threads":      true,
		"/scopes/assist/tags/0":         true,
		"/scopes/assist/routes/-":       false,
		"/scopes/assist/routes/1":       false,
		"/scopes/assist/routes/other":   false,
		"/scopes/assist/secret_refs/a":  false,
		"/scopes/assist/routes":         false,
		"/scopes/assist/routes/0/name":  false,
		"/scopes/missing/routes/0":      false,
		"/scopes/assist/routes/0/x/y/z": false,
	} {
		if got := overwritesElement(text, pointer); got != want {
			t.Errorf("%s: %v, want %v", pointer, got, want)
		}
	}
}

func TestRoutesThreads(t *testing.T) {
	with := []byte(`{"scopes":[{"id":"assist","routes":[{"name":"threads","handler":"assist"}]},{"id":"eng","routes":[]}]}`)
	if !routesThreads(with, "assist") || routesThreads(with, "eng") {
		t.Fatal("routesThreads")
	}
}
