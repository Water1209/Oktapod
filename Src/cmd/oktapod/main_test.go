package main

import (
	"reflect"
	"testing"
)

func TestParseCommandLineAcceptsGlobalConfigAnywhere(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want []string
	}{
		{name: "before", args: []string{"--config", "portable/config.yaml", "config", "doctor", "--json"}, want: []string{"doctor", "--json"}},
		{name: "after", args: []string{"config", "doctor", "--config=portable/config.yaml", "--json"}, want: []string{"doctor", "--json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			command, args, err := parseCommandLine(test.args)
			if err != nil || command != "config" || !reflect.DeepEqual(args, test.want) {
				t.Fatalf("command=%q args=%v err=%v", command, args, err)
			}
		})
	}
}

func TestParseCommandLineDefaultsToServe(t *testing.T) {
	command, args, err := parseCommandLine([]string{"--config", "portable/config.yaml"})
	if err != nil || command != "serve" || len(args) != 0 {
		t.Fatalf("command=%q args=%v err=%v", command, args, err)
	}
}
