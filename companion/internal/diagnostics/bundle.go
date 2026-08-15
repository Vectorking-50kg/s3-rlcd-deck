package diagnostics

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"time"
)

type BundleInput struct {
	BuildVersion            string
	BuildCommit             string
	ConfigurationSchemaKeys []string
	DeckRings               []DeckRing
}

type BundleFile struct {
	Path   string `json:"path"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

type BundleManifest struct {
	SchemaVersion uint32       `json:"schema_version"`
	CreatedUTC    string       `json:"created_utc"`
	BuildVersion  string       `json:"build_version"`
	BuildCommit   string       `json:"build_commit"`
	Files         []BundleFile `json:"files"`
}

type schemaKeysDocument struct {
	SchemaVersion uint32   `json:"schema_version"`
	Keys          []string `json:"keys"`
}

type deckRingDocument struct {
	SchemaVersion uint32     `json:"schema_version"`
	Rings         []DeckRing `json:"rings"`
}

func (service *Service) Export(
	ctx context.Context,
	input BundleInput,
) ([]byte, BundleManifest, error) {
	if service == nil || ctx == nil || !safeBuildVersion.MatchString(input.BuildVersion) ||
		!safeCommit.MatchString(input.BuildCommit) || len(input.ConfigurationSchemaKeys) > 512 ||
		len(input.DeckRings) > 32 {
		return nil, BundleManifest{}, ErrUnavailable
	}
	keys := append([]string(nil), input.ConfigurationSchemaKeys...)
	for _, key := range keys {
		if !safeSchemaKey.MatchString(key) {
			return nil, BundleManifest{}, errors.New("invalid diagnostic schema key")
		}
	}
	sort.Strings(keys)
	for index := 1; index < len(keys); index++ {
		if keys[index] == keys[index-1] {
			return nil, BundleManifest{}, errors.New("duplicate diagnostic schema key")
		}
	}
	rings := make([]DeckRing, len(input.DeckRings))
	for index, ring := range input.DeckRings {
		if !validDeckRing(ring) {
			return nil, BundleManifest{}, errors.New("invalid Deck diagnostic ring")
		}
		rings[index] = DeckRing{
			DeviceIDHash: ring.DeviceIDHash, Dropped: ring.Dropped,
			Events: append([]DeckEvent(nil), ring.Events...),
		}
	}
	sort.Slice(rings, func(i, j int) bool { return rings[i].DeviceIDHash < rings[j].DeviceIDHash })
	if err := service.Flush(ctx); err != nil {
		return nil, BundleManifest{}, err
	}
	now := service.now().UTC()
	events, err := service.snapshot(ctx, now.Add(-24*time.Hour), service.maximumExportBytes/2)
	if err != nil {
		return nil, BundleManifest{}, err
	}
	files := map[string][]byte{
		"companion/events.jsonl": events,
	}
	files["deck/ring.json"], err = marshalBundleDocument(deckRingDocument{SchemaVersion: 1, Rings: rings})
	if err != nil {
		return nil, BundleManifest{}, err
	}
	files["configuration/schema-keys.json"], err = marshalBundleDocument(
		schemaKeysDocument{SchemaVersion: 1, Keys: keys},
	)
	if err != nil {
		return nil, BundleManifest{}, err
	}
	paths := make([]string, 0, len(files))
	for filePath := range files {
		if !safeArchivePath(filePath) {
			return nil, BundleManifest{}, errors.New("unsafe diagnostic archive path")
		}
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	manifest := BundleManifest{
		SchemaVersion: 1, CreatedUTC: now.Format(time.RFC3339Nano),
		BuildVersion: input.BuildVersion, BuildCommit: input.BuildCommit,
		Files: make([]BundleFile, 0, len(paths)),
	}
	for _, filePath := range paths {
		digest := sha256.Sum256(files[filePath])
		manifest.Files = append(manifest.Files, BundleFile{
			Path: filePath, Size: len(files[filePath]), SHA256: hex.EncodeToString(digest[:]),
		})
	}
	manifestDocument, err := marshalBundleDocument(manifest)
	if err != nil {
		return nil, BundleManifest{}, err
	}
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	allPaths := append([]string{"manifest.json"}, paths...)
	for _, filePath := range allPaths {
		if err = ctx.Err(); err != nil {
			_ = archive.Close()
			return nil, BundleManifest{}, err
		}
		document := files[filePath]
		if filePath == "manifest.json" {
			document = manifestDocument
		}
		header := &zip.FileHeader{Name: filePath, Method: zip.Deflate}
		header.SetModTime(time.Unix(0, 0).UTC())
		writer, createErr := archive.CreateHeader(header)
		if createErr != nil {
			return nil, BundleManifest{}, createErr
		}
		if _, err = writer.Write(document); err != nil {
			return nil, BundleManifest{}, err
		}
		if output.Len() > service.maximumExportBytes {
			return nil, BundleManifest{}, errors.New("diagnostic bundle exceeds bound")
		}
	}
	if err = archive.Close(); err != nil {
		return nil, BundleManifest{}, err
	}
	if output.Len() > service.maximumExportBytes {
		return nil, BundleManifest{}, errors.New("diagnostic bundle exceeds bound")
	}
	return output.Bytes(), manifest, nil
}

func marshalBundleDocument(value any) ([]byte, error) {
	document, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	document = append(document, '\n')
	return document, nil
}

func safeArchivePath(value string) bool {
	if value == "" || len(value) > 128 || value == "." || path.Clean(value) != value ||
		path.IsAbs(value) || strings.Contains(value, "..") || strings.HasSuffix(value, "/") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, character := range segment {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') &&
				character != '.' && character != '_' && character != '-' {
				return false
			}
		}
	}
	return true
}
