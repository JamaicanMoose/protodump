package protodump

import (
	"fmt"
	"reflect"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

func PruneDefinitions(defs []*ProtoDefinition, targetNames []string) ([]*ProtoDefinition, error) {
	var files protoregistry.Files
	for _, def := range defs {
		if def.descriptor != nil {
			files.RegisterFile(def.descriptor)
		}
	}

	// Re-link descriptors using full registry to resolve cross-file option extensions
	var relinkedFiles protoregistry.Files
	for _, def := range defs {
		if def.descriptor != nil {
			if fd, err := (protodesc.FileOptions{AllowUnresolvable: true}).New(def.pb, &files); err == nil {
				def.descriptor = fd
			}
			relinkedFiles.RegisterFile(def.descriptor)
		}
	}
	files = relinkedFiles

	var targets []protoreflect.Descriptor
	for _, targetName := range targetNames {
		var target protoreflect.Descriptor
		if d, err := files.FindDescriptorByName(protoreflect.FullName(targetName)); err == nil {
			target = d
		} else {
			files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
				if d := findDescriptor(fd, protoreflect.FullName(targetName)); d != nil {
					target = d
					return false // break
				}
				return true
			})
		}

		if target == nil {
			return nil, fmt.Errorf("target %s not found in parsed files", targetName)
		}
		targets = append(targets, target)
	}

	required := make(map[string]bool)
	processedFileOpts := make(map[string]bool)
	var queue []protoreflect.Descriptor

	var processOptions func(opts proto.Message)

	var add func(d protoreflect.Descriptor)
	add = func(d protoreflect.Descriptor) {
		if d == nil {
			return
		}
		if d.IsPlaceholder() {
			if realDesc, err := files.FindDescriptorByName(d.FullName()); err == nil {
				d = realDesc
			}
		}
		name := string(d.FullName())
		if !required[name] {
			required[name] = true
			if !d.IsPlaceholder() {
				queue = append(queue, d)
			}

			if parent, ok := d.Parent().(protoreflect.MessageDescriptor); ok {
				add(parent)
			}

			if pf := d.ParentFile(); pf != nil && !processedFileOpts[string(pf.Path())] {
				processedFileOpts[string(pf.Path())] = true
				processOptions(pf.Options())
			}
		}
	}

	processOptions = func(opts proto.Message) {
		if opts == nil {
			return
		}
		rv := reflect.ValueOf(opts)
		if rv.Kind() == reflect.Pointer && rv.IsNil() {
			return
		}

		m := opts.ProtoReflect()
		if !m.IsValid() {
			return
		}

		var inspectValue func(v protoreflect.Value, fd protoreflect.FieldDescriptor)
		inspectValue = func(v protoreflect.Value, fd protoreflect.FieldDescriptor) {
			if fd.IsExtension() {
				add(fd)
			}
			if fd.Message() != nil {
				add(fd.Message())
			}
			if fd.Enum() != nil {
				add(fd.Enum())
			}

			if fd.IsList() {
				list := v.List()
				for i := 0; i < list.Len(); i++ {
					elem := list.Get(i)
					if fd.Message() != nil && elem.Message().IsValid() {
						elem.Message().Range(func(innerFd protoreflect.FieldDescriptor, innerVal protoreflect.Value) bool {
							inspectValue(innerVal, innerFd)
							return true
						})
					} else if fd.Enum() != nil {
						if enumDesc := fd.Enum(); enumDesc != nil {
							if enumVal := enumDesc.Values().ByNumber(elem.Enum()); enumVal != nil {
								add(enumVal)
							}
						}
					}
				}
			} else if fd.IsMap() {
				mp := v.Map()
				mp.Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
					mapValueDesc := fd.MapValue()
					if mapValueDesc.Message() != nil && mv.Message().IsValid() {
						add(mapValueDesc.Message())
						mv.Message().Range(func(innerFd protoreflect.FieldDescriptor, innerVal protoreflect.Value) bool {
							inspectValue(innerVal, innerFd)
							return true
						})
					} else if mapValueDesc.Enum() != nil {
						add(mapValueDesc.Enum())
						if enumDesc := mapValueDesc.Enum(); enumDesc != nil {
							if enumVal := enumDesc.Values().ByNumber(mv.Enum()); enumVal != nil {
								add(enumVal)
							}
						}
					}
					return true
				})
			} else {
				if fd.Enum() != nil {
					if enumDesc := fd.Enum(); enumDesc != nil {
						if enumVal := enumDesc.Values().ByNumber(v.Enum()); enumVal != nil {
							add(enumVal)
						}
					}
				} else if fd.Message() != nil && v.Message().IsValid() {
					v.Message().Range(func(innerFd protoreflect.FieldDescriptor, innerVal protoreflect.Value) bool {
						inspectValue(innerVal, innerFd)
						return true
					})
				}
			}
		}

		m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			inspectValue(v, fd)
			return true
		})
	}

	for _, target := range targets {
		add(target)
	}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		processOptions(curr.Options())

		switch c := curr.(type) {
		case protoreflect.MessageDescriptor:
			fields := c.Fields()
			for i := 0; i < fields.Len(); i++ {
				f := fields.Get(i)
				processOptions(f.Options())
				if f.Message() != nil {
					add(f.Message())
				}
				if f.Enum() != nil {
					add(f.Enum())
				}
			}
			oneofs := c.Oneofs()
			for i := 0; i < oneofs.Len(); i++ {
				o := oneofs.Get(i)
				processOptions(o.Options())
			}
		case protoreflect.EnumDescriptor:
			vals := c.Values()
			for i := 0; i < vals.Len(); i++ {
				v := vals.Get(i)
				processOptions(v.Options())
			}
		case protoreflect.ServiceDescriptor:
			methods := c.Methods()
			for i := 0; i < methods.Len(); i++ {
				m := methods.Get(i)
				processOptions(m.Options())
				add(m.Input())
				add(m.Output())
			}
		case protoreflect.FieldDescriptor:
			if c.IsExtension() && c.ContainingMessage() != nil {
				add(c.ContainingMessage())
			}
			if c.Message() != nil {
				add(c.Message())
			}
			if c.Enum() != nil {
				add(c.Enum())
			}
		case protoreflect.MethodDescriptor:
			add(c.Input())
			add(c.Output())
		}
	}

	keptFiles := make(map[string]bool)
	var prunedDefs []*ProtoDefinition

	// Prune ast nodes
	for _, def := range defs {
		pb := def.pb
		pkg := pb.GetPackage()
		pb.MessageType = pruneMessages(pb.MessageType, pkg, required)
		pb.EnumType = pruneEnums(pb.EnumType, pkg, required)
		pb.Service = pruneServices(pb.Service, pkg, required)
		pb.Extension = pruneExtensions(pb.Extension, pkg, required)

		if len(pb.MessageType) > 0 || len(pb.EnumType) > 0 || len(pb.Service) > 0 || len(pb.Extension) > 0 {
			keptFiles[pb.GetName()] = true
			prunedDefs = append(prunedDefs, def)
		}
	}

	// Resolve new dependencies and rewrite strings
	var prunedFiles protoregistry.Files

	var finalDefs []*ProtoDefinition
	for _, def := range prunedDefs {
		var newDeps []string
		for _, dep := range def.pb.Dependency {
			if keptFiles[dep] {
				newDeps = append(newDeps, dep)
			}
		}
		def.pb.Dependency = newDeps

		// Recreate descriptor
		fd, err := protodesc.FileOptions{AllowUnresolvable: true}.New(def.pb, &prunedFiles)
		if err == nil {
			prunedFiles.RegisterFile(fd)
			def.descriptor = fd
			finalDefs = append(finalDefs, def)
		}
	}

	return finalDefs, nil
}

