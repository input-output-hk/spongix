package main

import (
	"bytes"
	"text/template"

	"github.com/input-output-hk/spongix/pkg/store"
)

var flakeTemplate = template.Must(template.New("flake.nix").Parse(`{
  outputs = { self, ... }: let
    symlinkPath = { name, system, path, version ? null, ... }@args:
      (derivation {
        inherit name system;
        builder = "${self}/bin/symlink";
        args = [path (placeholder "out")];
        pathToLink = path;
      }) // args // (if version != null then { inherit version; } else {});
  in {
{{- range .Entries }}
    packages.{{ .System }}.default = symlinkPath {
      name = "{{ $.Name }}-{{ $.Version }}";
      version = "{{ $.Version }}";
      system = "{{ .System }}";
      path = builtins.fetchClosure {
        fromPath = "{{ .FromPath }}";
        toPath = "{{ .ToPath }}";
        fromStore = "{{ .FromStore }}";
      };
{{- if .MainProgram }}
      mainProgram = "{{ .MainProgram }}";
      meta.mainProgram = "{{ .MainProgram }}";
      passthru.shellPath = "/bin/{{ .MainProgram }}";
{{- end }}
    };
{{- end }}
  };
}
`))

type flakeData struct {
	Name    string
	Version string
	Entries []*store.RegistryEntry
}

// renderFlakeNix renders a flake.nix for the given package version entries.
func renderFlakeNix(name, version string, entries []*store.RegistryEntry) ([]byte, error) {
	var buf bytes.Buffer
	err := flakeTemplate.Execute(&buf, flakeData{
		Name:    name,
		Version: version,
		Entries: entries,
	})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
