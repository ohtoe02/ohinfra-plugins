package main

import (
	"reflect"
	"testing"
)

func TestBuildDefinitionPublishesServerSetupIdentity(t *testing.T) {
	definition := buildDefinition()
	if definition.Manifest.Name != "server-setup-base" {
		t.Fatalf("name = %q", definition.Manifest.Name)
	}
	if !reflect.DeepEqual(definition.Manifest.Commands[1].Path, []string{"setup", "apply"}) {
		t.Fatalf("commands = %#v", definition.Manifest.Commands)
	}
}