func findDescriptor(fd protoreflect.FileDescriptor, name protoreflect.FullName) protoreflect.Descriptor {
	msgs := fd.Messages()
	for i := 0; i < msgs.Len(); i++ {
		if d := findInMessage(msgs.Get(i), name); d != nil {
			return d
		}
	}
	enums := fd.Enums()
	for i := 0; i < enums.Len(); i++ {
		if enums.Get(i).FullName() == name {
			return enums.Get(i)
		}
	}
	svcs := fd.Services()
	for i := 0; i < svcs.Len(); i++ {
		if svcs.Get(i).FullName() == name {
			return svcs.Get(i)
		}
	}
	exts := fd.Extensions()
	for i := 0; i < exts.Len(); i++ {
		if exts.Get(i).FullName() == name {
			return exts.Get(i)
		}
	}
	return nil
}

func findInMessage(md protoreflect.MessageDescriptor, name protoreflect.FullName) protoreflect.Descriptor {
	if md.FullName() == name {
		return md
	}
	msgs := md.Messages()
	for i := 0; i < msgs.Len(); i++ {
		if d := findInMessage(msgs.Get(i), name); d != nil {
			return d
		}
	}
	enums := md.Enums()
	for i := 0; i < enums.Len(); i++ {
		if enums.Get(i).FullName() == name {
			return enums.Get(i)
		}
	}
	exts := md.Extensions()
	for i := 0; i < exts.Len(); i++ {
		if exts.Get(i).FullName() == name {
			return exts.Get(i)
		}
	}
	return nil
}

func qualify(pkg, name string) string {
	if pkg == "" {
		return name
	}
	return pkg + "." + name
}

func pruneMessages(msgs []*descriptorpb.DescriptorProto, prefix string, required map[string]bool) []*descriptorpb.DescriptorProto {
	var res []*descriptorpb.DescriptorProto
	for _, m := range msgs {
		fqn := qualify(prefix, m.GetName())
		if required[fqn] {
			m.NestedType = pruneMessages(m.NestedType, fqn, required)
			m.EnumType = pruneEnums(m.EnumType, fqn, required)
			m.Extension = pruneExtensions(m.Extension, fqn, required)
			res = append(res, m)
		}
	}
	return res
}

func pruneEnums(enums []*descriptorpb.EnumDescriptorProto, prefix string, required map[string]bool) []*descriptorpb.EnumDescriptorProto {
	var res []*descriptorpb.EnumDescriptorProto
	for _, e := range enums {
		fqn := qualify(prefix, e.GetName())
		if required[fqn] {
			res = append(res, e)
		}
	}
	return res
}

func pruneServices(svcs []*descriptorpb.ServiceDescriptorProto, prefix string, required map[string]bool) []*descriptorpb.ServiceDescriptorProto {
	var res []*descriptorpb.ServiceDescriptorProto
	for _, s := range svcs {
		fqn := qualify(prefix, s.GetName())
		if required[fqn] {
			res = append(res, s)
		}
	}
	return res
}

func pruneExtensions(exts []*descriptorpb.FieldDescriptorProto, prefix string, required map[string]bool) []*descriptorpb.FieldDescriptorProto {
	var res []*descriptorpb.FieldDescriptorProto
	for _, e := range exts {
		fqn := qualify(prefix, e.GetName())
		if required[fqn] {
			res = append(res, e)
		}
	}
	return res
}
