package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
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
	RawXML   string // pre-serialized <Item>…</Item> subtree (from .rbxmx prefabs)
	FilePath string // Local path to the source file
}

type Builder struct {
	cfg           ProjectConfig
	root          string
	byPath        map[string]*Instance
	top           []*Instance
	nextRef       int
	sharedStrings map[string]string // md5 -> value
	sourcemap     map[string]string // roblox_path -> local_path
}

var InjectGlobals = `local __clOldRequireGlob = require
function require(m)
	if type(m) == "string" and m:sub(1,1) ~= "." and m:sub(1,1) ~= "@" then
		if string.lower(m) == m then
			m = game.ReplicatedStorage:WaitForChild("packages"):WaitForChild(m)
		else
			return game:GetService(m)
		end
	end
	return __clOldRequireGlob(m)
end`

func NewBuilder(cfg ProjectConfig, root string) *Builder {
	return &Builder{
		cfg:           cfg,
		root:          root,
		byPath:        map[string]*Instance{},
		sharedStrings: map[string]string{},
		sourcemap:     map[string]string{},
		nextRef:       1,
	}
}

func (b *Builder) newReferent() string {
	r := fmt.Sprintf("RBX%032d", b.nextRef)
	b.nextRef++
	return r
}

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

func (b *Builder) resolveTarget(target string) *Instance {
	parts := strings.Split(target, ".")
	cur := b.service(parts[0])
	for i := 1; i < len(parts); i++ {
		path := strings.Join(parts[:i+1], ".")
		class := "Folder"
		switch path {
		case "StarterPlayer.StarterPlayerScripts":
			class = "StarterPlayerScripts"
		case "StarterPlayer.StarterCharacterScripts":
			class = "StarterCharacterScripts"
		}
		cur = b.getOrCreate(path, class, parts[i], cur)
	}
	return cur
}

func (b *Builder) place(target string, instancePath []string, leafClass string) *Instance {
	cur := b.resolveTarget(target)
	for i, seg := range instancePath {
		fullPath := target + "." + strings.Join(instancePath[:i+1], ".")
		class := "Folder"
		if i == len(instancePath)-1 && leafClass != "" {
			class = leafClass
		}
		cur = b.getOrCreate(fullPath, class, seg, cur)
	}
	return cur
}

func setSource(inst *Instance, src string) {
	if !isScriptClass(inst.Class) {
		inst.Class = "ModuleScript"
	}
	for i := range inst.Props {
		if inst.Props[i].Name == "Source" {
			inst.Props[i].Value = src
			return
		}
	}
	inst.Props = append(inst.Props, Property{Name: "Source", Type: "ProtectedString", Value: src})
}

// --- Serialization ---

