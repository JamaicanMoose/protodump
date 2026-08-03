package protodump

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestPruneParentMessageEnqueuing(t *testing.T) {
	// Outer message containing Inner message.
	// Outer message has a field of type SharedDependency.
	// Target for pruning is Outer.Inner.
	// Outer must be kept AND SharedDependency must be traversed and kept.
	fd := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("test.proto"),
		Package: proto.String("testpkg"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Outer"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     proto.String("shared"),
						Number:   proto.Int32(1),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: proto.String(".testpkg.SharedDependency"),
					},
				},
				NestedType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("Inner"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:   proto.String("val"),
								Number: proto.Int32(1),
								Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
							},
						},
					},
				},
			},
			{
				Name: proto.String("SharedDependency"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   proto.String("data"),
						Number: proto.Int32(1),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					},
				},
			},
		},
	}

	var registry protoregistry.Files
	desc, err := protodesc.FileOptions{AllowUnresolvable: true}.New(fd, &registry)
	require.NoError(t, err)

	def := &ProtoDefinition{
		pb:         fd,
		descriptor: desc,
	}

	pruned, err := PruneDefinitions([]*ProtoDefinition{def}, []string{"testpkg.Outer.Inner"})
	require.NoError(t, err)
	require.Len(t, pruned, 1)

	// Verify that both Outer (with Inner) and SharedDependency are retained in pruned AST
	msgNames := make(map[string]bool)
	for _, m := range pruned[0].pb.MessageType {
		msgNames[m.GetName()] = true
	}

	assert.True(t, msgNames["Outer"], "Outer should be kept as parent of target Outer.Inner")
	assert.True(t, msgNames["SharedDependency"], "SharedDependency should be kept as a dependency of Outer's fields")
}

func TestPruneCrossFilePlaceholderResolution(t *testing.T) {
	// file1: service.proto referencing ResponseMsg in types.proto
	fdService := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("service.proto"),
		Package:    proto.String("pkg"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"types.proto"},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("MyService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String("GetData"),
						InputType:  proto.String(".pkg.Empty"),
						OutputType: proto.String(".pkg.ResponseMsg"),
					},
				},
			},
		},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Empty")},
		},
	}

	// file2: types.proto defining ResponseMsg which has a field of type SubType
	fdTypes := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("types.proto"),
		Package: proto.String("pkg"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("ResponseMsg"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     proto.String("sub"),
						Number:   proto.Int32(1),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: proto.String(".pkg.SubType"),
					},
				},
			},
			{
				Name: proto.String("SubType"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   proto.String("val"),
						Number: proto.Int32(1),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					},
				},
			},
		},
	}

	// Compile each file standalone with empty registry (so cross-file refs are placeholders)
	descService, err := protodesc.FileOptions{AllowUnresolvable: true}.New(fdService, &protoregistry.Files{})
	require.NoError(t, err)

	descTypes, err := protodesc.FileOptions{AllowUnresolvable: true}.New(fdTypes, &protoregistry.Files{})
	require.NoError(t, err)

	defs := []*ProtoDefinition{
		{pb: fdService, descriptor: descService},
		{pb: fdTypes, descriptor: descTypes},
	}

	pruned, err := PruneDefinitions(defs, []string{"pkg.MyService"})
	require.NoError(t, err)
	require.Len(t, pruned, 2)

	typesMsgNames := make(map[string]bool)
	for _, def := range pruned {
		if def.pb.GetName() == "types.proto" {
			for _, m := range def.pb.MessageType {
				typesMsgNames[m.GetName()] = true
			}
		}
	}

	assert.True(t, typesMsgNames["ResponseMsg"], "ResponseMsg should be kept")
	assert.True(t, typesMsgNames["SubType"], "SubType should be kept as dependency of ResponseMsg")
}

