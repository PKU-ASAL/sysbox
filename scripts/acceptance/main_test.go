package main

import (
	"strings"
	"testing"
)

func TestRequireExecutedPassingTests(t *testing.T) {
	for _, tc := range []struct {
		name, events string
		valid        bool
	}{
		{"pass", `{"Action":"run","Package":"p","Test":"TestReal"}
{"Action":"pass","Package":"p","Test":"TestReal"}
{"Action":"pass","Package":"p"}`, true},
		{"empty", `{"Action":"pass","Package":"p"}`, false},
		{"skip", `{"Action":"skip","Package":"p","Test":"TestReal"}
{"Action":"pass","Package":"p"}`, false},
		{"subtest skip", `{"Action":"run","Package":"p","Test":"TestReal"}
{"Action":"skip","Package":"p","Test":"TestReal/required"}
{"Action":"pass","Package":"p","Test":"TestReal"}
{"Action":"pass","Package":"p"}`, false},
		{"unfinished", `{"Action":"run","Package":"p","Test":"TestReal"}`, false},
		{"no run", `{"Action":"pass","Package":"p","Test":"TestReal"}
{"Action":"pass","Package":"p"}`, false},
		{"invalid json", `not-json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verify(strings.NewReader(tc.events), []string{"TestReal"})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestRequireEveryNamedTest(t *testing.T) {
	events := `{"Action":"run","Package":"p","Test":"TestOne"}
{"Action":"pass","Package":"p","Test":"TestOne"}
{"Action":"pass","Package":"p"}`
	if err := verify(strings.NewReader(events), []string{"TestOne", "TestTwo"}); err == nil {
		t.Fatal("missing test accepted")
	}
}