// sanitize drops characters that are illegal in XML 1.0 and repairs invalid UTF-8.
func sanitize(s string) string {
	if utf8.ValidString(s) && !strings.ContainsFunc(s, isIllegalXML) {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s { // ranging decodes invalid bytes as U+FFFD
		if r == utf8.RuneError || isIllegalXML(r) {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

func isIllegalXML(r rune) bool {
	switch {
	case r == 0x9 || r == 0xA || r == 0xD:
		return false
	case r >= 0x20 && r <= 0xD7FF, r >= 0xE000 && r <= 0xFFFD, r >= 0x10000 && r <= 0x10FFFF:
		return false
	}
	return true
}

func escText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(sanitize(s)))
	return b.String()
}

func escAttr(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(sanitize(s)))
	return strings.ReplaceAll(b.String(), "\"", "&quot;")
}

// cdata wraps s in a CDATA section, splitting on any "]]>" so it can't terminate early.
func cdata(s string) string {
	return "<![CDATA[" + strings.ReplaceAll(sanitize(s), "]]>", "]]]]><![CDATA[>") + "]]>"
}

func writeItem(sb *strings.Builder, inst *Instance, depth int) {
	ind := strings.Repeat("\t", depth)
	if inst.RawXML != "" {
		sb.WriteString(inst.RawXML)
		sb.WriteString("\n")
		return
	}
	fmt.Fprintf(sb, "%s<Item class=\"%s\" referent=\"%s\">\n", ind, escAttr(inst.Class), escAttr(inst.Referent))
	fmt.Fprintf(sb, "%s\t<Properties>\n", ind)
	fmt.Fprintf(sb, "%s\t\t<string name=\"Name\">%s</string>\n", ind, escText(inst.Name))
	for _, p := range inst.Props {
		if p.Type == "ProtectedString" {
			fmt.Fprintf(sb, "%s\t\t<ProtectedString name=\"%s\">%s</ProtectedString>\n", ind, escAttr(p.Name), cdata(p.Value))
		} else {
			fmt.Fprintf(sb, "%s\t\t<%s name=\"%s\">%s</%s>\n", ind, p.Type, escAttr(p.Name), escText(p.Value), p.Type)
		}
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

	if len(b.sharedStrings) > 0 {
		sb.WriteString("\t<SharedStrings>\n")
		for md5, val := range b.sharedStrings {
			fmt.Fprintf(&sb, "\t\t<SharedString md5=\"%s\">%s</SharedString>\n", escAttr(md5), escText(val))
		}
		sb.WriteString("\t</SharedStrings>\n")
	}

	sb.WriteString("</roblox>\n")
	return []byte(sb.String())
}

// --- Prefab (.rbxmx) support ---

// extractItems pulls the raw <Item>…</Item> subtrees out of an .rbxmx model file.
func extractItems(data []byte) ([]string, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	var items []string
	var depth int
	var start int64
	for {
		tok, err := dec.Token()
		if err != nil {
			if err != io.EOF {
				return nil, err
			}
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "Item" {
				if depth == 0 {
					start = dec.InputOffset() - int64(len(rawStartTag(data, dec.InputOffset())))
				}
				depth++
			}
		case xml.EndElement:
			if t.Name.Local == "Item" {
				depth--
				if depth == 0 {
					items = append(items, string(data[start:dec.InputOffset()]))
				}
			}
		}
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no <Item> elements found")
	}
	return items, nil
}

// extractSharedStrings pulls the <SharedStrings> section from an .rbxmx file.
func extractSharedStrings(data []byte, shared map[string]string) error {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	var inSharedStrings bool
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "SharedStrings" {
				inSharedStrings = true
			} else if inSharedStrings && t.Name.Local == "SharedString" {
				var md5 string
				for _, attr := range t.Attr {
					if attr.Name.Local == "md5" {
						md5 = attr.Value
						break
					}
				}
				var val string
				if err := dec.DecodeElement(&val, &t); err != nil {
					return err
				}
				if md5 != "" {
					shared[md5] = val
				}
			}
		case xml.EndElement:
			if t.Name.Local == "SharedStrings" {
				inSharedStrings = false
			}
		}
	}
	return nil
}

// rawStartTag returns the bytes of the start tag that ends at offset `end`.
func rawStartTag(data []byte, end int64) []byte {
	i := int(end) - 1
	for i >= 0 && data[i] != '<' {
		i--
	}
	if i < 0 {
		return nil
	}
	return data[i:int(end)]
}

// --- Build command ---

func (b *Builder) syncBootstrap() {
	if b.cfg.DisableBootstrap {
		return
	}
	for _, ev := range bootstrapEvents(b.cfg) {
		if ev.Op != "CREATE" {
			continue
		}
		inst := b.place(ev.Target, ev.InstancePath, ev.ClassName)
		setSource(inst, ev.Source)
	}
}

func isScriptClass(c string) bool {
	switch c {
	case "Script", "LocalScript", "ModuleScript":
		return true
	}
	return false
}

func parseScript(cfg ProjectConfig, source string, fullRel string) string {
	if !cfg.DisableBootstrap && (strings.Contains(fullRel, "Controller.lua") || strings.Contains(fullRel, "Service.lua")) {
		source = "local mod = {}\n" +
			strings.ReplaceAll(source, "\n\n", "\n") +
			"\nreturn mod"
	}

	return addLuauHeader(InjectGlobals + "\n" + source)
}

