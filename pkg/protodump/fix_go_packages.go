package protodump

import (
	"path"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

func formatGoPackageForFile(fileName string, modulePrefix string) string {
	dir := path.Dir(fileName)
	dir = strings.TrimPrefix(dir, "google3/")

	dirParts := strings.Split(dir, "/")
	for i, part := range dirParts {
		if part == "internal" {
			dirParts[i] = "v1internal"
		}
	}
	dir = strings.Join(dirParts, "/")

	base := path.Base(dir)
	if base == "" || base == "." || base == "/" {
		base = "pb"
	}

	importPath := dir
	if modulePrefix != "" && !strings.HasPrefix(importPath, modulePrefix) {
		importPath = path.Join(modulePrefix, importPath)
	}

	return importPath + ";" + base
}

// ResolveGoPackageCycles detects import cycles between Go packages in the given proto definitions
// and merges cyclic Go packages into a single unified go_package option per strongly connected component.
func ResolveGoPackageCycles(defs []*ProtoDefinition, modulePrefix string) ([]*ProtoDefinition, error) {
	if len(defs) == 0 {
		return defs, nil
	}

	fileToDef := make(map[string]*ProtoDefinition)
	fileToGoPkg := make(map[string]string)

	for _, def := range defs {
		if def.pb != nil && def.pb.GetName() != "" {
			fileName := def.pb.GetName()
			fileToDef[fileName] = def

			goPkg := formatGoPackageForFile(fileName, modulePrefix)
			if def.pb.Options == nil {
				def.pb.Options = &descriptorpb.FileOptions{}
			}
			def.pb.Options.GoPackage = proto.String(goPkg)
			fileToGoPkg[fileName] = goPkg
		}
	}

	nodesSet := make(map[string]bool)
	adj := make(map[string]map[string]bool)

	for _, def := range defs {
		srcPkg := fileToGoPkg[def.pb.GetName()]
		if srcPkg == "" {
			continue
		}
		nodesSet[srcPkg] = true
		if adj[srcPkg] == nil {
			adj[srcPkg] = make(map[string]bool)
		}

		for _, depName := range def.pb.Dependency {
			dstPkg := fileToGoPkg[depName]
			if dstPkg != "" && dstPkg != srcPkg {
				adj[srcPkg][dstPkg] = true
				nodesSet[dstPkg] = true
			}
		}
	}

	var nodes []string
	for n := range nodesSet {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	var sccs [][]string
	index := 0
	indices := make(map[string]int)
	lowlink := make(map[string]int)
	onStack := make(map[string]bool)
	var stack []string

	var strongConnect func(v string)
	strongConnect = func(v string) {
		indices[v] = index
		lowlink[v] = index
		index++
		stack = append(stack, v)
		onStack[v] = true

		var neighbors []string
		for w := range adj[v] {
			neighbors = append(neighbors, w)
		}
		sort.Strings(neighbors)

		for _, w := range neighbors {
			if _, visited := indices[w]; !visited {
				strongConnect(w)
				if lowlink[w] < lowlink[v] {
					lowlink[v] = lowlink[w]
				}
			} else if onStack[w] {
				if indices[w] < lowlink[v] {
					lowlink[v] = indices[w]
				}
			}
		}

		if lowlink[v] == indices[v] {
			var scc []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				scc = append(scc, w)
				if w == v {
					break
				}
			}
			sccs = append(sccs, scc)
		}
	}

	for _, n := range nodes {
		if _, visited := indices[n]; !visited {
			strongConnect(n)
		}
	}

	pkgRewrite := make(map[string]string)

	for _, scc := range sccs {
		hasCycle := len(scc) > 1
		if len(scc) == 1 {
			if adj[scc[0]][scc[0]] {
				hasCycle = true
			}
		}

		if !hasCycle {
			continue
		}

		mergedPkg := computeMergedGoPackage(scc, modulePrefix)
		for _, pkg := range scc {
			pkgRewrite[pkg] = mergedPkg
		}
	}

	oldToNewName := make(map[string]string)

	for _, def := range defs {
		oldPkg := fileToGoPkg[def.pb.GetName()]
		if newPkg, ok := pkgRewrite[oldPkg]; ok {
			if def.pb.Options == nil {
				def.pb.Options = &descriptorpb.FileOptions{}
			}
			def.pb.Options.GoPackage = proto.String(newPkg)

			newImportPath := newPkg
			if idx := strings.Index(newPkg, ";"); idx != -1 {
				newImportPath = newPkg[:idx]
			}
			relDir := strings.TrimPrefix(newImportPath, modulePrefix)
			relDir = strings.TrimPrefix(relDir, "/")

			oldName := def.pb.GetName()
			newName := path.Join(relDir, path.Base(oldName))
			if oldName != newName {
				oldToNewName[oldName] = newName
				def.pb.Name = proto.String(newName)
			}
		}
	}

	for _, def := range defs {
		for i, dep := range def.pb.Dependency {
			depClean := strings.TrimPrefix(dep, "google3/")
			if strings.Contains(depClean, "internal/") {
				depClean = replaceInternalPath(depClean)
			}
			if newName, ok := oldToNewName[depClean]; ok {
				def.pb.Dependency[i] = newName
			} else {
				def.pb.Dependency[i] = depClean
			}
		}
	}

	DeduplicatePackageSymbols(defs)
	FixTypeReferences(defs)

	var files protoregistry.Files
	for _, def := range defs {
		fd, _ := (protodesc.FileOptions{AllowUnresolvable: true}).New(def.pb, &protoregistry.Files{})
		if fd != nil {
			files.RegisterFile(fd)
		}
	}

	var relinkedFiles protoregistry.Files
	var finalDefs []*ProtoDefinition
	for _, def := range defs {
		fd, err := (protodesc.FileOptions{AllowUnresolvable: true}).New(def.pb, &files)
		if err == nil {
			relinkedFiles.RegisterFile(fd)
			def.descriptor = fd
		} else if placeholderFd, pErr := (protodesc.FileOptions{AllowUnresolvable: true}).New(def.pb, &protoregistry.Files{}); pErr == nil {
			def.descriptor = placeholderFd
		}
		finalDefs = append(finalDefs, def)
	}

	return finalDefs, nil
}

func FixTypeReferences(defs []*ProtoDefinition) {
	validFQNs := make(map[string]bool)
	shortNameToFQN := make(map[string]string)

	var registerEnum func(e *descriptorpb.EnumDescriptorProto, prefix string, isTopLevel bool)
	registerEnum = func(e *descriptorpb.EnumDescriptorProto, prefix string, isTopLevel bool) {
		pkgPrefix := prefix
		if pkgPrefix != "" {
			pkgPrefix = "." + pkgPrefix
		}
		fqn := pkgPrefix + "." + e.GetName()
		validFQNs[fqn] = true
		if isTopLevel {
			shortNameToFQN[e.GetName()] = fqn
		}
	}

	var registerMsg func(m *descriptorpb.DescriptorProto, prefix string, isTopLevel bool)
	registerMsg = func(m *descriptorpb.DescriptorProto, prefix string, isTopLevel bool) {
		pkgPrefix := prefix
		if pkgPrefix != "" {
			pkgPrefix = "." + pkgPrefix
		}
		fqn := pkgPrefix + "." + m.GetName()
		validFQNs[fqn] = true
		if isTopLevel {
			shortNameToFQN[m.GetName()] = fqn
		}

		for _, nested := range m.NestedType {
			registerMsg(nested, strings.TrimPrefix(fqn, "."), false)
		}
		for _, enum := range m.EnumType {
			registerEnum(enum, strings.TrimPrefix(fqn, "."), false)
		}
	}

	for _, def := range defs {
		if def.pb == nil {
			continue
		}
		pkg := def.pb.GetPackage()
		for _, m := range def.pb.MessageType {
			registerMsg(m, pkg, true)
		}
		for _, e := range def.pb.EnumType {
			registerEnum(e, pkg, true)
		}
	}

	var fixMsgFields func(m *descriptorpb.DescriptorProto)
	fixMsgFields = func(m *descriptorpb.DescriptorProto) {
		for _, f := range m.Field {
			if f.TypeName != nil {
				tn := *f.TypeName
				if strings.HasPrefix(tn, ".google.protobuf.") || strings.HasPrefix(tn, ".google.rpc.") {
					continue
				}
				if !validFQNs[tn] {
					shortName := path.Base(strings.ReplaceAll(tn, ".", "/"))
					if canonical, ok := shortNameToFQN[shortName]; ok {
						f.TypeName = proto.String(canonical)
					}
				}
			}
		}
		for _, nested := range m.NestedType {
			fixMsgFields(nested)
		}
	}

	for _, def := range defs {
		if def.pb == nil {
			continue
		}
		for _, m := range def.pb.MessageType {
			fixMsgFields(m)
		}
	}
}

func DeduplicatePackageSymbols(defs []*ProtoDefinition) {
	sort.Slice(defs, func(i, j int) bool {
		nameI := path.Base(defs[i].pb.GetName())
		nameJ := path.Base(defs[j].pb.GetName())
		goPkgI := defs[i].pb.GetOptions().GetGoPackage()
		goPkgJ := defs[j].pb.GetOptions().GetGoPackage()

		basePkgI := path.Base(goPkgI)
		if idx := strings.Index(basePkgI, ";"); idx != -1 {
			basePkgI = basePkgI[idx+1:]
		}
		basePkgJ := path.Base(goPkgJ)
		if idx := strings.Index(basePkgJ, ";"); idx != -1 {
			basePkgJ = basePkgJ[idx+1:]
		}

		matchesI := strings.HasPrefix(nameI, basePkgI) || strings.HasPrefix(basePkgI, strings.TrimSuffix(nameI, ".proto"))
		matchesJ := strings.HasPrefix(nameJ, basePkgJ) || strings.HasPrefix(basePkgJ, strings.TrimSuffix(nameJ, ".proto"))

		if matchesI != matchesJ {
			return matchesI
		}

		return len(defs[i].pb.MessageType) > len(defs[j].pb.MessageType)
	})

	seenMsgs := make(map[string]string)
	seenEnums := make(map[string]string)

	for _, def := range defs {
		if def.pb == nil {
			continue
		}
		goPkg := def.pb.GetOptions().GetGoPackage()
		if idx := strings.Index(goPkg, ";"); idx != -1 {
			goPkg = goPkg[idx+1:]
		}

		var filteredMsgs []*descriptorpb.DescriptorProto
		for _, msg := range def.pb.MessageType {
			key := goPkg + ":" + msg.GetName()
			if _, ok := seenMsgs[key]; ok {
				continue
			}
			seenMsgs[key] = def.pb.GetName()
			filteredMsgs = append(filteredMsgs, msg)
		}
		def.pb.MessageType = filteredMsgs

		var filteredEnums []*descriptorpb.EnumDescriptorProto
		for _, enum := range def.pb.EnumType {
			key := goPkg + ":" + enum.GetName()
			if _, ok := seenEnums[key]; ok {
				continue
			}
			seenEnums[key] = def.pb.GetName()
			filteredEnums = append(filteredEnums, enum)
		}
		def.pb.EnumType = filteredEnums
	}
}

func computeMergedGoPackage(scc []string, modulePrefix string) string {
	var paths []string
	for _, p := range scc {
		idx := strings.Index(p, ";")
		if idx != -1 {
			paths = append(paths, p[:idx])
		} else {
			paths = append(paths, p)
		}
	}

	sort.Slice(paths, func(i, j int) bool {
		if strings.Contains(paths[i], "codeium_common") {
			return true
		}
		if strings.Contains(paths[j], "codeium_common") {
			return false
		}
		return len(paths[i]) > len(paths[j])
	})

	canonical := paths[0]
	base := path.Base(canonical)
	if base == "" || base == "." || base == "/" {
		base = "pb"
	}

	return canonical + ";" + base
}