func TestPruneExtensionAsTarget(t *testing.T) {
	// options.proto: defines a top-level extension extending FileOptions
	fdOptions := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("options.proto"),
		Package: proto.String("optpkg"),
		Syntax:  proto.String("proto2"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		Extension: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     proto.String("my_file_opt"),
				Number:   proto.Int32(50001),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Extendee: proto.String(".google.protobuf.FileOptions"),
			},
			{
				Name:     proto.String("unused_opt"),
				Number:   proto.Int32(50002),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
				Extendee: proto.String(".google.protobuf.FileOptions"),
			},
		},
	}

	desc, err := protodesc.FileOptions{AllowUnresolvable: true}.New(fdOptions, &protoregistry.Files{})
	require.NoError(t, err)

	def := &ProtoDefinition{pb: fdOptions, descriptor: desc}

	pruned, err := PruneDefinitions([]*ProtoDefinition{def}, []string{"optpkg.my_file_opt"})
	require.NoError(t, err)
	require.Len(t, pruned, 1)

	extNames := make(map[string]bool)
	for _, ext := range pruned[0].pb.Extension {
		extNames[ext.GetName()] = true
	}

	assert.True(t, extNames["my_file_opt"], "Target extension my_file_opt should be kept")
	assert.False(t, extNames["unused_opt"], "Unused extension unused_opt should be pruned")
}

func TestPruneMessageWithCustomOptionDependency(t *testing.T) {
	// options.proto defines MyCustomOption message and an extension extending MessageOptions of type MyCustomOption
	fdOptions := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("options.proto"),
		Package: proto.String("optpkg"),
		Syntax:  proto.String("proto2"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("MyCustomOption"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     proto.String("info"),
						Number:   proto.Int32(1),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					},
				},
			},
		},
		Extension: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     proto.String("msg_opt"),
				Number:   proto.Int32(50003),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
				TypeName: proto.String(".optpkg.MyCustomOption"),
				Extendee: proto.String(".google.protobuf.MessageOptions"),
			},
		},
	}

	// app.proto defines TargetMessage which uses msg_opt in options
	fdApp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("app.proto"),
		Package:    proto.String("apppkg"),
		Syntax:     proto.String("proto2"),
		Dependency: []string{"options.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("TargetMessage"),
				Options: &descriptorpb.MessageOptions{},
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   proto.String("id"),
						Number: proto.Int32(1),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					},
				},
			},
			{
				Name: proto.String("UnusedMessage"),
			},
		},
	}

	// Register options.proto in registry so app.proto can build option extension
	var reg protoregistry.Files
	descOptions, err := protodesc.FileOptions{AllowUnresolvable: true}.New(fdOptions, &reg)
	require.NoError(t, err)
	reg.RegisterFile(descOptions)

	descApp, err := protodesc.FileOptions{AllowUnresolvable: true}.New(fdApp, &reg)
	require.NoError(t, err)

	// Set custom option on TargetMessage
	msgOpts := &descriptorpb.MessageOptions{}
	extDesc := descOptions.Extensions().ByName("msg_opt")
	require.NotNil(t, extDesc)
	extType := dynamicpb.NewExtensionType(extDesc)
	optMsgDesc := descOptions.Messages().ByName("MyCustomOption")
	require.NotNil(t, optMsgDesc)
	customMsgVal := dynamicpb.NewMessage(optMsgDesc)
	customMsgVal.Set(optMsgDesc.Fields().ByName("info"), protoreflect.ValueOfString("hello"))
	proto.SetExtension(msgOpts, extType, customMsgVal)
	fdApp.MessageType[0].Options = msgOpts

	defs := []*ProtoDefinition{
		{pb: fdOptions, descriptor: descOptions},
		{pb: fdApp, descriptor: descApp},
	}

	pruned, err := PruneDefinitions(defs, []string{"apppkg.TargetMessage"})
	require.NoError(t, err)
	require.NotEmpty(t, pruned)

	appMsgNames := make(map[string]bool)
	for _, def := range pruned {
		if def.pb.GetName() == "app.proto" {
			for _, m := range def.pb.MessageType {
				appMsgNames[m.GetName()] = true
			}
		}
	}

	assert.True(t, appMsgNames["TargetMessage"], "TargetMessage should be kept")
	assert.False(t, appMsgNames["UnusedMessage"], "UnusedMessage should be pruned")

	optionsMsgNames := make(map[string]bool)
	optionsExtNames := make(map[string]bool)
	for _, def := range pruned {
		if def.pb.GetName() == "options.proto" {
			for _, m := range def.pb.MessageType {
				optionsMsgNames[m.GetName()] = true
			}
			for _, ext := range def.pb.Extension {
				optionsExtNames[ext.GetName()] = true
			}
		}
	}
	assert.True(t, optionsMsgNames["MyCustomOption"], "MyCustomOption should be kept as option dependency")
	assert.True(t, optionsExtNames["msg_opt"], "msg_opt extension should be kept as option dependency")
}