func (b *Builder) syncDir(absDir, target, relPrefix string) {
	b.resolveTarget(target)

	filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == absDir {
			return nil
		}
		rel, _ := filepath.Rel(absDir, path)

		var rootDir string
		for _, r := range b.cfg.Roots {
			if filepath.Join(b.root, r.Dir) == absDir {
				rootDir = r.Dir
				break
			}
		}

		fullRel := relPrefix
		isPackage := strings.Contains(absDir, filepath.Join(packagesBase(), ""))
		if rootDir != "" {
			fullRel = rootDir + "/"
		} else if isPackage {
			// If we are in a package directory, use the package prefix
			// We need the package name relative to the packages base
			pkgRel, _ := filepath.Rel(packagesBase(), absDir)
			fullRel = pkgPrefix + filepath.ToSlash(pkgRel) + "/"
		}
		if fullRel != "" && !strings.HasSuffix(fullRel, "/") {
			fullRel += "/"
		}
		fullRel += filepath.ToSlash(rel)

		c := classify(b.cfg, fullRel, info.IsDir())
		if !c.Syncable {
			// Special case: if we are syncing a package and the file is not syncable
			// according to the global config (which expects pkgPrefix), we might need to
			// bypass classify or use a different one.
			// However, classify uses mapPath.
			return nil
		}

		if info.IsDir() {
			b.place(c.Target, c.InstancePath, "")
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		source := string(data)
		lower := strings.ToLower(fullRel)
		if (strings.HasSuffix(lower, ".luau") || strings.HasSuffix(lower, ".lua")) && !isPackage {
			source = parseScript(b.cfg, source, fullRel)
		} else if strings.HasSuffix(lower, ".json") {
			var parsed any
			if err := json.Unmarshal([]byte(source), &parsed); err != nil {
				source = "return " + source
			} else {
				source = "return " + jsonToLuau(parsed)
			}
		}

		switch {
		case isScriptClass(c.ClassName):
			inst := b.place(c.Target, c.InstancePath, c.ClassName)
			setSource(inst, source)
			b.sourcemap[c.Target] = path
			inst.FilePath = path

		case strings.EqualFold(filepath.Ext(path), ".rbxmx"):
			if err := extractSharedStrings(data, b.sharedStrings); err != nil {
				fmt.Fprintf(os.Stderr, "build: error extracting shared strings from %s: %v\n", rel, err)
			}
			items, err := extractItems(data)
			if err != nil {
				fmt.Fprintf(os.Stderr, "build: skipping %s: %v\n", rel, err)
				return nil
			}
			// Preserve the mapped filename as a Model, with the imported model
			// tree nested beneath that node.
			model := b.place(c.Target, c.InstancePath, "Model")
			model.Class = "Model" // upgrade if a same-name folder was visited first
			for _, raw := range items {
				model.Children = append(model.Children, &Instance{RawXML: raw})
			}

		case strings.EqualFold(filepath.Ext(path), ".rbxm"):
			fmt.Fprintf(os.Stderr, "build: skipping %s: binary .rbxm cannot be embedded in XML .rbxlx; convert it to .rbxmx for build output\n", rel)

		default:
			fmt.Fprintf(os.Stderr, "build: skipping %s (unsupported class %q)\n", rel, c.ClassName)
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

	for _, s := range []string{"Workspace", "ReplicatedStorage", "ServerScriptService", "ServerStorage", "StarterPlayer"} {
		b.service(s)
	}
	b.resolveTarget("StarterPlayer.StarterPlayerScripts")

	b.syncBootstrap()

	for _, r := range cfg.Roots {
		absDir := filepath.Join(root, r.Dir)
		if dirExists(absDir) {
			b.syncDir(absDir, r.Target, "")
		}
	}
	for _, p := range cfg.Packages {
		absDir := filepath.Join(packagesBase(), filepath.FromSlash(p.PackageName))
		if !dirExists(absDir) {
			continue
		}

		// If there is a "src" directory, use it as the root for syncing.
		srcDir := filepath.Join(absDir, "src")
		if dirExists(srcDir) {
			// Sync from src/, but treat it as the package root.
			// We pass an empty relPrefix because the files inside src/
			// should be mapped directly to the package target.
			b.syncDir(srcDir, p.Target, "")
		} else {
			b.syncDir(absDir, p.Target, "")
		}
	}

	return os.WriteFile(outputPath, b.build(), 0644)
}
