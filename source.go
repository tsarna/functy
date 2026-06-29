package functy

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
)

// Extension is the file extension for functy source files.
const Extension = ".cty"

// ParseSources collects functy sources from a heterogeneous set of inputs,
// returning the raw bytes of each (it does not parse them — call Parser.Parse).
// Each argument may be:
//
//   - a string path to a .cty file, or to a directory (walked recursively,
//     skipping dot-directories, collecting every .cty file);
//   - a []string of such paths;
//   - an embed.FS (walked recursively for .cty files);
//   - a Source, used as-is;
//   - a []byte, treated as the bytes of one anonymous source.
//
// This mirrors how a host discovers .vcl/.vinit files, but yields raw bytes
// because functy has its own front-end.
func ParseSources(inputs ...any) ([]Source, hcl.Diagnostics) {
	var sources []Source
	var diags hcl.Diagnostics

	for _, in := range inputs {
		switch v := in.(type) {
		case Source:
			sources = append(sources, v)
		case []byte:
			sources = append(sources, Source{Filename: "<bytes>", Bytes: v})
		case string:
			fileSources, fdiags := sourcesFromPath(v)
			diags = diags.Extend(fdiags)
			sources = append(sources, fileSources...)
		case []string:
			for _, p := range v {
				fileSources, fdiags := sourcesFromPath(p)
				diags = diags.Extend(fdiags)
				sources = append(sources, fileSources...)
			}
		case embed.FS:
			fsSources, fdiags := sourcesFromFS(v)
			diags = diags.Extend(fdiags)
			sources = append(sources, fsSources...)
		default:
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Unsupported functy source",
				Detail:   fmt.Sprintf("Cannot read functy sources from a value of type %T.", in),
			})
		}
	}
	return sources, diags
}

func sourcesFromPath(path string) ([]Source, hcl.Diagnostics) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Cannot read functy source",
			Detail:   err.Error(),
		}}
	}
	if !info.IsDir() {
		return readFileSource(path)
	}

	var sources []Source
	var diags hcl.Diagnostics
	walkErr := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if p != path && strings.HasPrefix(info.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, Extension) {
			fileSources, fdiags := readFileSource(p)
			diags = diags.Extend(fdiags)
			sources = append(sources, fileSources...)
		}
		return nil
	})
	if walkErr != nil {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Cannot walk functy source directory",
			Detail:   walkErr.Error(),
		})
	}
	return sources, diags
}

func readFileSource(path string) ([]Source, hcl.Diagnostics) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Cannot read functy source",
			Detail:   err.Error(),
		}}
	}
	return []Source{{Filename: path, Bytes: b}}, nil
}

func sourcesFromFS(fsys embed.FS) ([]Source, hcl.Diagnostics) {
	var sources []Source
	var diags hcl.Diagnostics
	walkErr := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, Extension) {
			return nil
		}
		b, readErr := fs.ReadFile(fsys, p)
		if readErr != nil {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Cannot read functy source",
				Detail:   readErr.Error(),
			})
			return nil
		}
		sources = append(sources, Source{Filename: p, Bytes: b})
		return nil
	})
	if walkErr != nil {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Cannot walk embedded functy sources",
			Detail:   walkErr.Error(),
		})
	}
	return sources, diags
}