func TestPruneNestedExtension(t *testing.T) {
	// Outer message containing nested extension extending MessageOptions
	fd := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("test.proto"),
		Package: proto.String("testpkg"),
		Syntax:  proto.String("proto2"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Outer"),
				Extension: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     proto.String("inner_opt"),
						Number:   proto.Int32(50004),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						Extendee: proto.String(".google.protobuf.MessageOptions"),
					},
					{
						Name:     proto.String("unused_inner_opt"),
						Number:   proto.Int32(50005),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						Extendee: proto.String(".google.protobuf.MessageOptions"),
					},
				},
			},
		},
	}

	var registry protoregistry.Files
	desc, err := protodesc.FileOptions{AllowUnresolvable: true}.New(fd, &registry)
	require.NoError(t, err)

	def := &ProtoDefinition{
		pb:         fd,
		descriptor: desc,
	}

	pruned, err := PruneDefinitions([]*ProtoDefinition{def}, []string{"testpkg.Outer.inner_opt"})
	require.NoError(t, err)
	require.Len(t, pruned, 1)

	require.Len(t, pruned[0].pb.MessageType, 1)
	outer := pruned[0].pb.MessageType[0]
	assert.Equal(t, "Outer", outer.GetName())

	extNames := make(map[string]bool)
	for _, ext := range outer.Extension {
		extNames[ext.GetName()] = true
	}

	assert.True(t, extNames["inner_opt"], "inner_opt should be kept as target")
	assert.False(t, extNames["unused_inner_opt"], "unused_inner_opt should be pruned")
}

func TestPruneRepeatedEnumOption(t *testing.T) {
	// options.proto: defines enum MyEnum and repeated enum extension on MessageOptions
	fdOptions := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("options.proto"),
		Package: proto.String("optpkg"),
		Syntax:  proto.String("proto2"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		EnumType: []*descriptorpb.EnumDescriptorProto{
			{
				Name: proto.String("MyEnum"),
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: proto.String("FOO"), Number: proto.Int32(0)},
					{Name: proto.String("BAR"), Number: proto.Int32(1)},
				},
			},
		},
		Extension: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     proto.String("repeated_enum_opt"),
				Number:   proto.Int32(50006),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(),
				TypeName: proto.String(".optpkg.MyEnum"),
				Extendee: proto.String(".google.protobuf.MessageOptions"),
			},
		},
	}

	fdApp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("app.proto"),
		Package:    proto.String("apppkg"),
		Syntax:     proto.String("proto2"),
		Dependency: []string{"options.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("TargetMessage"),
				Options: &descriptorpb.MessageOptions{},
			},
		},
	}

	var reg protoregistry.Files
	descOptions, err := protodesc.FileOptions{AllowUnresolvable: true}.New(fdOptions, &reg)
	require.NoError(t, err)
	reg.RegisterFile(descOptions)

	descApp, err := protodesc.FileOptions{AllowUnresolvable: true}.New(fdApp, &reg)
	require.NoError(t, err)

	msgOpts := &descriptorpb.MessageOptions{}
	extDesc := descOptions.Extensions().ByName("repeated_enum_opt")
	require.NotNil(t, extDesc)
	extType := dynamicpb.NewExtensionType(extDesc)

	listVal := extType.New().List()
	listVal.Append(protoreflect.ValueOfEnum(1))
	proto.SetExtension(msgOpts, extType, listVal)
	fdApp.MessageType[0].Options = msgOpts

	defs := []*ProtoDefinition{
		{pb: fdOptions, descriptor: descOptions},
		{pb: fdApp, descriptor: descApp},
	}

	pruned, err := PruneDefinitions(defs, []string{"apppkg.TargetMessage"})
	require.NoError(t, err)
	require.NotEmpty(t, pruned)
}


