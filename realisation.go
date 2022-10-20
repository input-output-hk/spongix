package main

type Realisation struct {
	ID                    string   `json:"id" db:"id"`
	OutPath               string   `json:"outPath" db:"out_path"`
	Signatures            []string `json:"signatures" db:"signatures"`
	DependentRealisations []string `json:"dependentRealisations" db:"dependent_realisations"`
	Namespace             string   `db:"namespace"`
}
