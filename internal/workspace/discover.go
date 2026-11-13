package workspace

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	groveeng "github.com/provasign/grove/pkg/grove"
)

// Candidate is a repository on disk that the manifests say is related to the
// primary one. Discovery only reports; linking is a separate, explicit step.
type Candidate struct {
	ID   string
	Path string // absolute
	// Explicit is true when the primary repository's own build already
	// points at this directory (go.work use, replace => ../x, file:../x).
	// Otherwise the match is a declared package name found in a sibling.
	Explicit bool
	// Relation is "depends on" (the primary requires it) or "used by" (it
	// requires the primary).
	Relation string
	Package  string // the package that links the two
	Version  string // the version the requiring side pins, as written
	Evidence string
	Linked   bool // already in the workspace
}

// Discover finds repositories related to primary, most certain first:
// directories its manifests point at, then sibling directories whose
// manifests declare a package it requires or require a package it declares.
// It reads manifest files only. Nothing is indexed, linked, or written.
func Discover(primary string, ws *Workspace) []Candidate {
	primary = resolve(primary)
	own := groveeng.ScanManifests(primary)
	linked := map[string]bool{}
	for _, r := range ws.Linked() {
		linked[r.Path] = true
	}
	seen := map[string]bool{primary: true}
	var out []Candidate
	add := func(c Candidate) {
		c.Path = resolve(c.Path)
		if seen[c.Path] {
			return
		}
		if fi, err := os.Stat(c.Path); err != nil || !fi.IsDir() {
			return
		}
		seen[c.Path] = true
		c.ID = filepath.Base(c.Path)
		c.Linked = linked[c.Path]
		out = append(out, c)
	}
	declares := func(pkgs []groveeng.ManifestPackage, eco, name string) bool {
		for _, p := range pkgs {
			if p.Ecosystem == eco && p.Name == name {
				return true
			}
		}
		return false
	}
	// usedBy finds the line in another repository's manifests that requires
	// a package the primary declares.
	usedBy := func(theirs groveeng.Manifests) (groveeng.ManifestRequire, bool) {
		for _, req := range theirs.Requires {
			if !req.CoMember && declares(own.Packages, req.Ecosystem, req.Name) {
				return req, true
			}
		}
		return groveeng.ManifestRequire{}, false
	}
	satisfied := map[string]bool{} // requires the build already resolves to a directory
	for _, req := range own.Requires {
		if req.LocalPath == "" || isInside(primary, resolve(req.LocalPath)) {
			continue
		}
		if !req.CoMember {
			satisfied[req.Ecosystem+":"+req.Name] = true
			add(Candidate{Path: req.LocalPath, Explicit: true, Relation: "depends on",
				Package: req.Name, Version: req.Version, Evidence: req.Evidence})
			continue
		}
		// Built in the same go.work, but not required here. It is related
		// only if it requires this repository.
		name := filepath.Base(req.LocalPath)
		if back, ok := usedBy(groveeng.ScanManifests(req.LocalPath)); ok {
			add(Candidate{Path: req.LocalPath, Explicit: true, Relation: "used by",
				Package: back.Name, Version: back.Version, Evidence: requiredBy(name, back) + "; " + req.Evidence})
		}
	}

	parent := filepath.Dir(primary)
	entries, _ := os.ReadDir(parent)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && e.Name() != "" && e.Name()[0] != '.' {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	dirs := make([]string, 0, len(names))
	for _, name := range names {
		dirs = append(dirs, filepath.Join(parent, name))
	}
	// Then repositories Prism was set up in elsewhere on this machine.
	for _, e := range Known() {
		dirs = append(dirs, e.Path)
	}
	for _, dir := range dirs {
		name := filepath.Base(dir)
		if r := resolve(dir); r == primary || isInside(primary, r) || isInside(r, primary) {
			continue
		}
		theirs := groveeng.ScanManifests(dir)
		if len(theirs.Packages) == 0 && len(theirs.Requires) == 0 {
			continue
		}
		for _, req := range own.Requires {
			if req.CoMember || satisfied[req.Ecosystem+":"+req.Name] {
				continue // the build already says which checkout this is
			}
			if declares(theirs.Packages, req.Ecosystem, req.Name) {
				add(Candidate{Path: dir, Relation: "depends on", Package: req.Name, Version: req.Version,
					Evidence: req.Evidence + "; declared by " + name + "/" + manifestOf(theirs.Packages, req.Name)})
				break
			}
		}
		if req, ok := usedBy(theirs); ok {
			add(Candidate{Path: dir, Relation: "used by", Package: req.Name, Version: req.Version,
				Evidence: requiredBy(name, req)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Explicit && !out[j].Explicit })
	return out
}

func manifestOf(pkgs []groveeng.ManifestPackage, name string) string {
	for _, p := range pkgs {
		if p.Name == name {
			return p.Manifest
		}
	}
	return ""
}

// isInside reports whether child is parent or below it.
func isInside(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// requiredBy words the evidence that repo requires a package of the primary.
func requiredBy(repo string, req groveeng.ManifestRequire) string {
	manifest := req.Manifest
	if strings.HasPrefix(manifest, "..") { // the link came from a shared go.work
		manifest = "go.mod"
	}
	return strings.TrimSpace(repo + "/" + manifest + " requires " + req.Name + " " + req.Version)
}

// Unlinked is a dependency this repository declares that no checkout on
// this machine was found for.
type Unlinked struct {
	Ecosystem string
	Name      string
	Version   string
	Evidence  string
	// URL is where the source can be fetched from, when the package name
	// says so (a Go module path). Empty otherwise.
	URL string
}

// Missing lists the requirements of primary that neither a discovered
// candidate nor an already linked repository satisfies: what a cache clone
// or the link server would have to supply. Most entries are ordinary
// third-party packages; the list is for picking out the ones that matter.
func Missing(primary string, ws *Workspace, found []Candidate) []Unlinked {
	primary = resolve(primary)
	have := map[string]bool{}
	note := func(dir string) {
		for _, p := range groveeng.ScanManifests(dir).Packages {
			have[p.Ecosystem+":"+p.Name] = true
		}
	}
	for _, c := range found {
		note(c.Path)
	}
	for _, r := range ws.Linked() {
		note(r.Path)
	}
	var out []Unlinked
	seen := map[string]bool{}
	for _, req := range groveeng.ScanManifests(primary).Requires {
		key := req.Ecosystem + ":" + req.Name
		if req.CoMember || have[key] || seen[key] {
			continue
		}
		if req.LocalPath != "" {
			if fi, err := os.Stat(req.LocalPath); err == nil && fi.IsDir() {
				continue
			}
		}
		seen[key] = true
		u := Unlinked{Ecosystem: req.Ecosystem, Name: req.Name, Version: req.Version, Evidence: req.Evidence}
		if req.Ecosystem == "go" {
			parts := strings.Split(req.Name, "/")
			if len(parts) >= 3 && strings.Contains(parts[0], ".") {
				u.URL = "https://" + strings.Join(parts[:3], "/")
			}
		}
		out = append(out, u)
	}
	return out
}
