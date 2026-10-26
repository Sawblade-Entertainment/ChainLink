package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// --- Tree model ---

type Property struct {
	Name, Type, Value string
}

type Instance struct {
	Referent string
	Class    string
	Name     string
	Props    []Property
	Children []*Instance
}

type Builder struct {
	cfg     ProjectConfig
	root    string
	byPath  map[string]*Instance // unique dotted path -> instance
	top     []*Instance          // top-level items (services)
	nextRef int
}

func NewBuilder(cfg ProjectConfig, root string) *Builder {
	return &Builder{cfg: cfg, root: root, byPath: map[string]*Instance{}, nextRef: 1}
}

func (b *Builder) newReferent() string {
	r := fmt.Sprintf("RBX%032d", b.nextRef) // any unique string works
	b.nextRef++
	return r
}

// getOrCreate returns the instance at `path`, creating it (under `parent`,
// or at top level if parent == nil) if it doesn't exist yet.
func (b *Builder) getOrCreate(path, class, name string, parent *Instance) *Instance {
	if inst, ok := b.byPath[path]; ok {
		return inst
	}
	inst := &Instance{Referent: b.newReferent(), Class: class, Name: name}
	b.byPath[path] = inst
	if parent == nil {
		b.top = append(b.top, inst)
	} else {
		parent.Children = append(parent.Children, inst)
	}
	return inst
}

func (b *Builder) service(name string) *Instance {
	return b.getOrCreate(name, name, name, nil)
}

// "ReplicatedStorage.shared.util" -> service + nested Folders
func (b *Builder) resolveTarget(target string) *Instance {
	parts := strings.Split(target, ".")
	cur := b.service(parts[0])
	for i := 1; i < len(parts); i++ {
		path := strings.Join(parts[:i+1], ".")
		class := "Folder"
		if path == "StarterPlayer.StarterPlayerScripts" {
			class = "StarterPlayerScripts"
		} else if path == "StarterPlayer.StarterCharacterScripts" {
			class = "StarterCharacterScripts"
		}
		cur = b.getOrCreate(path, class, parts[i], cur)
	}
	return cur
}

// --- Serialization ---

func escText(s string) string {
	r := strings.NewReplacer("&", "&", "<", "<", ">", ">")
	return r.Replace(s)
}

func escAttr(s string) string {
	r := strings.NewReplacer("&", "&", "<", "<", ">", ">", `"`, "\"")
	return r.Replace(s)
}

func writeItem(sb *strings.Builder, inst *Instance, depth int) {
	ind := strings.Repeat("\t", depth)
	fmt.Fprintf(sb, "%s<Item class=\"%s\" referent=\"%s\">\n", ind, escAttr(inst.Class), escAttr(inst.Referent))
	fmt.Fprintf(sb, "%s\t<Properties>\n", ind)
	fmt.Fprintf(sb, "%s\t\t<string name=\"Name\">%s</string>\n", ind, escText(inst.Name))
	for _, p := range inst.Props {
		fmt.Fprintf(sb, "%s\t\t<%s name=\"%s\">%s</%s>\n", ind, p.Type, escAttr(p.Name), escText(p.Value), p.Type)
	}
	fmt.Fprintf(sb, "%s\t</Properties>\n", ind)
	for _, c := range inst.Children {
		writeItem(sb, c, depth+1)
	}
	fmt.Fprintf(sb, "%s</Item>\n", ind)
}

func (b *Builder) build() []byte {
	var sb strings.Builder
	sb.WriteString(`<roblox xmlns:xmime="http://www.w3.org/2005/05/xmlmime" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:noNamespaceSchemaLocation="http://www.roblox.com/roblox.xsd" version="4">` + "\n")
	for _, it := range b.top {
		writeItem(&sb, it, 1)
	}
	sb.WriteString("</roblox>\n")
	return []byte(sb.String())
}

// --- Build command ---

func (b *Builder) syncDir(absDir, target, relPrefix string) {
	targetInst := b.resolveTarget(target)

	filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == absDir {
			return nil
		}
		rel, _ := filepath.Rel(absDir, path)
		c := classify(b.cfg, relPrefix+rel, info.IsDir())
		if !c.Syncable {
			return nil
		}

		cur := targetInst
		for i, seg := range c.InstancePath {
			fullPath := target + "." + strings.Join(c.InstancePath[:i+1], ".")
			class := "Folder"
			if i == len(c.InstancePath)-1 && !info.IsDir() {
				class = c.ClassName
			}
			cur = b.getOrCreate(fullPath, class, seg, cur)
		}

		if !info.IsDir() {
			data, err := os.ReadFile(path) // path is already absolute
			if err != nil {
				return nil
			}
			cur.Props = append(cur.Props, Property{Name: "Source", Type: "ProtectedString", Value: string(data)})
		}
		return nil
	})
}

func handleBuild(outputPath string) error {
	cfg := loadConfig()
	root, err := os.Getwd()
	if err != nil {
		return err
	}

	b := NewBuilder(cfg, root)

	// Pre-create the common services so they appear in a sane order.
	for _, s := range []string{"Workspace", "ReplicatedStorage", "ServerScriptService", "ServerStorage", "StarterPlayer"} {
		b.service(s)
	}
	b.resolveTarget("StarterPlayer.StarterPlayerScripts")

	for _, r := range cfg.Roots {
		absDir := filepath.Join(root, r.Dir)
		if dirExists(absDir) {
			b.syncDir(absDir, r.Target, "")
		}
	}
	for _, p := range cfg.Packages {
		absDir := filepath.Join(packagesBase(), filepath.FromSlash(p.PackageName))
		if dirExists(absDir) {
			b.syncDir(absDir, p.Target, pkgPrefix)
		}
	}

	return os.WriteFile(outputPath, b.build(), 0644)
}
