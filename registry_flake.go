package main

import (
	"bytes"
	"text/template"

	"github.com/input-output-hk/spongix/pkg/store"
)

var flakeTemplate = template.Must(template.New("flake.nix").Parse(`{
  outputs = { ... }: let
    symlinkBin = builtins.fetchClosure {
      fromPath = "{{ .SymlinkBin.FromPath }}";
      toPath = "{{ .SymlinkBin.ToPath }}";
      fromStore = "{{ .SymlinkBin.FromStore }}";
    };
    symlinkPath = { name, system, path, ... }@args:
      (derivation {
        inherit name system;
        builder = "''${symlinkBin}/bin/symlink";
        args = [path (placeholder "out")];
        pathToLink = path;
      }) // args;
  in {
{{- range .Entries }}
    packages.{{ .System }}.default = symlinkPath {
      name = "{{ $.Name }}-{{ $.Version }}";
      system = "{{ .System }}";
      path = builtins.fetchClosure {
        fromPath = "{{ .FromPath }}";
        toPath = "{{ .ToPath }}";
        fromStore = "{{ .FromStore }}";
      };
{{- if .MainProgram }}
      mainProgram = "{{ .MainProgram }}";
      meta.mainProgram = "{{ .MainProgram }}";
{{- end }}
    };
{{- end }}
  };
}
`))

type flakeData struct {
	Name       string
	Version    string
	Entries    []*store.RegistryEntry
	SymlinkBin SymlinkBin
}

// renderFlakeNix renders a flake.nix for the given package version entries.
func renderFlakeNix(name, version string, entries []*store.RegistryEntry, sb SymlinkBin) ([]byte, error) {
	var buf bytes.Buffer
	err := flakeTemplate.Execute(&buf, flakeData{
		Name:       name,
		Version:    version,
		Entries:    entries,
		SymlinkBin: sb,
	})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
