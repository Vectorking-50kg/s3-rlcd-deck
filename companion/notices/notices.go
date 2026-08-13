package notices

import (
	"bytes"
	"embed"
	"io/fs"
	"sort"
)

//go:embed licenses/*
var licenseFiles embed.FS

func ThirdParty() string {
	entries, _ := fs.ReadDir(licenseFiles, "licenses")
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
	var contents bytes.Buffer
	contents.WriteString("S3 RLCD Deck Companion third-party notices\n\n")
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		license, err := licenseFiles.ReadFile("licenses/" + entry.Name())
		if err != nil {
			continue
		}
		contents.WriteString("===== " + entry.Name() + " =====\n")
		contents.Write(license)
		if len(license) == 0 || license[len(license)-1] != '\n' {
			contents.WriteByte('\n')
		}
		contents.WriteByte('\n')
	}
	return contents.String()
}
