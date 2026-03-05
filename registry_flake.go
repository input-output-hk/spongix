package main

import (
	"bytes"
	"text/template"

	"github.com/input-output-hk/spongix/pkg/store"
)

var flakeTemplate = template.Must(template.New("flake.nix").Parse(`{
  outputs = { self, ... }: {
{{- range .Entries }}
    packages.{{ .System }}.default = let
      storePath = builtins.fetchClosure {
        fromPath = "{{ .FromPath }}";
        toPath = "{{ .ToPath }}";
        fromStore = "{{ .FromStore }}";
      };
      drv = derivation {
        name = "{{ $.Name }}-{{ $.Version }}";
        system = "{{ .System }}";
        builder = "${self}/bin/symlink";
        args = [storePath (placeholder "out")];
        pathToLink = storePath;
      };
      attrs = {
        version = "{{ $.Version }}";
{{- if .MainProgram }}
        mainProgram = "{{ .MainProgram }}";
        meta.mainProgram = "{{ .MainProgram }}";
        passthru.shellPath = "/bin/{{ .MainProgram }}";
        shellPath = "/bin/{{ .MainProgram }}";
{{- end }}
      };
    in drv // attrs // { out = drv.out // attrs; };
    packages.{{ .System }}.{{ $.Name }} = self.packages.{{ .System }}.default;
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
