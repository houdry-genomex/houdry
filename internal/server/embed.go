package server

import (
	"embed"
	"text/template"
)

//go:embed embed/install.sh.tmpl embed/install.ps1.tmpl embed/dashboard.html
var embedded embed.FS

var (
	installSHTmpl  = template.Must(template.New("install.sh").Parse(mustRead("embed/install.sh.tmpl")))
	installPS1Tmpl = template.Must(template.New("install.ps1").Parse(mustRead("embed/install.ps1.tmpl")))
	dashboardHTML  = mustRead("embed/dashboard.html")
)

func mustRead(name string) string {
	b, err := embedded.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return string(b)
}
