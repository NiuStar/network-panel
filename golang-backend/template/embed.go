package template

import _ "embed"

//go:embed clash.yaml
var clashTemplate string

//go:embed surge.surgeconfig
var surgeTemplate string

func Load(name string) (string, bool) {
	switch name {
	case "clash.yaml":
		return clashTemplate, clashTemplate != ""
	case "surge.surgeconfig":
		return surgeTemplate, surgeTemplate != ""
	default:
		return "", false
	}
}
